package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	klitkav1 "github.com/yevhen/klitka/proto/gen/go/klitka/v1"
)

func TestParseFSBackend(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    fsBackend
		wantErr bool
	}{
		{name: "default empty", raw: "", want: fsBackendAuto},
		{name: "auto", raw: "auto", want: fsBackendAuto},
		{name: "virtiofs", raw: "virtiofs", want: fsBackendVirtioFS},
		{name: "fsrpc", raw: "fsrpc", want: fsBackendFSRPC},
		{name: "trim and lower", raw: "  ViRTioFS  ", want: fsBackendVirtioFS},
		{name: "invalid", raw: "unknown", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFSBackend(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFSBackend() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("parseFSBackend() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveAutoFSBackend(t *testing.T) {
	tests := []struct {
		name             string
		goos             string
		ci               bool
		virtiofsdPresent bool
		want             fsBackend
	}{
		{name: "ci uses fsrpc on linux", goos: "linux", ci: true, virtiofsdPresent: true, want: fsBackendFSRPC},
		{name: "darwin uses fsrpc", goos: "darwin", ci: false, virtiofsdPresent: true, want: fsBackendFSRPC},
		{name: "linux uses virtiofs when available", goos: "linux", ci: false, virtiofsdPresent: true, want: fsBackendVirtioFS},
		{name: "linux falls back to fsrpc without virtiofsd", goos: "linux", ci: false, virtiofsdPresent: false, want: fsBackendFSRPC},
		{name: "other os falls back to fsrpc", goos: "freebsd", ci: false, virtiofsdPresent: true, want: fsBackendFSRPC},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveAutoFSBackend(tc.goos, tc.ci, tc.virtiofsdPresent)
			if got != tc.want {
				t.Fatalf("resolveAutoFSBackend(%q, ci=%v, virtio=%v) = %q, want %q", tc.goos, tc.ci, tc.virtiofsdPresent, got, tc.want)
			}
		})
	}
}

func TestEffectiveFSBackendHonorsExplicitValue(t *testing.T) {
	if got := effectiveFSBackend(fsBackendFSRPC); got != fsBackendFSRPC {
		t.Fatalf("effectiveFSBackend(fsrpc) = %q, want %q", got, fsBackendFSRPC)
	}
	if got := effectiveFSBackend(fsBackendVirtioFS); got != fsBackendVirtioFS {
		t.Fatalf("effectiveFSBackend(virtiofs) = %q, want %q", got, fsBackendVirtioFS)
	}
}

func TestFSRPCPortName(t *testing.T) {
	if got, want := fsrpcPortName(0), "virtio-fs-0"; got != want {
		t.Fatalf("fsrpcPortName(0) = %q, want %q", got, want)
	}
	if got, want := fsrpcPortName(12), "virtio-fs-12"; got != want {
		t.Fatalf("fsrpcPortName(12) = %q, want %q", got, want)
	}
}

func TestPrepareFSRPCMountsAddsNamedPorts(t *testing.T) {
	hostDir := t.TempDir()
	tempDir := t.TempDir()

	mounts := []*klitkav1.Mount{{
		HostPath:  hostDir,
		GuestPath: "/workspace",
		Mode:      klitkav1.MountMode_MOUNT_MODE_RO,
	}}

	vmMounts, qemuArgs, appendArgs, err := prepareVmMounts(mounts, tempDir, "q35", fsBackendFSRPC)
	if err != nil {
		t.Fatalf("prepareVmMounts() error = %v", err)
	}

	if len(vmMounts) != 1 {
		t.Fatalf("expected 1 vm mount, got %d", len(vmMounts))
	}
	if got, want := vmMounts[0].tag, "virtio-fs-0"; got != want {
		t.Fatalf("vm mount tag = %q, want %q", got, want)
	}
	if got := filepath.Base(vmMounts[0].socketPath); got != "fsrpc-0.sock" {
		t.Fatalf("unexpected fsrpc socket name: %q", got)
	}

	joinedArgs := strings.Join(qemuArgs, " ")
	if !strings.Contains(joinedArgs, "name=virtio-fs-0") {
		t.Fatalf("expected qemu args to contain named fsrpc port, got: %s", joinedArgs)
	}

	if len(appendArgs) != 1 {
		t.Fatalf("expected 1 append arg, got %d", len(appendArgs))
	}
	if got, want := appendArgs[0], "klitka.fsrpc=virtio-fs-0:/workspace:ro"; got != want {
		t.Fatalf("append arg = %q, want %q", got, want)
	}

	if _, err := os.Stat(hostDir); err != nil {
		t.Fatalf("expected host mount to exist: %v", err)
	}
}
