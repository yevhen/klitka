package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type fsBackend string

const (
	fsBackendAuto     fsBackend = "auto"
	fsBackendFSRPC    fsBackend = "fsrpc"
	fsBackendVirtioFS fsBackend = "virtiofs"
)

func parseFSBackend(raw string) (fsBackend, error) {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return fsBackendAuto, nil
	}

	switch fsBackend(value) {
	case fsBackendAuto, fsBackendFSRPC, fsBackendVirtioFS:
		return fsBackend(value), nil
	default:
		return "", fmt.Errorf("invalid KLITKA_FS_BACKEND: %q", raw)
	}
}

func fsBackendFromEnv() (fsBackend, error) {
	return parseFSBackend(os.Getenv("KLITKA_FS_BACKEND"))
}

func effectiveFSBackend(configured fsBackend) fsBackend {
	if configured != fsBackendAuto {
		return configured
	}
	return resolveAutoFSBackend(runtime.GOOS, runningInCI(), virtiofsdAvailable())
}

func resolveAutoFSBackend(goos string, ci bool, virtiofsAvailable bool) fsBackend {
	if ci {
		return fsBackendFSRPC
	}
	if goos == "darwin" {
		return fsBackendFSRPC
	}
	if goos == "linux" && virtiofsAvailable {
		return fsBackendVirtioFS
	}
	return fsBackendFSRPC
}

func runningInCI() bool {
	return strings.TrimSpace(os.Getenv("CI")) != ""
}

func virtiofsdAvailable() bool {
	candidate := strings.TrimSpace(os.Getenv("KLITKA_VIRTIOFSD"))
	if candidate == "" {
		candidate = "virtiofsd"
	}
	_, err := exec.LookPath(candidate)
	return err == nil
}

func fsrpcPortName(index int) string {
	return fmt.Sprintf("virtio-fs-%d", index)
}
