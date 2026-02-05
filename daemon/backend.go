package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"connectrpc.com/connect"
	klitkavmv1 "github.com/klitkavm/klitkavm/proto/gen/go/klitkavm/v1"
)

type ExecBackend interface {
	Exec(ctx context.Context, command string, args []string) (*klitkavmv1.ExecResponse, error)
	ExecStream(ctx context.Context, start *klitkavmv1.ExecStart, stream *connect.BidiStream[klitkavmv1.ExecStreamRequest, klitkavmv1.ExecStreamResponse]) error
	Close() error
}

type VM struct {
	ID      string
	Backend ExecBackend
}

type backendMode string

const (
	backendAuto backendMode = "auto"
	backendHost backendMode = "host"
	backendVM   backendMode = "vm"
)

var ErrVMUnavailable = errors.New("vm backend unavailable")

func newVM(id string, req *klitkavmv1.StartVMRequest) (*VM, error) {
	mode := backendModeFromEnv()
	if mode == backendVM || mode == backendAuto {
		backend, err := newVMBackend(id, req)
		if err == nil {
			return &VM{ID: id, Backend: backend}, nil
		}
		if mode == backendVM {
			return nil, err
		}
		if !errors.Is(err, ErrVMUnavailable) {
			return nil, err
		}
	}

	backend, err := newHostBackend(id, req)
	if err != nil {
		return nil, err
	}
	return &VM{ID: id, Backend: backend}, nil
}

func backendModeFromEnv() backendMode {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("KLITKAVM_BACKEND")))
	if raw == "" {
		return backendAuto
	}
	switch raw {
	case string(backendHost):
		return backendHost
	case string(backendVM):
		return backendVM
	default:
		return backendAuto
	}
}

func vmUnavailable(message string) error {
	if message == "" {
		return ErrVMUnavailable
	}
	return fmt.Errorf("%w: %s", ErrVMUnavailable, message)
}
