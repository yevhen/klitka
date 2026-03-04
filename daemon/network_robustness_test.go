package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"connectrpc.com/connect"
	klitkav1 "github.com/yevhen/klitka/proto/gen/go/klitka/v1"
)

type fakeBackend struct{}

func (fakeBackend) Exec(context.Context, string, []string) (*klitkav1.ExecResponse, error) {
	return &klitkav1.ExecResponse{ExitCode: 0}, nil
}

func (fakeBackend) ExecStream(context.Context, *klitkav1.ExecStart, *connect.BidiStream[klitkav1.ExecStreamRequest, klitkav1.ExecStreamResponse]) error {
	return nil
}

func (fakeBackend) Close() error {
	return nil
}

type strictRegistry struct {
	mu     sync.Mutex
	active map[string]struct{}
}

func newStrictRegistry() *strictRegistry {
	return &strictRegistry{active: map[string]struct{}{}}
}

func (registry *strictRegistry) newEnforcer(vmID string, _ int) (strictTransportEnforcer, error) {
	anchorID := "anchor/" + vmID
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.active[anchorID]; exists {
		return nil, fmt.Errorf("anchor already exists: %s", anchorID)
	}
	registry.active[anchorID] = struct{}{}
	return &registryEnforcer{anchorID: anchorID, registry: registry}, nil
}

func (registry *strictRegistry) count() int {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return len(registry.active)
}

func (registry *strictRegistry) contains(anchorID string) bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	_, ok := registry.active[anchorID]
	return ok
}

func (registry *strictRegistry) clearAll() {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for anchorID := range registry.active {
		delete(registry.active, anchorID)
	}
}

type registryEnforcer struct {
	anchorID string
	registry *strictRegistry
	once     sync.Once
}

func (enforcer *registryEnforcer) AnchorID() string {
	return enforcer.anchorID
}

func (enforcer *registryEnforcer) Close() error {
	enforcer.once.Do(func() {
		enforcer.registry.mu.Lock()
		delete(enforcer.registry.active, enforcer.anchorID)
		enforcer.registry.mu.Unlock()
	})
	return nil
}

func TestStrictResidueCleanupPrepareBeforeFactory(t *testing.T) {
	registry := withFakeStrictRuntime(t)
	staleAnchor := "anchor/stale-from-crash"
	registry.mu.Lock()
	registry.active[staleAnchor] = struct{}{}
	registry.mu.Unlock()

	strictTransportPrepare = func() error {
		registry.clearAll()
		return nil
	}
	strictTransportFactory = func(vmID string, proxyPort int) (strictTransportEnforcer, error) {
		if registry.count() != 0 {
			return nil, errors.New("stale strict anchors detected")
		}
		return registry.newEnforcer(vmID, proxyPort)
	}

	vm, err := newVM("vm-residue", strictRequestWithAllowHost("residue.test"))
	if err != nil {
		t.Fatalf("expected residue cleanup to allow strict startup, got: %v", err)
	}
	if err := closeVMForTest(vm); err != nil {
		t.Fatalf("failed to close vm: %v", err)
	}
}

func TestStrictRepeatedStartStopNoAnchorLeak(t *testing.T) {
	registry := withFakeStrictRuntime(t)

	for i := 0; i < 8; i++ {
		vmID := fmt.Sprintf("vm-cycle-%d", i)
		vm, err := newVM(vmID, strictRequestWithAllowHost("cycle.test"))
		if err != nil {
			t.Fatalf("start cycle %d failed: %v", i, err)
		}
		if registry.count() != 1 {
			t.Fatalf("expected one active anchor after start cycle %d, got %d", i, registry.count())
		}
		if err := closeVMForTest(vm); err != nil {
			t.Fatalf("close cycle %d failed: %v", i, err)
		}
		if registry.count() != 0 {
			t.Fatalf("expected zero active anchors after cycle %d, got %d", i, registry.count())
		}
	}
}

func TestStrictMultiVMConcurrentIsolation(t *testing.T) {
	registry := withFakeStrictRuntime(t)

	hosts := []string{"alpha.test", "beta.test", "gamma.test"}
	vms := make([]*VM, len(hosts))
	errCh := make(chan error, len(hosts))
	var wg sync.WaitGroup

	for i, host := range hosts {
		i := i
		host := host
		wg.Add(1)
		go func() {
			defer wg.Done()
			vm, err := newVM(fmt.Sprintf("vm-%d", i), strictRequestWithAllowHost(host))
			if err != nil {
				errCh <- err
				return
			}
			vms[i] = vm
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent strict startup failed: %v", err)
		}
	}

	if registry.count() != len(hosts) {
		t.Fatalf("expected %d active anchors, got %d", len(hosts), registry.count())
	}

	for i, vm := range vms {
		if vm == nil {
			t.Fatalf("vm %d is nil", i)
		}
		if err := vm.Network.policy.checkHost(hosts[i]); err != nil {
			t.Fatalf("vm %d should allow own host %s: %v", i, hosts[i], err)
		}
		for j, host := range hosts {
			if i == j {
				continue
			}
			if err := vm.Network.policy.checkHost(host); err == nil {
				t.Fatalf("vm %d should block foreign host %s", i, host)
			}
		}
	}

	for _, vm := range vms {
		if err := closeVMForTest(vm); err != nil {
			t.Fatalf("failed to close vm: %v", err)
		}
	}
	if registry.count() != 0 {
		t.Fatalf("expected all anchors cleaned up, got %d", registry.count())
	}
}

func TestStrictStopOneVMKeepsOtherActive(t *testing.T) {
	registry := withFakeStrictRuntime(t)

	vmA, err := newVM("vm-a", strictRequestWithAllowHost("a.test"))
	if err != nil {
		t.Fatalf("start vm-a failed: %v", err)
	}
	vmB, err := newVM("vm-b", strictRequestWithAllowHost("b.test"))
	if err != nil {
		t.Fatalf("start vm-b failed: %v", err)
	}

	if err := closeVMForTest(vmA); err != nil {
		t.Fatalf("stop vm-a failed: %v", err)
	}

	if registry.count() != 1 {
		t.Fatalf("expected one active anchor after stopping vm-a, got %d", registry.count())
	}
	if !registry.contains("anchor/vm-b") {
		t.Fatalf("expected vm-b anchor to remain active")
	}

	if err := vmB.Network.policy.checkHost("b.test"); err != nil {
		t.Fatalf("vm-b policy should still work: %v", err)
	}
	if err := vmB.Network.policy.checkHost("a.test"); err == nil {
		t.Fatalf("vm-b should not inherit vm-a policy")
	}

	if err := closeVMForTest(vmB); err != nil {
		t.Fatalf("stop vm-b failed: %v", err)
	}
	if registry.count() != 0 {
		t.Fatalf("expected all anchors cleaned after stopping vm-b, got %d", registry.count())
	}
}

func withFakeStrictRuntime(t *testing.T) *strictRegistry {
	t.Helper()

	registry := newStrictRegistry()
	originalPrepare := strictTransportPrepare
	originalFactory := strictTransportFactory
	originalVMBackendFactory := vmBackendFactory

	strictTransportPrepare = func() error { return nil }
	strictTransportFactory = registry.newEnforcer
	vmBackendFactory = func(string, *klitkav1.StartVMRequest, []string, *networkManager) (ExecBackend, error) {
		return fakeBackend{}, nil
	}
	t.Setenv("KLITKA_BACKEND", "vm")

	t.Cleanup(func() {
		strictTransportPrepare = originalPrepare
		strictTransportFactory = originalFactory
		vmBackendFactory = originalVMBackendFactory
	})

	return registry
}

func closeVMForTest(vm *VM) error {
	if vm == nil {
		return nil
	}
	closeErr := vm.Backend.Close()
	if vm.Network != nil {
		closeErr = errors.Join(closeErr, vm.Network.Close())
	}
	return closeErr
}

func strictRequestWithAllowHost(host string) *klitkav1.StartVMRequest {
	return &klitkav1.StartVMRequest{
		Network: &klitkav1.NetworkPolicy{
			AllowHosts:         []string{host},
			BlockPrivateRanges: false,
			EgressMode:         klitkav1.EgressMode_EGRESS_MODE_STRICT,
		},
	}
}
