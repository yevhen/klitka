package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"

	klitkavmv1 "github.com/klitkavm/klitkavm/proto/gen/go/klitkavm/v1"
)

func commandFromArgs(ctx context.Context, command string, args []string) *exec.Cmd {
	return exec.CommandContext(ctx, command, args...)
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
				select {
				case errCh <- sendErr:
				default:
				}
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) || errors.Is(err, syscall.EIO) {
				return
			}
			select {
			case errCh <- err:
			default:
			}
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

func mergeEnv(base []string, overrides []string) []string {
	if len(overrides) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(overrides))
	index := map[string]int{}

	add := func(entry string) {
		key := entry
		if idx := strings.Index(entry, "="); idx >= 0 {
			key = entry[:idx]
		}
		if pos, ok := index[key]; ok {
			out[pos] = entry
			return
		}
		index[key] = len(out)
		out = append(out, entry)
	}

	for _, entry := range base {
		add(entry)
	}
	for _, entry := range overrides {
		if entry == "" {
			continue
		}
		add(entry)
	}

	return out
}
