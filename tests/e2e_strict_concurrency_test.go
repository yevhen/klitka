//go:build !windows

package tests

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	klitkav1 "github.com/yevhen/klitka/proto/gen/go/klitka/v1"
	klitkav1connect "github.com/yevhen/klitka/proto/gen/go/klitka/v1/klitkav1connect"
)

func TestE2EStrictMultiVMConcurrentIsolation(t *testing.T) {
	requireStrictTransport(t)
	requireVMBackend(t)
	addr := startTestDaemon(t)
	client := newTCPDaemonClient(addr)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start http server: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	targetURL := fmt.Sprintf("http://localhost:%d", listener.Addr().(*net.TCPAddr).Port)
	vmAID := startStrictVM(t, ctx, client, []string{"localhost"})
	vmBID := startStrictVM(t, ctx, client, []string{"example.com"})
	defer stopVMNoFail(t, client, vmAID)
	defer stopVMNoFail(t, client, vmBID)

	respA, err := client.Exec(ctx, connect.NewRequest(&klitkav1.ExecRequest{
		VmId:    vmAID,
		Command: "curl",
		Args:    []string{"-fsS", targetURL},
	}))
	if err != nil {
		t.Fatalf("vm A exec failed: %v", err)
	}
	if respA.Msg.GetExitCode() != 0 {
		t.Fatalf("vm A expected allowed request to succeed, exit=%d", respA.Msg.GetExitCode())
	}

	respB, err := client.Exec(ctx, connect.NewRequest(&klitkav1.ExecRequest{
		VmId:    vmBID,
		Command: "curl",
		Args:    []string{"-fsS", targetURL},
	}))
	if err != nil {
		t.Fatalf("vm B exec failed: %v", err)
	}
	if respB.Msg.GetExitCode() == 0 {
		t.Fatalf("vm B expected blocked request to fail")
	}
}

func TestE2EStrictStopOneVMOtherContinues(t *testing.T) {
	requireStrictTransport(t)
	requireVMBackend(t)
	addr := startTestDaemon(t)
	client := newTCPDaemonClient(addr)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start http server: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	targetURL := fmt.Sprintf("http://localhost:%d", listener.Addr().(*net.TCPAddr).Port)
	vmAID := startStrictVM(t, ctx, client, []string{"localhost"})
	vmBID := startStrictVM(t, ctx, client, []string{"localhost"})
	defer stopVMNoFail(t, client, vmBID)

	if _, err := client.StopVM(ctx, connect.NewRequest(&klitkav1.StopVMRequest{VmId: vmAID})); err != nil {
		t.Fatalf("failed to stop vm A: %v", err)
	}

	respB, err := client.Exec(ctx, connect.NewRequest(&klitkav1.ExecRequest{
		VmId:    vmBID,
		Command: "curl",
		Args:    []string{"-fsS", targetURL},
	}))
	if err != nil {
		t.Fatalf("vm B exec failed after vm A stop: %v", err)
	}
	if respB.Msg.GetExitCode() != 0 {
		t.Fatalf("vm B should continue working after vm A stop, exit=%d", respB.Msg.GetExitCode())
	}
}

func TestE2EStrictRepeatedStartStopCycles(t *testing.T) {
	requireStrictTransport(t)
	requireVMBackend(t)
	addr := startTestDaemon(t)
	client := newTCPDaemonClient(addr)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start http server: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	targetURL := fmt.Sprintf("http://localhost:%d", listener.Addr().(*net.TCPAddr).Port)

	for cycle := 0; cycle < 3; cycle++ {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		vmID := startStrictVM(t, ctx, client, []string{"localhost"})
		resp, err := client.Exec(ctx, connect.NewRequest(&klitkav1.ExecRequest{
			VmId:    vmID,
			Command: "curl",
			Args:    []string{"-fsS", targetURL},
		}))
		if err != nil {
			cancel()
			t.Fatalf("cycle %d exec failed: %v", cycle, err)
		}
		if resp.Msg.GetExitCode() != 0 {
			cancel()
			t.Fatalf("cycle %d expected success, exit=%d", cycle, resp.Msg.GetExitCode())
		}
		if _, err := client.StopVM(ctx, connect.NewRequest(&klitkav1.StopVMRequest{VmId: vmID})); err != nil {
			cancel()
			t.Fatalf("cycle %d stop failed: %v", cycle, err)
		}
		cancel()
	}
}

func startStrictVM(t *testing.T, ctx context.Context, client klitkav1connect.DaemonServiceClient, allowHosts []string) string {
	t.Helper()

	resp, err := client.StartVM(ctx, connect.NewRequest(&klitkav1.StartVMRequest{
		Network: &klitkav1.NetworkPolicy{
			AllowHosts:         allowHosts,
			BlockPrivateRanges: false,
			EgressMode:         klitkav1.EgressMode_EGRESS_MODE_STRICT,
		},
	}))
	if err != nil {
		t.Fatalf("start vm failed: %v", err)
	}
	return resp.Msg.GetVmId()
}

func stopVMNoFail(t *testing.T, client klitkav1connect.DaemonServiceClient, vmID string) {
	t.Helper()
	if vmID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = client.StopVM(ctx, connect.NewRequest(&klitkav1.StopVMRequest{VmId: vmID}))
}
