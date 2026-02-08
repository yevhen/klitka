package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"github.com/creack/pty"

	klitkav1 "github.com/yevhen/klitka/proto/gen/go/klitka/v1"
)

type HostBackend struct {
	ID     string
	Root   string
	Mounts []Mount
	Env    []string
}

type Mount struct {
	GuestPath string
	HostPath  string
	Mode      klitkav1.MountMode
}

func newHostBackend(id string, req *klitkav1.StartVMRequest, env []string) (*HostBackend, error) {
	root, err := os.MkdirTemp("", fmt.Sprintf("klitka-%s-", id))
	if err != nil {
		return nil, err
	}
	if err := prepareRoot(root); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	mounts, err := buildMounts(root, req.GetMounts())
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	return &HostBackend{ID: id, Root: root, Mounts: mounts, Env: env}, nil
}

func (backend *HostBackend) Close() error {
	if backend == nil {
		return nil
	}
	if backend.Root != "" {
		_ = os.RemoveAll(backend.Root)
	}
	return nil
}

func (backend *HostBackend) Exec(ctx context.Context, command string, args []string) (*klitkav1.ExecResponse, error) {
	command, args = backend.RewriteCommand(command, args)
	cmd := commandFromArgs(ctx, command, args)
	cmd.Env = mergeEnv(os.Environ(), backend.Env)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	exitCode := int32(0)
	if err := cmd.Run(); err != nil {
		exitCode = exitCodeFromError(err, &stderr)
	}

	return &klitkav1.ExecResponse{
		ExitCode: exitCode,
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
	}, nil
}

func (backend *HostBackend) ExecStream(
	ctx context.Context,
	start *klitkav1.ExecStart,
	stream *connect.BidiStream[klitkav1.ExecStreamRequest, klitkav1.ExecStreamResponse],
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	command, args := backend.RewriteCommand(start.GetCommand(), start.GetArgs())
	cmd := commandFromArgs(ctx, command, args)
	cmd.Env = mergeEnv(os.Environ(), backend.Env)

	stdin, stdout, stderr, ptyFile, err := startCommand(cmd, start.GetPty())
	if err != nil {
		_ = stream.Send(execOutput("stderr", []byte(err.Error())))
		_ = stream.Send(execExit(127))
		return nil
	}

	sendMu := sync.Mutex{}
	send := func(resp *klitkav1.ExecStreamResponse) error {
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

	go func() {
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

func (backend *HostBackend) RewriteCommand(command string, args []string) (string, []string) {
	command = backend.rewritePath(command, false)
	if len(args) == 0 {
		return command, args
	}

	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = backend.rewritePath(arg, true)
	}
	return command, out
}

func (backend *HostBackend) rewritePath(input string, allowRoot bool) string {
	if !strings.HasPrefix(input, string(os.PathSeparator)) {
		return input
	}

	for _, mount := range backend.Mounts {
		guest := mount.GuestPath
		if input == guest {
			return mount.HostPath
		}
		if strings.HasPrefix(input, guest+string(os.PathSeparator)) {
			suffix := strings.TrimPrefix(input, guest)
			return filepath.Clean(mount.HostPath + suffix)
		}
	}

	if !allowRoot || backend.Root == "" {
		return input
	}

	return backend.rootPath(input)
}

func prepareRoot(root string) error {
	return os.MkdirAll(filepath.Join(root, "tmp"), 0o755)
}

func (backend *HostBackend) rootPath(input string) string {
	if input == string(os.PathSeparator) {
		return backend.Root
	}
	trimmed := strings.TrimPrefix(input, string(os.PathSeparator))
	target := filepath.Join(backend.Root, trimmed)
	parent := filepath.Dir(target)
	if parent != "" && parent != backend.Root {
		_ = os.MkdirAll(parent, 0o755)
	}
	return target
}

func buildMounts(root string, mounts []*klitkav1.Mount) ([]Mount, error) {
	if len(mounts) == 0 {
		return nil, nil
	}

	out := make([]Mount, 0, len(mounts))
	for _, mount := range mounts {
		guestPath := filepath.Clean(mount.GetGuestPath())
		if guestPath == "." || guestPath == "" || !filepath.IsAbs(guestPath) {
			return nil, fmt.Errorf("invalid guest path: %q", mount.GetGuestPath())
		}

		hostPath := filepath.Clean(mount.GetHostPath())
		if hostPath == "." || hostPath == "" {
			return nil, fmt.Errorf("invalid host path: %q", mount.GetHostPath())
		}

		if _, err := os.Stat(hostPath); err != nil {
			return nil, fmt.Errorf("host path not found: %s", hostPath)
		}

		mode := mount.GetMode()
		if mode == klitkav1.MountMode_MOUNT_MODE_UNSPECIFIED {
			mode = klitkav1.MountMode_MOUNT_MODE_RO
		}

		resolvedHostPath := hostPath
		if mode == klitkav1.MountMode_MOUNT_MODE_RO {
			copyPath := filepath.Join(root, "mounts", strings.TrimPrefix(guestPath, string(os.PathSeparator)))
			if err := copyTree(hostPath, copyPath); err != nil {
				return nil, err
			}
			if err := makeReadOnly(copyPath); err != nil {
				return nil, err
			}
			resolvedHostPath = copyPath
		}

		out = append(out, Mount{
			GuestPath: guestPath,
			HostPath:  resolvedHostPath,
			Mode:      mode,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		return len(out[i].GuestPath) > len(out[j].GuestPath)
	})

	return out, nil
}

func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst, info.Mode())
	}

	return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode()&os.ModePerm)
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Chmod(mode & os.ModePerm); err != nil {
		return err
	}
	return nil
}

func makeReadOnly(path string) error {
	return filepath.WalkDir(path, func(entryPath string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode() & os.ModePerm
		mode &^= 0o222
		return os.Chmod(entryPath, mode)
	})
}
