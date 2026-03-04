package daemon

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	klitkav1 "github.com/yevhen/klitka/proto/gen/go/klitka/v1"
)

type failingCloseBackend struct{}

func (failingCloseBackend) Exec(context.Context, string, []string) (*klitkav1.ExecResponse, error) {
	return &klitkav1.ExecResponse{ExitCode: 0}, nil
}

func (failingCloseBackend) ExecStream(context.Context, *klitkav1.ExecStart, *connect.BidiStream[klitkav1.ExecStreamRequest, klitkav1.ExecStreamResponse]) error {
	return nil
}

func (failingCloseBackend) Close() error {
	return errors.New("backend close failed")
}

func TestServiceStopVMClosesNetworkOnBackendCloseError(t *testing.T) {
	strict := &fakeStrictTransport{anchorID: "anchor-service"}
	service := NewService()
	service.vms["vm-service"] = &VM{
		ID:      "vm-service",
		Backend: failingCloseBackend{},
		Network: &networkManager{strictTransport: strict},
	}

	_, err := service.StopVM(context.Background(), connect.NewRequest(&klitkav1.StopVMRequest{VmId: "vm-service"}))
	if err == nil {
		t.Fatalf("expected stop to fail when backend close fails")
	}
	if strict.closeCalls != 1 {
		t.Fatalf("expected strict transport close to be called once, got %d", strict.closeCalls)
	}
}
