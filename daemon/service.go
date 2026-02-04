package daemon

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sync"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	klitkavmv1 "github.com/klitkavm/klitkavm/proto/gen/go/klitkavm/v1"
)

type VM struct {
	ID string
}

type Service struct {
	mu  sync.Mutex
	vms map[string]*VM
}

func NewService() *Service {
	return &Service{vms: make(map[string]*VM)}
}

func (s *Service) StartVM(
	ctx context.Context,
	_ *connect.Request[klitkavmv1.StartVMRequest],
) (*connect.Response[klitkavmv1.StartVMResponse], error) {
	_ = ctx
	vmID := uuid.NewString()
	s.mu.Lock()
	s.vms[vmID] = &VM{ID: vmID}
	s.mu.Unlock()

	return connect.NewResponse(&klitkavmv1.StartVMResponse{VmId: vmID}), nil
}

func (s *Service) Exec(
	ctx context.Context,
	req *connect.Request[klitkavmv1.ExecRequest],
) (*connect.Response[klitkavmv1.ExecResponse], error) {
	vmID := req.Msg.GetVmId()
	if vmID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("vm_id is required"))
	}

	s.mu.Lock()
	_, ok := s.vms[vmID]
	s.mu.Unlock()
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("vm %s not found", vmID))
	}

	command := req.Msg.GetCommand()
	if command == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("command is required"))
	}

	var cmd *exec.Cmd
	args := req.Msg.GetArgs()
	if len(args) > 0 {
		cmd = exec.CommandContext(ctx, command, args...)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	exitCode := int32(0)
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = int32(exitErr.ExitCode())
		} else {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("exec failed: %w", err))
		}
	}

	resp := &klitkavmv1.ExecResponse{
		ExitCode: exitCode,
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) StopVM(
	ctx context.Context,
	req *connect.Request[klitkavmv1.StopVMRequest],
) (*connect.Response[klitkavmv1.StopVMResponse], error) {
	_ = ctx
	vmID := req.Msg.GetVmId()
	if vmID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("vm_id is required"))
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.vms, vmID)

	return connect.NewResponse(&klitkavmv1.StopVMResponse{}), nil
}
