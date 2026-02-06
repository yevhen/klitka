package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"connectrpc.com/connect"
	klitkav1 "github.com/klitka/klitka/proto/gen/go/klitka/v1"
)

type ExecBackend interface {
	Exec(ctx context.Context, command string, args []string) (*klitkav1.ExecResponse, error)
	ExecStream(ctx context.Context, start *klitkav1.ExecStart, stream *connect.BidiStream[klitkav1.ExecStreamRequest, klitkav1.ExecStreamResponse]) error
	Close() error
}

type VM struct {
	ID      string
	Backend ExecBackend
	Network *networkManager
}

type backendMode string

const (
	backendAuto backendMode = "auto"
	backendHost backendMode = "host"
	backendVM   backendMode = "vm"
)

var ErrVMUnavailable = errors.New("vm backend unavailable")

func newVM(id string, req *klitkav1.StartVMRequest) (*VM, error) {
	network, err := newNetworkManager(req)
	if err != nil {
		return nil, err
	}
	env := []string{}
	if network != nil {
		env = network.env
	}

	mode := backendModeFromEnv()
	if mode == backendVM || mode == backendAuto {
		backend, err := newVMBackend(id, req, env, network)
		if err == nil {
			return &VM{ID: id, Backend: backend, Network: network}, nil
		}
		if mode == backendVM {
			if network != nil {
				_ = network.Close()
			}
			return nil, err
		}
		if !errors.Is(err, ErrVMUnavailable) {
			if network != nil {
				_ = network.Close()
			}
			return nil, err
		}
	}

	backend, err := newHostBackend(id, req, env)
	if err != nil {
		if network != nil {
			_ = network.Close()
		}
		return nil, err
	}
	return &VM{ID: id, Backend: backend, Network: network}, nil
}

func backendModeFromEnv() backendMode {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("KLITKA_BACKEND")))
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
