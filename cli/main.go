package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"connectrpc.com/connect"

	"github.com/klitkavm/klitkavm/daemon"
	klitkavmv1 "github.com/klitkavm/klitkavm/proto/gen/go/klitkavm/v1"
	klitkavmv1connect "github.com/klitkavm/klitkavm/proto/gen/go/klitkavm/v1/klitkavmv1connect"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	sub := os.Args[1]
	switch sub {
	case "exec":
		execCommand(os.Args[2:])
	case "start":
		startCommand(os.Args[2:])
	case "stop":
		stopCommand(os.Args[2:])
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  klitkavm exec [--socket path | --tcp host:port] -- <command>")
	fmt.Println("  klitkavm start [--socket path | --tcp host:port]")
	fmt.Println("  klitkavm stop --id <vm-id> [--socket path | --tcp host:port]")
}

func execCommand(args []string) {
	fs := flag.NewFlagSet("exec", flag.ExitOnError)
	socket := fs.String("socket", socketDefault(), "unix socket path")
	tcp := fs.String("tcp", tcpDefault(), "tcp address host:port")
	fs.Parse(args)

	cmdArgs := fs.Args()
	if len(cmdArgs) == 0 {
		log.Fatal("exec requires a command")
	}

	client, baseURL := newClient(*socket, *tcp)
	ctx := context.Background()

	startResp, err := client.StartVM(ctx, connect.NewRequest(&klitkavmv1.StartVMRequest{}))
	if err != nil {
		log.Fatalf("start vm failed: %v", err)
	}
	vmID := startResp.Msg.GetVmId()

	command := cmdArgs[0]
	var commandArgs []string
	if len(cmdArgs) > 1 {
		commandArgs = cmdArgs[1:]
	}

	execResp, err := client.Exec(ctx, connect.NewRequest(&klitkavmv1.ExecRequest{
		VmId:    vmID,
		Command: command,
		Args:    commandArgs,
	}))
	if err != nil {
		log.Fatalf("exec failed: %v", err)
	}

	stopReq := connect.NewRequest(&klitkavmv1.StopVMRequest{VmId: vmID})
	_, stopErr := client.StopVM(ctx, stopReq)
	if stopErr != nil {
		log.Printf("stop vm failed: %v", stopErr)
	}

	exitCode := execResp.Msg.GetExitCode()
	if len(execResp.Msg.GetStdout()) > 0 {
		_, _ = os.Stdout.Write(execResp.Msg.GetStdout())
	}
	if len(execResp.Msg.GetStderr()) > 0 {
		_, _ = os.Stderr.Write(execResp.Msg.GetStderr())
	}

	if exitCode != 0 {
		log.Printf("command exited with code %d (via %s)", exitCode, baseURL)
		os.Exit(int(exitCode))
	}
}

func startCommand(args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	socket := fs.String("socket", socketDefault(), "unix socket path")
	tcp := fs.String("tcp", tcpDefault(), "tcp address host:port")
	fs.Parse(args)

	client, _ := newClient(*socket, *tcp)
	ctx := context.Background()

	resp, err := client.StartVM(ctx, connect.NewRequest(&klitkavmv1.StartVMRequest{}))
	if err != nil {
		log.Fatalf("start vm failed: %v", err)
	}
	fmt.Println(resp.Msg.GetVmId())
}

func stopCommand(args []string) {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	vmID := fs.String("id", "", "vm id")
	socket := fs.String("socket", socketDefault(), "unix socket path")
	tcp := fs.String("tcp", tcpDefault(), "tcp address host:port")
	fs.Parse(args)

	if *vmID == "" {
		log.Fatal("stop requires --id")
	}

	client, _ := newClient(*socket, *tcp)
	ctx := context.Background()
	_, err := client.StopVM(ctx, connect.NewRequest(&klitkavmv1.StopVMRequest{VmId: *vmID}))
	if err != nil {
		log.Fatalf("stop vm failed: %v", err)
	}
}

func newClient(socketPath, tcpAddr string) (klitkavmv1connect.DaemonServiceClient, string) {
	if tcpAddr != "" {
		baseURL := tcpBaseURL(tcpAddr)
		return klitkavmv1connect.NewDaemonServiceClient(http.DefaultClient, baseURL), baseURL
	}
	if socketPath == "" {
		log.Fatal("either --socket or --tcp must be provided")
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	client := &http.Client{Transport: transport}
	baseURL := "http://unix"
	return klitkavmv1connect.NewDaemonServiceClient(client, baseURL), baseURL
}

func tcpBaseURL(addr string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "http://" + addr
}

func socketDefault() string {
	if value := os.Getenv("KLITKAVM_SOCKET"); value != "" {
		return value
	}
	return daemon.DefaultSocketPath()
}

func tcpDefault() string {
	return os.Getenv("KLITKAVM_TCP")
}
