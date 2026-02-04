package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/term"

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
	case "shell":
		shellCommand(os.Args[2:])
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
	fmt.Println("  klitkavm exec [--mount host:guest[:ro|rw]] [--socket path | --tcp host:port] -- <command>")
	fmt.Println("  klitkavm shell [--mount host:guest[:ro|rw]] [--socket path | --tcp host:port]")
	fmt.Println("  klitkavm start [--mount host:guest[:ro|rw]] [--socket path | --tcp host:port]")
	fmt.Println("  klitkavm stop --id <vm-id> [--socket path | --tcp host:port]")
}

func execCommand(args []string) {
	fs := flag.NewFlagSet("exec", flag.ExitOnError)
	mountArgs := mountFlag{}
	socket := fs.String("socket", socketDefault(), "unix socket path")
	tcp := fs.String("tcp", tcpDefault(), "tcp address host:port")
	fs.Var(&mountArgs, "mount", "mount in format host:guest[:ro|rw]")
	fs.Parse(args)

	cmdArgs := fs.Args()
	if len(cmdArgs) == 0 {
		log.Fatal("exec requires a command")
	}

	client, baseURL := newClient(*socket, *tcp)
	ctx := context.Background()

	mounts, err := parseMountFlags(mountArgs)
	if err != nil {
		log.Fatalf("invalid mount flag: %v", err)
	}

	startResp, err := client.StartVM(ctx, connect.NewRequest(&klitkavmv1.StartVMRequest{Mounts: mounts}))
	if err != nil {
		log.Fatalf("start vm failed: %v", err)
	}
	vmID := startResp.Msg.GetVmId()

	command := cmdArgs[0]
	var commandArgs []string
	if len(cmdArgs) > 1 {
		commandArgs = cmdArgs[1:]
	} else if strings.ContainsAny(command, " \t\n") {
		command = "sh"
		commandArgs = []string{"-c", cmdArgs[0]}
	}

	stopVM := func() {
		stopReq := connect.NewRequest(&klitkavmv1.StopVMRequest{VmId: vmID})
		if _, stopErr := client.StopVM(ctx, stopReq); stopErr != nil {
			log.Printf("stop vm failed: %v", stopErr)
		}
	}

	execResp, err := client.Exec(ctx, connect.NewRequest(&klitkavmv1.ExecRequest{
		VmId:    vmID,
		Command: command,
		Args:    commandArgs,
	}))
	if err != nil {
		stopVM()
		log.Fatalf("exec failed: %v", err)
	}

	stopVM()

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

func shellCommand(args []string) {
	fs := flag.NewFlagSet("shell", flag.ExitOnError)
	mountArgs := mountFlag{}
	socket := fs.String("socket", socketDefault(), "unix socket path")
	tcp := fs.String("tcp", tcpDefault(), "tcp address host:port")
	fs.Var(&mountArgs, "mount", "mount in format host:guest[:ro|rw]")
	fs.Parse(args)

	client, _ := newClient(*socket, *tcp)
	ctx := context.Background()

	mounts, err := parseMountFlags(mountArgs)
	if err != nil {
		log.Fatalf("invalid mount flag: %v", err)
	}

	startResp, err := client.StartVM(ctx, connect.NewRequest(&klitkavmv1.StartVMRequest{Mounts: mounts}))
	if err != nil {
		log.Fatalf("start vm failed: %v", err)
	}
	vmID := startResp.Msg.GetVmId()

	stream := client.ExecStream(ctx)
	sendMu := sync.Mutex{}
	send := func(msg *klitkavmv1.ExecStreamRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(msg)
	}

	startMsg := &klitkavmv1.ExecStreamRequest{
		Payload: &klitkavmv1.ExecStreamRequest_Start{
			Start: &klitkavmv1.ExecStart{
				VmId:    vmID,
				Command: "sh",
				Args:    []string{},
				Pty:     true,
			},
		},
	}
	if err := send(startMsg); err != nil {
		log.Fatalf("failed to start shell: %v", err)
	}

	fd := int(os.Stdin.Fd())
	isTTY := term.IsTerminal(fd)
	var restore func()
	if isTTY {
		state, err := term.MakeRaw(fd)
		if err == nil {
			restore = func() { _ = term.Restore(fd, state) }
			defer restore()
		}
	}

	if isTTY {
		sendResize(fd, send)
		watchResize(fd, send)
	}

	exitCodeCh := make(chan int32, 1)
	readErrCh := make(chan error, 1)

	go func() {
		reader := bufio.NewReader(os.Stdin)
		buf := make([]byte, 4096)
		for {
			n, err := reader.Read(buf)
			if n > 0 {
				if sendErr := send(&klitkavmv1.ExecStreamRequest{
					Payload: &klitkavmv1.ExecStreamRequest_Input{
						Input: &klitkavmv1.ExecInput{Data: buf[:n]},
					},
				}); sendErr != nil {
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					_ = send(&klitkavmv1.ExecStreamRequest{
						Payload: &klitkavmv1.ExecStreamRequest_Input{
							Input: &klitkavmv1.ExecInput{Eof: true},
						},
					})
					_ = stream.CloseRequest()
				}
				return
			}
		}
	}()

	go func() {
		for {
			resp, err := stream.Receive()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					select {
					case readErrCh <- err:
					default:
					}
				}
				return
			}
			if output := resp.GetOutput(); output != nil {
				if output.GetStream() == "stderr" {
					_, _ = os.Stderr.Write(output.GetData())
				} else {
					_, _ = os.Stdout.Write(output.GetData())
				}
			}
			if exit := resp.GetExit(); exit != nil {
				exitCodeCh <- exit.GetExitCode()
				return
			}
		}
	}()

	var exitCode int32
	select {
	case exitCode = <-exitCodeCh:
	case err := <-readErrCh:
		log.Printf("shell stream error: %v", err)
		exitCode = 1
	}

	_ = stream.CloseRequest()

	stopVM := connect.NewRequest(&klitkavmv1.StopVMRequest{VmId: vmID})
	if _, stopErr := client.StopVM(ctx, stopVM); stopErr != nil {
		log.Printf("stop vm failed: %v", stopErr)
	}

	if exitCode != 0 {
		os.Exit(int(exitCode))
	}
}

func sendResize(fd int, send func(*klitkavmv1.ExecStreamRequest) error) {
	cols, rows, err := term.GetSize(fd)
	if err != nil {
		return
	}
	_ = send(&klitkavmv1.ExecStreamRequest{
		Payload: &klitkavmv1.ExecStreamRequest_Resize{
			Resize: &klitkavmv1.PtyResize{Rows: uint32(rows), Cols: uint32(cols)},
		},
	})
}

func watchResize(fd int, send func(*klitkavmv1.ExecStreamRequest) error) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			sendResize(fd, send)
		}
	}()
}

func startCommand(args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	mountArgs := mountFlag{}
	socket := fs.String("socket", socketDefault(), "unix socket path")
	tcp := fs.String("tcp", tcpDefault(), "tcp address host:port")
	fs.Var(&mountArgs, "mount", "mount in format host:guest[:ro|rw]")
	fs.Parse(args)

	client, _ := newClient(*socket, *tcp)
	ctx := context.Background()

	mounts, err := parseMountFlags(mountArgs)
	if err != nil {
		log.Fatalf("invalid mount flag: %v", err)
	}

	resp, err := client.StartVM(ctx, connect.NewRequest(&klitkavmv1.StartVMRequest{Mounts: mounts}))
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

type mountFlag []string

func (m *mountFlag) String() string {
	return strings.Join(*m, ",")
}

func (m *mountFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func parseMountFlags(flags mountFlag) ([]*klitkavmv1.Mount, error) {
	if len(flags) == 0 {
		return nil, nil
	}

	mounts := make([]*klitkavmv1.Mount, 0, len(flags))
	for _, item := range flags {
		parts := strings.SplitN(item, ":", 3)
		if len(parts) < 2 {
			return nil, fmt.Errorf("invalid mount format: %q", item)
		}
		hostPath := strings.TrimSpace(parts[0])
		guestPath := strings.TrimSpace(parts[1])
		mode := klitkavmv1.MountMode_MOUNT_MODE_RO
		if len(parts) == 3 {
			switch strings.ToLower(strings.TrimSpace(parts[2])) {
			case "ro", "":
				mode = klitkavmv1.MountMode_MOUNT_MODE_RO
			case "rw":
				mode = klitkavmv1.MountMode_MOUNT_MODE_RW
			default:
				return nil, fmt.Errorf("invalid mount mode: %q", parts[2])
			}
		}
		if hostPath == "" || guestPath == "" {
			return nil, fmt.Errorf("invalid mount format: %q", item)
		}
		mounts = append(mounts, &klitkavmv1.Mount{
			HostPath:  hostPath,
			GuestPath: guestPath,
			Mode:      mode,
		})
	}

	return mounts, nil
}

func newClient(socketPath, tcpAddr string) (klitkavmv1connect.DaemonServiceClient, string) {
	if tcpAddr != "" {
		baseURL := tcpBaseURL(tcpAddr)
		client := &http.Client{Transport: http2Transport(func(network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).Dial(network, addr)
		})}
		return klitkavmv1connect.NewDaemonServiceClient(client, baseURL), baseURL
	}
	if socketPath == "" {
		log.Fatal("either --socket or --tcp must be provided")
	}

	transport := http2Transport(func(_ string, _ string) (net.Conn, error) {
		return (&net.Dialer{}).Dial("unix", socketPath)
	})
	client := &http.Client{Transport: transport}
	baseURL := "http://unix"
	return klitkavmv1connect.NewDaemonServiceClient(client, baseURL), baseURL
}

func http2Transport(dial func(network, addr string) (net.Conn, error)) *http2.Transport {
	return &http2.Transport{
		AllowHTTP: true,
		DialTLS: func(network, addr string, _ *tls.Config) (net.Conn, error) {
			return dial(network, addr)
		},
	}
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
