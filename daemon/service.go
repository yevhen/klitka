package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"connectrpc.com/connect"
	"github.com/creack/pty"
	"github.com/google/uuid"

	klitkavmv1 "github.com/klitkavm/klitkavm/proto/gen/go/klitkavm/v1"
)

type Service struct {
	mu  sync.Mutex
	vms map[string]struct{}
}

func NewService() *Service {
	return &Service{vms: make(map[string]struct{})}
}

func (s *Service) StartVM(
	_ context.Context,
	_ *connect.Request[klitkavmv1.StartVMRequest],
) (*connect.Response[klitkavmv1.StartVMResponse], error) {
	vmID := uuid.NewString()
	s.mu.Lock()
	s.vms[vmID] = struct{}{}
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

	if !s.hasVM(vmID) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("vm %s not found", vmID))
	}

	command := req.Msg.GetCommand()
	if command == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("command is required"))
	}

	cmd := commandFromArgs(ctx, command, req.Msg.GetArgs())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	exitCode := int32(0)
	if err := cmd.Run(); err != nil {
		exitCode = exitCodeFromError(err, &stderr)
	}

	resp := &klitkavmv1.ExecResponse{
		ExitCode: exitCode,
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) ExecStream(
	ctx context.Context,
	stream *connect.BidiStream[klitkavmv1.ExecStreamRequest, klitkavmv1.ExecStreamResponse],
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

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
	if !s.hasVM(start.GetVmId()) {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("vm %s not found", start.GetVmId()))
	}
	if start.GetCommand() == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("command is required"))
	}

	cmd := commandFromArgs(ctx, start.GetCommand(), start.GetArgs())

	stdin, stdout, stderr, ptyFile, err := startCommand(cmd, start.GetPty())
	if err != nil {
		_ = stream.Send(execOutput("stderr", []byte(err.Error())))
		_ = stream.Send(execExit(127))
		return nil
	}

	sendMu := sync.Mutex{}
	send := func(resp *klitkavmv1.ExecStreamResponse) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		err := stream.Send(resp)
		if err != nil {
			cancel()
		}
		return err
	}

	outputErr := make(chan error, 1)
	wg := &sync.WaitGroup{}

	if stdout != nil {
		wg.Add(1)
		go readOutput(ctx, wg, stdout, "stdout", send, outputErr)
	}
	if stderr != nil {
		wg.Add(1)
		go readOutput(ctx, wg, stderr, "stderr", send, outputErr)
	}

	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		for {
			msg, recvErr := stream.Receive()
			if recvErr != nil {
				if errors.Is(recvErr, io.EOF) {
					sendEof(stdin, ptyFile)
					return
				}
				cancel()
				return
			}
			if input := msg.GetInput(); input != nil {
				if len(input.GetData()) > 0 {
					_, _ = stdin.Write(input.GetData())
				}
				if input.GetEof() {
					sendEof(stdin, ptyFile)
					return
				}
			}
			if resize := msg.GetResize(); resize != nil && ptyFile != nil {
				_ = pty.Setsize(ptyFile, &pty.Winsize{Rows: uint16(resize.GetRows()), Cols: uint16(resize.GetCols())})
			}
		}
	}()

	waitErr := cmd.Wait()
	if ptyFile != nil {
		_ = ptyFile.Close()
	}
	wg.Wait()

	exitCode := int32(0)
	if waitErr != nil {
		exitCode = exitCodeFromError(waitErr, nil)
	}

	if err := send(execExit(exitCode)); err != nil {
		return err
	}

	select {
	case err := <-outputErr:
		if err != nil {
			return err
		}
	default:
	}

	return nil
}

func (s *Service) StopVM(
	_ context.Context,
	req *connect.Request[klitkavmv1.StopVMRequest],
) (*connect.Response[klitkavmv1.StopVMResponse], error) {
	vmID := req.Msg.GetVmId()
	if vmID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("vm_id is required"))
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.vms[vmID]; !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("vm %s not found", vmID))
	}
	delete(s.vms, vmID)

	return connect.NewResponse(&klitkavmv1.StopVMResponse{}), nil
}

func (s *Service) hasVM(vmID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.vms[vmID]
	return ok
}

func commandFromArgs(ctx context.Context, command string, args []string) *exec.Cmd {
	if len(args) > 0 {
		return exec.CommandContext(ctx, command, args...)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}

func startCommand(cmd *exec.Cmd, usePty bool) (io.WriteCloser, io.Reader, io.Reader, *os.File, error) {
	if usePty {
		ptyFile, err := pty.Start(cmd)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		return ptyFile, ptyFile, nil, ptyFile, nil
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, nil, err
	}
	return stdin, stdout, stderr, nil, nil
}

func readOutput(
	ctx context.Context,
	wg *sync.WaitGroup,
	reader io.Reader,
	stream string,
	send func(*klitkavmv1.ExecStreamResponse) error,
	errCh chan<- error,
) {
	defer wg.Done()
	buf := make([]byte, 32*1024)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := reader.Read(buf)
		if n > 0 {
			if sendErr := send(execOutput(stream, buf[:n])); sendErr != nil {
				errCh <- sendErr
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) || errors.Is(err, syscall.EIO) {
				return
			}
			errCh <- err
			return
		}
	}
}

func sendEof(stdin io.WriteCloser, ptyFile *os.File) {
	if ptyFile != nil {
		_, _ = stdin.Write([]byte{4})
		return
	}
	_ = stdin.Close()
}

func execOutput(stream string, data []byte) *klitkavmv1.ExecStreamResponse {
	return &klitkavmv1.ExecStreamResponse{
		Payload: &klitkavmv1.ExecStreamResponse_Output{
			Output: &klitkavmv1.ExecOutput{
				Stream: stream,
				Data:   data,
			},
		},
	}
}

func execExit(code int32) *klitkavmv1.ExecStreamResponse {
	return &klitkavmv1.ExecStreamResponse{
		Payload: &klitkavmv1.ExecStreamResponse_Exit{
			Exit: &klitkavmv1.ExecExit{ExitCode: code},
		},
	}
}

func exitCodeFromError(err error, stderr *bytes.Buffer) int32 {
	var exitErr *exec.ExitError
	var execErr *exec.Error
	var pathErr *os.PathError

	switch {
	case errors.As(err, &exitErr):
		return int32(exitErr.ExitCode())
	case errors.As(err, &execErr):
		if stderr != nil && stderr.Len() == 0 {
			_, _ = stderr.WriteString(execErr.Error())
		}
		return 127
	case errors.As(err, &pathErr):
		if stderr != nil && stderr.Len() == 0 {
			_, _ = stderr.WriteString(pathErr.Error())
		}
		return 127
	default:
		if stderr != nil && stderr.Len() == 0 {
			_, _ = stderr.WriteString(err.Error())
		}
		return 1
	}
}
