//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	klitkav1 "github.com/klitka/klitka/proto/gen/go/klitka/v1"
)

const defaultWslTcpAddr = "127.0.0.1:5711"

const (
	wslDistroEnv      = "KLITKA_WSL_DISTRO"
	wslRepoEnv        = "KLITKA_WSL_REPO"
	wslDaemonEnv      = "KLITKA_WSL_DAEMON_PATH"
	wslGuestDirEnv    = "KLITKA_WSL_GUEST_DIR"
	wslGuestKernelEnv = "KLITKA_WSL_GUEST_KERNEL"
	wslGuestInitrdEnv = "KLITKA_WSL_GUEST_INITRD"
)

type wslContext struct {
	exePath string
	distro  string
}

func isWindows() bool {
	return true
}

func defaultTcpAddr() string {
	return defaultWslTcpAddr
}

func ensureWslDaemon(ctx context.Context, tcpAddr string) (*wslContext, error) {
	wsl, err := newWslContext()
	if err != nil {
		return nil, err
	}
	if tcpAddr == "" {
		tcpAddr = defaultWslTcpAddr
	}
	if isTcpReachable(tcpAddr) {
		return wsl, nil
	}

	repoRootWin, repoRootWsl, err := resolveRepoRoot(wsl)
	if err != nil {
		return nil, err
	}

	daemonPath, err := resolveDaemonPath(wsl, repoRootWsl)
	if err != nil {
		return nil, err
	}

	if !wsl.fileExists(ctx, daemonPath) {
		if err := wsl.requireCommand(ctx, "go"); err != nil {
			return nil, err
		}
		buildCmd := fmt.Sprintf("cd %s && mkdir -p bin && go build -o %s ./cmd/klitka-daemon", shellEscape(repoRootWsl), shellEscape(daemonPath))
		if err := wsl.run(ctx, "bash", "-lc", buildCmd); err != nil {
			return nil, fmt.Errorf("build daemon in WSL failed: %w", err)
		}
	}

	kernelPath, initrdPath, err := resolveGuestAssets(wsl, repoRootWin, repoRootWsl)
	if err != nil {
		return nil, err
	}

	startCmd := strings.Join([]string{
		"KLITKA_BACKEND=vm",
		"KLITKA_TMPDIR=/tmp",
		"KLITKA_GUEST_KERNEL=" + shellEscape(kernelPath),
		"KLITKA_GUEST_INITRD=" + shellEscape(initrdPath),
		"nohup",
		shellEscape(daemonPath),
		"--tcp",
		shellEscape(tcpAddr),
		">",
		shellEscape("/tmp/klitka-daemon.log"),
		"2>&1",
		"&",
	}, " ")

	if err := wsl.run(ctx, "bash", "-lc", startCmd); err != nil {
		return nil, fmt.Errorf("start daemon in WSL failed: %w", err)
	}

	if err := waitForTcp(tcpAddr, 15*time.Second); err != nil {
		return nil, err
	}

	return wsl, nil
}

func rewriteMountsForWsl(ctx context.Context, wsl *wslContext, mounts []*klitkav1.Mount) ([]*klitkav1.Mount, error) {
	if wsl == nil || len(mounts) == 0 {
		return mounts, nil
	}
	out := make([]*klitkav1.Mount, 0, len(mounts))
	for _, mount := range mounts {
		hostPath := strings.TrimSpace(mount.GetHostPath())
		if hostPath == "" {
			return nil, fmt.Errorf("invalid host path")
		}
		if !isLikelyWslPath(hostPath) {
			converted, err := wsl.toWslPath(ctx, hostPath)
			if err != nil {
				return nil, fmt.Errorf("convert mount path %q: %w", hostPath, err)
			}
			hostPath = converted
		}
		out = append(out, &klitkav1.Mount{
			HostPath:  hostPath,
			GuestPath: mount.GetGuestPath(),
			Mode:      mount.GetMode(),
		})
	}
	return out, nil
}

func newWslContext() (*wslContext, error) {
	exe, err := exec.LookPath("wsl.exe")
	if err != nil {
		exe, err = exec.LookPath("wsl")
	}
	if err != nil {
		return nil, fmt.Errorf("wsl.exe not found in PATH")
	}
	return &wslContext{
		exePath: exe,
		distro:  strings.TrimSpace(os.Getenv(wslDistroEnv)),
	}, nil
}

func resolveRepoRoot(wsl *wslContext) (string, string, error) {
	if override := strings.TrimSpace(os.Getenv(wslRepoEnv)); override != "" {
		if isLikelyWslPath(override) {
			return "", override, nil
		}
		converted, err := wsl.toWslPath(context.Background(), override)
		if err != nil {
			return "", "", err
		}
		return override, converted, nil
	}

	repoRoot, err := findRepoRoot()
	if err != nil {
		return "", "", err
	}
	wslPath, err := wsl.toWslPath(context.Background(), repoRoot)
	if err != nil {
		return repoRoot, "", err
	}
	return repoRoot, wslPath, nil
}

func resolveDaemonPath(wsl *wslContext, repoRootWsl string) (string, error) {
	if override := strings.TrimSpace(os.Getenv(wslDaemonEnv)); override != "" {
		if isLikelyWslPath(override) {
			return override, nil
		}
		return wsl.toWslPath(context.Background(), override)
	}
	if repoRootWsl == "" {
		return "", errors.New("KLITKA_WSL_DAEMON_PATH or KLITKA_WSL_REPO must be set")
	}
	return path.Join(repoRootWsl, "bin", "klitka-daemon"), nil
}

func resolveGuestAssets(wsl *wslContext, repoRootWin, repoRootWsl string) (string, string, error) {
	kernel := strings.TrimSpace(os.Getenv(wslGuestKernelEnv))
	initrd := strings.TrimSpace(os.Getenv(wslGuestInitrdEnv))

	if kernel == "" || initrd == "" {
		base := strings.TrimSpace(os.Getenv(wslGuestDirEnv))
		if base == "" {
			if repoRootWsl == "" {
				return "", "", errors.New("KLITKA_WSL_GUEST_DIR or KLITKA_WSL_REPO must be set")
			}
			base = path.Join(repoRootWsl, "guest", "image", "out")
		}
		kernel = path.Join(base, "vmlinuz")
		initrd = path.Join(base, "initramfs.cpio.gz")
	}

	if !wsl.fileExists(context.Background(), kernel) || !wsl.fileExists(context.Background(), initrd) {
		if repoRootWsl == "" {
			return "", "", fmt.Errorf("guest assets missing: %s %s", kernel, initrd)
		}
		buildCmd := fmt.Sprintf("cd %s && ./guest/image/build.sh", shellEscape(repoRootWsl))
		if err := wsl.run(context.Background(), "bash", "-lc", buildCmd); err != nil {
			return "", "", fmt.Errorf("build guest image in WSL failed (repo %s): %w", repoRootWin, err)
		}
	}

	if !wsl.fileExists(context.Background(), kernel) || !wsl.fileExists(context.Background(), initrd) {
		return "", "", fmt.Errorf("guest assets missing after build: %s %s", kernel, initrd)
	}

	return kernel, initrd, nil
}

func (wsl *wslContext) toWslPath(ctx context.Context, windowsPath string) (string, error) {
	args := wsl.commandArgs("wslpath", "-a", "-u", windowsPath)
	cmd := exec.CommandContext(ctx, wsl.exePath, args...)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("wslpath failed: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func (wsl *wslContext) run(ctx context.Context, command ...string) error {
	cmd := exec.CommandContext(ctx, wsl.exePath, wsl.commandArgs(command...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (wsl *wslContext) fileExists(ctx context.Context, target string) bool {
	cmd := exec.CommandContext(ctx, wsl.exePath, wsl.commandArgs("bash", "-lc", fmt.Sprintf("test -e %s", shellEscape(target)))...)
	return cmd.Run() == nil
}

func (wsl *wslContext) requireCommand(ctx context.Context, name string) error {
	cmd := exec.CommandContext(ctx, wsl.exePath, wsl.commandArgs("bash", "-lc", fmt.Sprintf("command -v %s >/dev/null", shellEscape(name)))...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s not found inside WSL; install it and retry", name)
	}
	return nil
}

func (wsl *wslContext) commandArgs(cmd ...string) []string {
	args := []string{}
	if wsl.distro != "" {
		args = append(args, "-d", wsl.distro)
	}
	args = append(args, "--")
	args = append(args, cmd...)
	return args
}

func isTcpReachable(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func waitForTcp(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if isTcpReachable(addr) {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for daemon at %s", addr)
}

func shellEscape(value string) string {
	if value == "" {
		return "''"
	}
	replacer := strings.ReplaceAll(value, "'", "'\"'\"'")
	return "'" + replacer + "'"
}

func findRepoRoot() (string, error) {
	if override := strings.TrimSpace(os.Getenv("KLITKA_REPO_ROOT")); override != "" {
		return override, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	path := cwd
	for {
		if fileExists(filepath.Join(path, "go.mod")) {
			return path, nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	return "", errors.New("could not find go.mod; set KLITKA_REPO_ROOT")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func isLikelyWslPath(value string) bool {
	return strings.HasPrefix(value, "/")
}
