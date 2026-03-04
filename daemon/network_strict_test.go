package daemon

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"

	klitkav1 "github.com/yevhen/klitka/proto/gen/go/klitka/v1"
)

type fakeStrictTransport struct {
	anchorID   string
	closeCalls int
	closeErr   error
}

func (transport *fakeStrictTransport) AnchorID() string {
	return transport.anchorID
}

func (transport *fakeStrictTransport) Close() error {
	transport.closeCalls++
	return transport.closeErr
}

func TestNewNetworkManagerStrictTransportSetupFailure(t *testing.T) {
	stubStrictTransportPrepare(t)
	testErr := errors.New("pf apply failed")
	originalFactory := strictTransportFactory
	strictTransportFactory = func(string, int) (strictTransportEnforcer, error) {
		return nil, testErr
	}
	t.Cleanup(func() {
		strictTransportFactory = originalFactory
	})

	manager, err := newNetworkManager("vm-strict", strictRequest())
	if err == nil {
		t.Fatalf("expected strict transport setup error")
	}
	if manager != nil {
		t.Fatalf("expected nil manager on strict setup failure")
	}
	if !strings.Contains(err.Error(), "strict transport setup failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewVMStrictRollbackClosesTransportOnBackendError(t *testing.T) {
	stubStrictTransportPrepare(t)
	transport := &fakeStrictTransport{anchorID: "anchor-test"}

	originalTransportFactory := strictTransportFactory
	strictTransportFactory = func(string, int) (strictTransportEnforcer, error) {
		return transport, nil
	}
	t.Cleanup(func() {
		strictTransportFactory = originalTransportFactory
	})

	originalVMBackendFactory := vmBackendFactory
	vmBackendFactory = func(string, *klitkav1.StartVMRequest, []string, *networkManager) (ExecBackend, error) {
		return nil, errors.New("vm backend boom")
	}
	t.Cleanup(func() {
		vmBackendFactory = originalVMBackendFactory
	})

	t.Setenv("KLITKA_BACKEND", "vm")

	vm, err := newVM("vm-rollback", strictRequest())
	if err == nil {
		t.Fatalf("expected vm backend error")
	}
	if vm != nil {
		t.Fatalf("expected vm to be nil on startup failure")
	}
	if transport.closeCalls != 1 {
		t.Fatalf("expected strict transport cleanup to run once, got %d", transport.closeCalls)
	}
}

func TestNetworkManagerStrictCloseIdempotent(t *testing.T) {
	stubStrictTransportPrepare(t)
	transport := &fakeStrictTransport{anchorID: "anchor-idempotent"}

	originalFactory := strictTransportFactory
	strictTransportFactory = func(string, int) (strictTransportEnforcer, error) {
		return transport, nil
	}
	t.Cleanup(func() {
		strictTransportFactory = originalFactory
	})

	manager, err := newNetworkManager("vm-idempotent", strictRequest())
	if err != nil {
		t.Fatalf("newNetworkManager failed: %v", err)
	}

	if err := manager.Close(); err != nil {
		t.Fatalf("first close failed: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("second close failed: %v", err)
	}

	if transport.closeCalls != 1 {
		t.Fatalf("expected strict transport close once, got %d", transport.closeCalls)
	}
}

func TestNewNetworkManagerStrictTransportPrepareFailure(t *testing.T) {
	prepareErr := errors.New("stale anchor cleanup failed")
	originalPrepare := strictTransportPrepare
	strictTransportPrepare = func() error {
		return prepareErr
	}
	t.Cleanup(func() {
		strictTransportPrepare = originalPrepare
	})

	manager, err := newNetworkManager("vm-prepare-fail", strictRequest())
	if err == nil {
		t.Fatalf("expected strict transport prepare error")
	}
	if manager != nil {
		t.Fatalf("expected nil manager on strict prepare failure")
	}
	if !strings.Contains(err.Error(), "strict transport prepare failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNetworkManagerStrictLogIncludesVMAndAnchor(t *testing.T) {
	stubStrictTransportPrepare(t)
	transport := &fakeStrictTransport{anchorID: "anchor-log"}

	originalFactory := strictTransportFactory
	strictTransportFactory = func(string, int) (strictTransportEnforcer, error) {
		return transport, nil
	}
	t.Cleanup(func() {
		strictTransportFactory = originalFactory
	})

	loggerOutput := &bytes.Buffer{}
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(loggerOutput)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
	})

	manager, err := newNetworkManager("vm-log", strictRequest())
	if err != nil {
		t.Fatalf("newNetworkManager failed: %v", err)
	}
	_ = manager.Close()

	logs := loggerOutput.String()
	if !strings.Contains(logs, "network_enforcement=strict") {
		t.Fatalf("expected strict network_enforcement in logs, got: %s", logs)
	}
	if !strings.Contains(logs, "vm_id=vm-log") {
		t.Fatalf("expected vm_id in logs, got: %s", logs)
	}
	if !strings.Contains(logs, "anchor_id=anchor-log") {
		t.Fatalf("expected anchor_id in logs, got: %s", logs)
	}
}

func stubStrictTransportPrepare(t *testing.T) {
	t.Helper()
	originalPrepare := strictTransportPrepare
	strictTransportPrepare = func() error {
		return nil
	}
	t.Cleanup(func() {
		strictTransportPrepare = originalPrepare
	})
}

func strictRequest() *klitkav1.StartVMRequest {
	return &klitkav1.StartVMRequest{
		Network: &klitkav1.NetworkPolicy{
			EgressMode: klitkav1.EgressMode_EGRESS_MODE_STRICT,
		},
	}
}
