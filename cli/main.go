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

	"github.com/yevhen/klitka/daemon"
	klitkav1 "github.com/yevhen/klitka/proto/gen/go/klitka/v1"
	klitkav1connect "github.com/yevhen/klitka/proto/gen/go/klitka/v1/klitkav1connect"
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
	fmt.Println("  klitka exec [--mount host:guest[:ro|rw]] [--allow-host host] [--deny-host host] [--block-private=false] [--egress-mode compat|strict] [--dns-mode open|trusted|synthetic] [--trusted-dns host[:port]] [--secret NAME@host[,host...][:header][:format][=VALUE]] [--socket path | --tcp host:port] -- <command>")
	fmt.Println("  klitka shell [--mount host:guest[:ro|rw]] [--allow-host host] [--deny-host host] [--block-private=false] [--egress-mode compat|strict] [--dns-mode open|trusted|synthetic] [--trusted-dns host[:port]] [--secret NAME@host[,host...][:header][:format][=VALUE]] [--socket path | --tcp host:port]")
	fmt.Println("  klitka start [--mount host:guest[:ro|rw]] [--allow-host host] [--deny-host host] [--block-private=false] [--egress-mode compat|strict] [--dns-mode open|trusted|synthetic] [--trusted-dns host[:port]] [--secret NAME@host[,host...][:header][:format][=VALUE]] [--socket path | --tcp host:port]")
	fmt.Println("  klitka stop --id <vm-id> [--socket path | --tcp host:port]")
}

func execCommand(args []string) {
	fs := flag.NewFlagSet("exec", flag.ExitOnError)
	mountArgs := mountFlag{}
	allowHosts := stringSliceFlag{}
	denyHosts := stringSliceFlag{}
	trustedDNS := stringSliceFlag{}
	secretArgs := stringSliceFlag{}
	blockPrivate := fs.Bool("block-private", true, "block private IP ranges when using network allowlist")
	egressMode := fs.String("egress-mode", "compat", "network egress mode: compat|strict")
	dnsMode := fs.String("dns-mode", "open", "dns mode: open|trusted|synthetic")
	socket := fs.String("socket", socketDefault(), "unix socket path")
	tcp := fs.String("tcp", tcpDefault(), "tcp address host:port")
	fs.Var(&mountArgs, "mount", "mount in format host:guest[:ro|rw]")
	fs.Var(&allowHosts, "allow-host", "allow outbound HTTP(S) to host (repeatable)")
	fs.Var(&denyHosts, "deny-host", "deny outbound HTTP(S) to host (repeatable)")
	fs.Var(&trustedDNS, "trusted-dns", "trusted DNS resolver host[:port] for --dns-mode=trusted (repeatable)")
	fs.Var(&secretArgs, "secret", "secret in format NAME@host[,host...][:header][:format][=VALUE] (VALUE defaults to $NAME)")
	fs.Parse(args)

	cmdArgs := fs.Args()
	if len(cmdArgs) == 0 {
		log.Fatal("exec requires a command")
	}

	conn, err := newClient(*socket, *tcp)
	if err != nil {
		log.Fatalf("connect failed: %v", err)
	}
	client := conn.client
	baseURL := conn.baseURL
	ctx := context.Background()

	mounts, err := parseMountFlags(mountArgs)
	if err != nil {
		log.Fatalf("invalid mount flag: %v", err)
	}
	mounts, err = rewriteMountsForWsl(ctx, conn.wsl, mounts)
	if err != nil {
		log.Fatalf("invalid mount path: %v", err)
	}

	secrets, err := parseSecretFlags(secretArgs)
	if err != nil {
		log.Fatalf("invalid secret flag: %v", err)
	}

	networkPolicy, err := buildNetworkPolicy(allowHosts, denyHosts, trustedDNS, *blockPrivate, *egressMode, *dnsMode)
	if err != nil {
		log.Fatalf("invalid network flags: %v", err)
	}
	startResp, err := client.StartVM(ctx, connect.NewRequest(&klitkav1.StartVMRequest{
		Mounts:  mounts,
		Network: networkPolicy,
		Secrets: secrets,
	}))
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
		stopReq := connect.NewRequest(&klitkav1.StopVMRequest{VmId: vmID})
		if _, stopErr := client.StopVM(ctx, stopReq); stopErr != nil {
			log.Printf("stop vm failed: %v", stopErr)
		}
	}

	execResp, err := client.Exec(ctx, connect.NewRequest(&klitkav1.ExecRequest{
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
	allowHosts := stringSliceFlag{}
	denyHosts := stringSliceFlag{}
	trustedDNS := stringSliceFlag{}
	secretArgs := stringSliceFlag{}
	blockPrivate := fs.Bool("block-private", true, "block private IP ranges when using network allowlist")
	egressMode := fs.String("egress-mode", "compat", "network egress mode: compat|strict")
	dnsMode := fs.String("dns-mode", "open", "dns mode: open|trusted|synthetic")
	socket := fs.String("socket", socketDefault(), "unix socket path")
	tcp := fs.String("tcp", tcpDefault(), "tcp address host:port")
	fs.Var(&mountArgs, "mount", "mount in format host:guest[:ro|rw]")
	fs.Var(&allowHosts, "allow-host", "allow outbound HTTP(S) to host (repeatable)")
	fs.Var(&denyHosts, "deny-host", "deny outbound HTTP(S) to host (repeatable)")
	fs.Var(&trustedDNS, "trusted-dns", "trusted DNS resolver host[:port] for --dns-mode=trusted (repeatable)")
	fs.Var(&secretArgs, "secret", "secret in format NAME@host[,host...][:header][:format][=VALUE] (VALUE defaults to $NAME)")
	fs.Parse(args)

	conn, err := newClient(*socket, *tcp)
	if err != nil {
		log.Fatalf("connect failed: %v", err)
	}
	client := conn.client
	ctx := context.Background()

	mounts, err := parseMountFlags(mountArgs)
	if err != nil {
		log.Fatalf("invalid mount flag: %v", err)
	}
	mounts, err = rewriteMountsForWsl(ctx, conn.wsl, mounts)
	if err != nil {
		log.Fatalf("invalid mount path: %v", err)
	}

	secrets, err := parseSecretFlags(secretArgs)
	if err != nil {
		log.Fatalf("invalid secret flag: %v", err)
	}

	networkPolicy, err := buildNetworkPolicy(allowHosts, denyHosts, trustedDNS, *blockPrivate, *egressMode, *dnsMode)
	if err != nil {
		log.Fatalf("invalid network flags: %v", err)
	}
	startResp, err := client.StartVM(ctx, connect.NewRequest(&klitkav1.StartVMRequest{
		Mounts:  mounts,
		Network: networkPolicy,
		Secrets: secrets,
	}))
	if err != nil {
		log.Fatalf("start vm failed: %v", err)
	}
	vmID := startResp.Msg.GetVmId()

	stream := client.ExecStream(ctx)
	sendMu := sync.Mutex{}
	send := func(msg *klitkav1.ExecStreamRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(msg)
	}

	startMsg := &klitkav1.ExecStreamRequest{
		Payload: &klitkav1.ExecStreamRequest_Start{
			Start: &klitkav1.ExecStart{
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
				if sendErr := send(&klitkav1.ExecStreamRequest{
					Payload: &klitkav1.ExecStreamRequest_Input{
						Input: &klitkav1.ExecInput{Data: buf[:n]},
					},
				}); sendErr != nil {
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					_ = send(&klitkav1.ExecStreamRequest{
						Payload: &klitkav1.ExecStreamRequest_Input{
							Input: &klitkav1.ExecInput{Eof: true},
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

	stopVM := connect.NewRequest(&klitkav1.StopVMRequest{VmId: vmID})
	if _, stopErr := client.StopVM(ctx, stopVM); stopErr != nil {
		log.Printf("stop vm failed: %v", stopErr)
	}

	if exitCode != 0 {
		os.Exit(int(exitCode))
	}
}

func sendResize(fd int, send func(*klitkav1.ExecStreamRequest) error) {
	cols, rows, err := term.GetSize(fd)
	if err != nil {
		return
	}
	_ = send(&klitkav1.ExecStreamRequest{
		Payload: &klitkav1.ExecStreamRequest_Resize{
			Resize: &klitkav1.PtyResize{Rows: uint32(rows), Cols: uint32(cols)},
		},
	})
}

func watchResize(fd int, send func(*klitkav1.ExecStreamRequest) error) {
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
	allowHosts := stringSliceFlag{}
	denyHosts := stringSliceFlag{}
	trustedDNS := stringSliceFlag{}
	secretArgs := stringSliceFlag{}
	blockPrivate := fs.Bool("block-private", true, "block private IP ranges when using network allowlist")
	egressMode := fs.String("egress-mode", "compat", "network egress mode: compat|strict")
	dnsMode := fs.String("dns-mode", "open", "dns mode: open|trusted|synthetic")
	socket := fs.String("socket", socketDefault(), "unix socket path")
	tcp := fs.String("tcp", tcpDefault(), "tcp address host:port")
	fs.Var(&mountArgs, "mount", "mount in format host:guest[:ro|rw]")
	fs.Var(&allowHosts, "allow-host", "allow outbound HTTP(S) to host (repeatable)")
	fs.Var(&denyHosts, "deny-host", "deny outbound HTTP(S) to host (repeatable)")
	fs.Var(&trustedDNS, "trusted-dns", "trusted DNS resolver host[:port] for --dns-mode=trusted (repeatable)")
	fs.Var(&secretArgs, "secret", "secret in format NAME@host[,host...][:header][:format][=VALUE] (VALUE defaults to $NAME)")
	fs.Parse(args)

	conn, err := newClient(*socket, *tcp)
	if err != nil {
		log.Fatalf("connect failed: %v", err)
	}
	client := conn.client
	ctx := context.Background()

	mounts, err := parseMountFlags(mountArgs)
	if err != nil {
		log.Fatalf("invalid mount flag: %v", err)
	}
	mounts, err = rewriteMountsForWsl(ctx, conn.wsl, mounts)
	if err != nil {
		log.Fatalf("invalid mount path: %v", err)
	}

	secrets, err := parseSecretFlags(secretArgs)
	if err != nil {
		log.Fatalf("invalid secret flag: %v", err)
	}

	networkPolicy, err := buildNetworkPolicy(allowHosts, denyHosts, trustedDNS, *blockPrivate, *egressMode, *dnsMode)
	if err != nil {
		log.Fatalf("invalid network flags: %v", err)
	}
	resp, err := client.StartVM(ctx, connect.NewRequest(&klitkav1.StartVMRequest{
		Mounts:  mounts,
		Network: networkPolicy,
		Secrets: secrets,
	}))
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

	conn, err := newClient(*socket, *tcp)
	if err != nil {
		log.Fatalf("connect failed: %v", err)
	}
	client := conn.client
	ctx := context.Background()
	_, err = client.StopVM(ctx, connect.NewRequest(&klitkav1.StopVMRequest{VmId: *vmID}))
	if err != nil {
		log.Fatalf("stop vm failed: %v", err)
	}
}

type mountFlag []string

type stringSliceFlag []string

func (m *mountFlag) String() string {
	return strings.Join(*m, ",")
}

func (m *mountFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func (s *stringSliceFlag) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSliceFlag) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func parseMountFlags(flags mountFlag) ([]*klitkav1.Mount, error) {
	if len(flags) == 0 {
		return nil, nil
	}

	mounts := make([]*klitkav1.Mount, 0, len(flags))
	for _, item := range flags {
		hostPath, guestPath, modeRaw, err := splitMountSpec(item)
		if err != nil {
			return nil, err
		}
		mode := klitkav1.MountMode_MOUNT_MODE_RO
		switch strings.ToLower(strings.TrimSpace(modeRaw)) {
		case "", "ro":
			mode = klitkav1.MountMode_MOUNT_MODE_RO
		case "rw":
			mode = klitkav1.MountMode_MOUNT_MODE_RW
		default:
			return nil, fmt.Errorf("invalid mount mode: %q", modeRaw)
		}
		if hostPath == "" || guestPath == "" {
			return nil, fmt.Errorf("invalid mount format: %q", item)
		}
		mounts = append(mounts, &klitkav1.Mount{
			HostPath:  hostPath,
			GuestPath: guestPath,
			Mode:      mode,
		})
	}

	return mounts, nil
}

func splitMountSpec(raw string) (string, string, string, error) {
	item := strings.TrimSpace(raw)
	if item == "" {
		return "", "", "", fmt.Errorf("invalid mount format: %q", raw)
	}

	mode := ""
	withoutMode := item
	lastColon := strings.LastIndex(item, ":")
	if lastColon >= 0 {
		candidate := strings.TrimSpace(item[lastColon+1:])
		if candidate == "ro" || candidate == "rw" {
			mode = candidate
			withoutMode = strings.TrimSpace(item[:lastColon])
		}
	}

	delim := strings.LastIndex(withoutMode, ":")
	if delim < 0 {
		return "", "", "", fmt.Errorf("invalid mount format: %q", raw)
	}

	hostPath := strings.TrimSpace(withoutMode[:delim])
	guestPath := strings.TrimSpace(withoutMode[delim+1:])
	return hostPath, guestPath, mode, nil
}

func parseSecretFlags(flags stringSliceFlag) ([]*klitkav1.Secret, error) {
	if len(flags) == 0 {
		return nil, nil
	}

	secrets := make([]*klitkav1.Secret, 0, len(flags))
	for _, item := range flags {
		secret, err := parseSecretFlag(item)
		if err != nil {
			return nil, err
		}
		secrets = append(secrets, secret)
	}
	return secrets, nil
}

func parseSecretFlag(item string) (*klitkav1.Secret, error) {
	item = strings.TrimSpace(item)
	if item == "" {
		return nil, fmt.Errorf("invalid secret format")
	}

	value := ""
	left := item
	if idx := strings.Index(item, "="); idx >= 0 {
		left = item[:idx]
		value = item[idx+1:]
	}

	parts := strings.Split(left, ":")
	if len(parts) > 3 {
		return nil, fmt.Errorf("invalid secret format: %q", item)
	}

	nameHosts := strings.TrimSpace(parts[0])
	header := ""
	format := ""
	if len(parts) > 1 {
		header = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		format = strings.TrimSpace(parts[2])
	}

	at := strings.Index(nameHosts, "@")
	if at < 0 {
		return nil, fmt.Errorf("invalid secret format: %q", item)
	}
	name := strings.TrimSpace(nameHosts[:at])
	hostsPart := strings.TrimSpace(nameHosts[at+1:])
	if name == "" || hostsPart == "" {
		return nil, fmt.Errorf("invalid secret format: %q", item)
	}

	hostsRaw := strings.Split(hostsPart, ",")
	hosts := make([]string, 0, len(hostsRaw))
	for _, host := range hostsRaw {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		hosts = append(hosts, host)
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("invalid secret format: %q", item)
	}

	if value == "" {
		value = os.Getenv(name)
	}
	if value == "" {
		return nil, fmt.Errorf("missing secret value for %s", name)
	}

	formatEnum, err := parseSecretFormat(format)
	if err != nil {
		return nil, err
	}

	secret := &klitkav1.Secret{
		Name:  name,
		Hosts: hosts,
		Value: value,
	}
	if header != "" {
		secret.Header = header
	}
	if formatEnum != klitkav1.SecretFormat_SECRET_FORMAT_UNSPECIFIED {
		secret.Format = formatEnum
	}

	return secret, nil
}

func parseSecretFormat(raw string) (klitkav1.SecretFormat, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return klitkav1.SecretFormat_SECRET_FORMAT_UNSPECIFIED, nil
	case "bearer":
		return klitkav1.SecretFormat_SECRET_FORMAT_BEARER, nil
	case "raw":
		return klitkav1.SecretFormat_SECRET_FORMAT_RAW, nil
	default:
		return klitkav1.SecretFormat_SECRET_FORMAT_UNSPECIFIED, fmt.Errorf("invalid secret format: %q", raw)
	}
}

func buildNetworkPolicy(
	allowHosts stringSliceFlag,
	denyHosts stringSliceFlag,
	trustedDNS stringSliceFlag,
	blockPrivate bool,
	egressModeRaw string,
	dnsModeRaw string,
) (*klitkav1.NetworkPolicy, error) {
	egressMode, err := parseEgressMode(egressModeRaw)
	if err != nil {
		return nil, err
	}
	dnsMode, err := parseDNSMode(dnsModeRaw)
	if err != nil {
		return nil, err
	}

	allow := nonEmptyStrings(allowHosts)
	deny := nonEmptyStrings(denyHosts)
	trusted := nonEmptyStrings(trustedDNS)

	if dnsMode == klitkav1.DNSMode_DNS_MODE_TRUSTED && len(trusted) == 0 {
		return nil, fmt.Errorf("trusted dns mode requires at least one --trusted-dns")
	}
	if dnsMode != klitkav1.DNSMode_DNS_MODE_TRUSTED && len(trusted) > 0 {
		return nil, fmt.Errorf("--trusted-dns is only supported with --dns-mode=trusted")
	}

	hasPolicy := len(allow) > 0 || len(deny) > 0 || len(trusted) > 0 || !blockPrivate ||
		egressMode != klitkav1.EgressMode_EGRESS_MODE_COMPAT || dnsMode != klitkav1.DNSMode_DNS_MODE_OPEN
	if !hasPolicy {
		return nil, nil
	}

	return &klitkav1.NetworkPolicy{
		AllowHosts:         allow,
		DenyHosts:          deny,
		BlockPrivateRanges: blockPrivate,
		EgressMode:         egressMode,
		DnsMode:            dnsMode,
		TrustedDnsServers:  trusted,
	}, nil
}

func parseEgressMode(raw string) (klitkav1.EgressMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "compat":
		return klitkav1.EgressMode_EGRESS_MODE_COMPAT, nil
	case "strict":
		return klitkav1.EgressMode_EGRESS_MODE_STRICT, nil
	default:
		return klitkav1.EgressMode_EGRESS_MODE_UNSPECIFIED, fmt.Errorf("invalid egress mode: %q", raw)
	}
}

func parseDNSMode(raw string) (klitkav1.DNSMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "open":
		return klitkav1.DNSMode_DNS_MODE_OPEN, nil
	case "trusted":
		return klitkav1.DNSMode_DNS_MODE_TRUSTED, nil
	case "synthetic":
		return klitkav1.DNSMode_DNS_MODE_SYNTHETIC, nil
	default:
		return klitkav1.DNSMode_DNS_MODE_UNSPECIFIED, fmt.Errorf("invalid dns mode: %q", raw)
	}
}

func nonEmptyStrings(values stringSliceFlag) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type daemonConnection struct {
	client  klitkav1connect.DaemonServiceClient
	baseURL string
	wsl     *wslContext
}

func newClient(socketPath, tcpAddr string) (daemonConnection, error) {
	if isWindows() {
		if socketPath != "" {
			return daemonConnection{}, fmt.Errorf("unix sockets are not supported on Windows; use --tcp")
		}
		if tcpAddr == "" {
			tcpAddr = defaultTcpAddr()
		}
		ctx := context.Background()
		wsl, err := ensureWslDaemon(ctx, tcpAddr)
		if err != nil {
			return daemonConnection{}, err
		}
		client, baseURL := newTcpClient(tcpAddr)
		return daemonConnection{client: client, baseURL: baseURL, wsl: wsl}, nil
	}

	if tcpAddr != "" {
		client, baseURL := newTcpClient(tcpAddr)
		return daemonConnection{client: client, baseURL: baseURL}, nil
	}
	if socketPath == "" {
		return daemonConnection{}, fmt.Errorf("either --socket or --tcp must be provided")
	}

	transport := http2Transport(func(_ string, _ string) (net.Conn, error) {
		return (&net.Dialer{}).Dial("unix", socketPath)
	})
	client := &http.Client{Transport: transport}
	baseURL := "http://unix"
	return daemonConnection{client: klitkav1connect.NewDaemonServiceClient(client, baseURL), baseURL: baseURL}, nil
}

func newTcpClient(tcpAddr string) (klitkav1connect.DaemonServiceClient, string) {
	baseURL := tcpBaseURL(tcpAddr)
	client := &http.Client{Transport: http2Transport(func(network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).Dial(network, addr)
	})}
	return klitkav1connect.NewDaemonServiceClient(client, baseURL), baseURL
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
	if value := os.Getenv("KLITKA_SOCKET"); value != "" {
		return value
	}
	return daemon.DefaultSocketPath()
}

func tcpDefault() string {
	if value := os.Getenv("KLITKA_TCP"); value != "" {
		return value
	}
	return defaultTcpAddr()
}
