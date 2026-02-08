package daemon

import (
	"context"
	"fmt"
	"sync"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	klitkav1 "github.com/yevhen/klitka/proto/gen/go/klitka/v1"
)

type Service struct {
	mu  sync.Mutex
	vms map[string]*VM
}

func NewService() *Service {
	return &Service{vms: make(map[string]*VM)}
}

func (s *Service) StartVM(
	_ context.Context,
	req *connect.Request[klitkav1.StartVMRequest],
) (*connect.Response[klitkav1.StartVMResponse], error) {
	vmID := uuid.NewString()
	vm, err := newVM(vmID, req.Msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	s.mu.Lock()
	s.vms[vmID] = vm
	s.mu.Unlock()

	return connect.NewResponse(&klitkav1.StartVMResponse{VmId: vmID}), nil
}

func (s *Service) Exec(
	ctx context.Context,
	req *connect.Request[klitkav1.ExecRequest],
) (*connect.Response[klitkav1.ExecResponse], error) {
	vmID := req.Msg.GetVmId()
	if vmID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("vm_id is required"))
	}

	vm, ok := s.getVM(vmID)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("vm %s not found", vmID))
	}

	command := req.Msg.GetCommand()
	if command == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("command is required"))
	}

	resp, err := vm.Backend.Exec(ctx, command, req.Msg.GetArgs())
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(resp), nil
}

func (s *Service) ExecStream(
	ctx context.Context,
	stream *connect.BidiStream[klitkav1.ExecStreamRequest, klitkav1.ExecStreamResponse],
) error {
	first, err := stream.Receive()
	if err != nil {
		return err
	}

	start := first.GetStart()
	if start == nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("first message must be start"))
	}
	if start.GetVmId() == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("vm_id is required"))
	}
	vm, ok := s.getVM(start.GetVmId())
	if !ok {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("vm %s not found", start.GetVmId()))
	}
	if start.GetCommand() == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("command is required"))
	}

	return vm.Backend.ExecStream(ctx, start, stream)
}

func (s *Service) StopVM(
	_ context.Context,
	req *connect.Request[klitkav1.StopVMRequest],
) (*connect.Response[klitkav1.StopVMResponse], error) {
	vmID := req.Msg.GetVmId()
	if vmID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("vm_id is required"))
	}

	s.mu.Lock()
	vm, ok := s.vms[vmID]
	if !ok {
		s.mu.Unlock()
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("vm %s not found", vmID))
	}
	delete(s.vms, vmID)
	s.mu.Unlock()

	if err := vm.Backend.Close(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if vm.Network != nil {
		if err := vm.Network.Close(); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	return connect.NewResponse(&klitkav1.StopVMResponse{}), nil
}

func (s *Service) getVM(vmID string) (*VM, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vm, ok := s.vms[vmID]
	return vm, ok
}
