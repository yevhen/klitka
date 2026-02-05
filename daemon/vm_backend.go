package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"connectrpc.com/connect"

	klitkavmv1 "github.com/klitkavm/klitkavm/proto/gen/go/klitkavm/v1"
)

type vmBackend struct {
	id         string
	assets     guestAssets
	qemu       *exec.Cmd
	conn       net.Conn
	client     *virtioClient
	tempDir    string
	socketPath string
	mounts     []vmMount
	env        []string
	waitCh     chan error
	closeOnce  sync.Once
}

type guestAssets struct {
	kernel string
	initrd string
	append string
}

type vmMount struct {
	tag        string
	hostPath   string
	guestPath  string
	socketPath string
	mode       klitkavmv1.MountMode
	process    *exec.Cmd
}

func newVMBackend(id string, req *klitkavmv1.StartVMRequest, env []string) (*vmBackend, error) {
	assets, err := resolveGuestAssets()
	if err != nil {
		return nil, err
	}

	qemuPath, err := resolveQemuPath()
	if err != nil {
		return nil, err
	}

	tempDir, err := createTempDir(fmt.Sprintf("klitkavm-vm-%s-", id))
	if err != nil {
		return nil, err
	}
	socketPath := filepath.Join(tempDir, "virtio.sock")

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, err
	}

	mounts, mountArgs, appendArgs, err := prepareVmMounts(req.GetMounts(), tempDir)
	if err != nil {
		_ = listener.Close()
		_ = os.RemoveAll(tempDir)
		return nil, err
	}

	append := buildKernelAppend(assets.append, appendArgs)
	cmd := exec.Command(qemuPath, buildQemuArgs(assets, socketPath, append, mountArgs)...)
	cmd.Stdout = qemuOutputWriter()
	cmd.Stderr = qemuOutputWriter()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	backend := &vmBackend{
		id:         id,
		assets:     assets,
		qemu:       cmd,
		tempDir:    tempDir,
		socketPath: socketPath,
		mounts:     mounts,
		env:        env,
		waitCh:     make(chan error, 1),
	}

	if err := cmd.Start(); err != nil {
		_ = listener.Close()
		_ = backend.Close()
		return nil, err
	}

	go func() {
		backend.waitCh <- cmd.Wait()
	}()

	conn, err := acceptWithTimeout(listener, 10*time.Second)
	_ = listener.Close()
	if err != nil {
		_ = backend.Close()
		return nil, err
	}
	backend.conn = conn
	backend.client = newVirtioClient(conn)

	return backend, nil
}

func (backend *vmBackend) Exec(ctx context.Context, command string, args []string) (*klitkavmv1.ExecResponse, error) {
	req, err := backend.client.startExec(command, args, false, false, backend.env)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	stdout, stderr, exitCode, err := collectExecOutput(req)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return &klitkavmv1.ExecResponse{
		ExitCode: exitCode,
		Stdout:   stdout,
		Stderr:   stderr,
	}, nil
}

func (backend *vmBackend) ExecStream(
	ctx context.Context,
	start *klitkavmv1.ExecStart,
	stream *connect.BidiStream[klitkavmv1.ExecStreamRequest, klitkavmv1.ExecStreamResponse],
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := backend.client.startExec(start.GetCommand(), start.GetArgs(), true, start.GetPty(), backend.env)
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

	inputErr := make(chan error, 1)
	go func() {
		for {
			msg, recvErr := stream.Receive()
			if recvErr != nil {
				if errors.Is(recvErr, io.EOF) {
					_ = backend.client.sendStdin(req.id, nil, true)
					return
				}
				inputErr <- recvErr
				cancel()
				return
			}
			if input := msg.GetInput(); input != nil {
				if len(input.GetData()) > 0 {
					_ = backend.client.sendStdin(req.id, input.GetData(), false)
				}
				if input.GetEof() {
					_ = backend.client.sendStdin(req.id, nil, true)
					return
				}
			}
			if resize := msg.GetResize(); resize != nil {
				_ = backend.client.sendResize(req.id, resize.GetRows(), resize.GetCols())
			}
		}
	}()

	for {
		select {
		case output, ok := <-req.output:
			if ok {
				if err := send(execOutput(output.stream, output.data)); err != nil {
					return err
				}
			}
		case exitMsg, ok := <-req.exit:
			if ok {
				if err := send(execExit(exitMsg.code)); err != nil {
					return err
				}
				return nil
			}
		case err := <-req.err:
			if err != nil {
				return err
			}
		case err := <-inputErr:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (backend *vmBackend) Close() error {
	var err error
	backend.closeOnce.Do(func() {
		if backend.client != nil {
			_ = backend.client.Close()
		}
		if backend.conn != nil {
			_ = backend.conn.Close()
		}
		if backend.qemu != nil && backend.qemu.Process != nil {
			_ = backend.qemu.Process.Signal(syscall.SIGTERM)
			select {
			case <-backend.waitCh:
			case <-time.After(2 * time.Second):
				_ = backend.qemu.Process.Kill()
				<-backend.waitCh
			}
		}
		for _, mount := range backend.mounts {
			if mount.process == nil || mount.process.Process == nil {
				continue
			}
			_ = mount.process.Process.Signal(syscall.SIGTERM)
			done := make(chan struct{})
			go func(cmd *exec.Cmd) {
				_, _ = cmd.Process.Wait()
				close(done)
			}(mount.process)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = mount.process.Process.Kill()
			}
		}
		if backend.tempDir != "" {
			_ = os.RemoveAll(backend.tempDir)
		}
	})
	return err
}

func resolveGuestAssets() (guestAssets, error) {
	kernel := strings.TrimSpace(os.Getenv("KLITKAVM_GUEST_KERNEL"))
	initrd := strings.TrimSpace(os.Getenv("KLITKAVM_GUEST_INITRD"))
	append := strings.TrimSpace(os.Getenv("KLITKAVM_GUEST_APPEND"))

	if kernel == "" || initrd == "" {
		fallbackKernel, fallbackInitrd := defaultGuestPaths()
		kernel = firstExistingPath(kernel, fallbackKernel)
		initrd = firstExistingPath(initrd, fallbackInitrd)
	}

	if kernel == "" || initrd == "" {
		return guestAssets{}, vmUnavailable("set KLITKAVM_GUEST_KERNEL and KLITKAVM_GUEST_INITRD")
	}
	if _, err := os.Stat(kernel); err != nil {
		return guestAssets{}, vmUnavailable(fmt.Sprintf("kernel not found: %s", kernel))
	}
	if _, err := os.Stat(initrd); err != nil {
		return guestAssets{}, vmUnavailable(fmt.Sprintf("initrd not found: %s", initrd))
	}

	if append == "" {
		append = defaultKernelAppend()
	}

	return guestAssets{kernel: kernel, initrd: initrd, append: append}, nil
}

func defaultGuestPaths() (string, string) {
	candidates := []string{}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "guest", "image", "out"))
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(exeDir, "..", "guest", "image", "out"))
	}

	for _, base := range candidates {
		kernel := filepath.Join(base, "vmlinuz")
		initrd := filepath.Join(base, "initramfs.cpio.gz")
		if fileExists(kernel) && fileExists(initrd) {
			return kernel, initrd
		}
	}
	return "", ""
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func firstExistingPath(primary, fallback string) string {
	if fileExists(primary) {
		return primary
	}
	if fileExists(fallback) {
		return fallback
	}
	return ""
}

func resolveQemuPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("KLITKAVM_QEMU")); override != "" {
		if _, err := exec.LookPath(override); err != nil {
			return "", vmUnavailable(fmt.Sprintf("qemu not found: %s", override))
		}
		return override, nil
	}

	candidate := "qemu-system-x86_64"
	if runtime.GOARCH == "arm64" {
		candidate = "qemu-system-aarch64"
	}
	if _, err := exec.LookPath(candidate); err != nil {
		return "", vmUnavailable(fmt.Sprintf("qemu not found: %s", candidate))
	}
	return candidate, nil
}

func resolveVirtiofsdPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("KLITKAVM_VIRTIOFSD")); override != "" {
		if _, err := exec.LookPath(override); err != nil {
			return "", vmUnavailable(fmt.Sprintf("virtiofsd not found: %s", override))
		}
		return override, nil
	}
	if _, err := exec.LookPath("virtiofsd"); err != nil {
		return "", vmUnavailable("virtiofsd not found")
	}
	return "virtiofsd", nil
}

func defaultKernelAppend() string {
	consoleDevice := "ttyS0"
	if runtime.GOARCH == "arm64" {
		consoleDevice = "ttyAMA0"
	}
	return fmt.Sprintf("console=%s", consoleDevice)
}

func buildKernelAppend(base string, extras []string) string {
	if len(extras) == 0 {
		return base
	}
	if base == "" {
		return strings.Join(extras, " ")
	}
	return strings.TrimSpace(base + " " + strings.Join(extras, " "))
}

func buildQemuArgs(assets guestAssets, socketPath string, appendArg string, mountArgs []string) []string {
	args := []string{
		"-nodefaults",
		"-no-reboot",
		"-m",
		"512M",
		"-smp",
		"2",
		"-kernel",
		assets.kernel,
		"-initrd",
		assets.initrd,
		"-append",
		appendArg,
		"-nographic",
		"-serial",
		"stdio",
	}

	machineType := selectMachineType()
	if machineType != "" {
		args = append(args, "-machine", machineType)
	}
	accel := selectAccel()
	if accel != "" {
		args = append(args, "-accel", accel)
	}
	cpu := selectCPU()
	if cpu != "" {
		args = append(args, "-cpu", cpu)
	}

	args = append(args,
		"-netdev",
		"user,id=net0",
		"-device",
		"virtio-net-pci,netdev=net0",
		"-chardev",
		fmt.Sprintf("socket,id=virtiocon0,path=%s,server=off", socketPath),
		"-device",
		"virtio-serial-pci,id=virtio-serial0",
		"-device",
		"virtserialport,chardev=virtiocon0,name=virtio-port,bus=virtio-serial0.0",
	)

	args = append(args, mountArgs...)
	return args
}

func prepareVmMounts(mounts []*klitkavmv1.Mount, tempDir string) ([]vmMount, []string, []string, error) {
	if len(mounts) == 0 {
		return nil, nil, nil, nil
	}
	virtiofsdPath, err := resolveVirtiofsdPath()
	if err != nil {
		return nil, nil, nil, err
	}

	out := make([]vmMount, 0, len(mounts))
	qemuArgs := []string{}
	appendArgs := []string{}

	cleanup := func() {
		for _, mount := range out {
			if mount.process != nil && mount.process.Process != nil {
				_ = mount.process.Process.Kill()
			}
		}
	}

	for idx, mount := range mounts {
		guestPath := filepath.Clean(mount.GetGuestPath())
		if guestPath == "." || guestPath == "" || !filepath.IsAbs(guestPath) {
			return nil, nil, nil, fmt.Errorf("invalid guest path: %q", mount.GetGuestPath())
		}
		hostPath := filepath.Clean(mount.GetHostPath())
		if hostPath == "." || hostPath == "" {
			return nil, nil, nil, fmt.Errorf("invalid host path: %q", mount.GetHostPath())
		}
		if _, err := os.Stat(hostPath); err != nil {
			return nil, nil, nil, fmt.Errorf("host path not found: %s", hostPath)
		}

		mode := mount.GetMode()
		if mode == klitkavmv1.MountMode_MOUNT_MODE_UNSPECIFIED {
			mode = klitkavmv1.MountMode_MOUNT_MODE_RO
		}

		tag := fmt.Sprintf("klitkavm%d", idx)
		socketPath := filepath.Join(tempDir, fmt.Sprintf("virtiofs-%d.sock", idx))

		args := []string{"--socket-path", socketPath, "--shared-dir", hostPath}
		if mode == klitkavmv1.MountMode_MOUNT_MODE_RO {
			args = append(args, "--readonly")
		}

		cmd := exec.Command(virtiofsdPath, args...)
		cmd.Stdout = qemuOutputWriter()
		cmd.Stderr = qemuOutputWriter()
		if err := cmd.Start(); err != nil {
			cleanup()
			return nil, nil, nil, fmt.Errorf("start virtiofsd: %w", err)
		}

		out = append(out, vmMount{
			tag:        tag,
			hostPath:   hostPath,
			guestPath:  guestPath,
			socketPath: socketPath,
			mode:       mode,
			process:    cmd,
		})

		qemuArgs = append(qemuArgs,
			"-chardev", fmt.Sprintf("socket,id=fs%d,path=%s", idx, socketPath),
			"-device", fmt.Sprintf("vhost-user-fs-pci,chardev=fs%d,tag=%s", idx, tag),
		)

		appendArgs = append(appendArgs, fmt.Sprintf("klitkavm.mount=%s:%s:%s", tag, guestPath, mountModeString(mode)))
	}

	return out, qemuArgs, appendArgs, nil
}

func mountModeString(mode klitkavmv1.MountMode) string {
	if mode == klitkavmv1.MountMode_MOUNT_MODE_RO {
		return "ro"
	}
	return "rw"
}

func selectMachineType() string {
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		return "microvm"
	}
	if runtime.GOARCH == "arm64" {
		return "virt"
	}
	return "q35"
}

func selectAccel() string {
	switch runtime.GOOS {
	case "linux":
		return "kvm"
	case "darwin":
		return "hvf"
	default:
		return "tcg"
	}
}

func selectCPU() string {
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		return "host"
	}
	return "max"
}

func acceptWithTimeout(listener net.Listener, timeout time.Duration) (net.Conn, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	type acceptResult struct {
		conn net.Conn
		err  error
	}
	ch := make(chan acceptResult, 1)
	go func() {
		conn, err := listener.Accept()
		ch <- acceptResult{conn: conn, err: err}
	}()

	select {
	case result := <-ch:
		return result.conn, result.err
	case <-timer.C:
		return nil, fmt.Errorf("timeout waiting for virtio connection")
	}
}

func qemuOutputWriter() io.Writer {
	if strings.TrimSpace(os.Getenv("KLITKAVM_DEBUG_QEMU")) != "" {
		return os.Stdout
	}
	return io.Discard
}

func createTempDir(prefix string) (string, error) {
	base := strings.TrimSpace(os.Getenv("KLITKAVM_TMPDIR"))
	if base == "" {
		base = os.TempDir()
	}

	create := func(dir string) (string, error) {
		tmp, err := os.MkdirTemp(dir, prefix)
		if err != nil {
			return "", err
		}
		socketPath := filepath.Join(tmp, "virtio.sock")
		if len(socketPath) > 96 {
			_ = os.RemoveAll(tmp)
			return "", fmt.Errorf("temp dir path too long for unix socket: %s", socketPath)
		}
		return tmp, nil
	}

	if tmp, err := create(base); err == nil {
		return tmp, nil
	}

	if base != "/tmp" {
		if tmp, err := create("/tmp"); err == nil {
			return tmp, nil
		}
	}

	return "", fmt.Errorf("failed to create temp dir for unix socket")
}
