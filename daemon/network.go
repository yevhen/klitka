package daemon

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	klitkav1 "github.com/klitka/klitka/proto/gen/go/klitka/v1"
)

const guestProxyHost = "10.0.2.2"

const proxyDialTimeout = 10 * time.Second

const dnsLookupTimeout = 5 * time.Second

type networkManager struct {
	policy     networkPolicy
	proxy      *proxyServer
	env        []string
	caCertPath string
}

type networkPolicy struct {
	allowHosts        []string
	denyHosts         []string
	blockPrivateRange bool
}

func newNetworkManager(req *klitkav1.StartVMRequest) (*networkManager, error) {
	policy := buildNetworkPolicy(req.GetNetwork())
	secrets, secretEnv, err := buildSecrets(req.GetSecrets())
	if err != nil {
		return nil, err
	}

	if policy.isEmpty() && len(secrets) == 0 {
		return nil, nil
	}

	proxy, err := newProxyServer(policy, secrets)
	if err != nil {
		return nil, err
	}

	proxyURL := fmt.Sprintf("http://%s", joinHostPort(guestProxyHost, proxy.Port()))
	env := []string{
		"HTTP_PROXY=" + proxyURL,
		"HTTPS_PROXY=" + proxyURL,
		"http_proxy=" + proxyURL,
		"https_proxy=" + proxyURL,
	}
	env = append(env, secretEnv...)

	caCertPath := ""
	if proxy.mitm != nil {
		caCertPath = proxy.mitm.CertPath()
	}

	return &networkManager{
		policy:     policy,
		proxy:      proxy,
		env:        env,
		caCertPath: caCertPath,
	}, nil
}

func (manager *networkManager) Close() error {
	if manager == nil || manager.proxy == nil {
		return nil
	}
	return manager.proxy.Close()
}

func buildNetworkPolicy(cfg *klitkav1.NetworkPolicy) networkPolicy {
	if cfg == nil {
		return networkPolicy{}
	}
	allow := normalizeHosts(cfg.GetAllowHosts())
	deny := normalizeHosts(cfg.GetDenyHosts())
	return networkPolicy{
		allowHosts:        allow,
		denyHosts:         deny,
		blockPrivateRange: cfg.GetBlockPrivateRanges(),
	}
}

func normalizeHosts(hosts []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(hosts))
	for _, host := range hosts {
		host = strings.TrimSpace(strings.ToLower(host))
		host = strings.TrimSuffix(host, ".")
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		out = append(out, host)
	}
	return out
}

func (policy networkPolicy) isEmpty() bool {
	return len(policy.allowHosts) == 0 && len(policy.denyHosts) == 0 && !policy.blockPrivateRange
}

type proxyServer struct {
	listener  net.Listener
	server    *http.Server
	policy    networkPolicy
	transport *http.Transport
	secrets   []secretSpec
	mitm      *mitmCA
}

func newProxyServer(policy networkPolicy, secrets []secretSpec) (*proxyServer, error) {
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, err
	}

	transport, err := newProxyTransport()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}

	proxy := &proxyServer{
		listener:  listener,
		policy:    policy,
		transport: transport,
		secrets:   secrets,
	}

	if len(secrets) > 0 {
		ca, err := loadOrCreateCA()
		if err != nil {
			_ = listener.Close()
			return nil, err
		}
		proxy.mitm = ca
	}

	server := &http.Server{Handler: proxy}
	proxy.server = server

	go func() {
		_ = server.Serve(listener)
	}()

	return proxy, nil
}

func (proxy *proxyServer) Port() int {
	addr, ok := proxy.listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0
	}
	return addr.Port
}

func (proxy *proxyServer) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = proxy.server.Shutdown(ctx)
	err := proxy.listener.Close()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (proxy *proxyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		proxy.handleConnect(w, r)
		return
	}
	proxy.handleHTTP(w, r, "http")
}

func (proxy *proxyServer) handleHTTP(w http.ResponseWriter, r *http.Request, scheme string) {
	if r.URL == nil {
		http.Error(w, "missing url", http.StatusBadRequest)
		return
	}

	targetURL := r.URL
	if !targetURL.IsAbs() {
		host := r.Host
		if host == "" {
			http.Error(w, "missing host", http.StatusBadRequest)
			return
		}
		targetURL = &url.URL{
			Scheme:   scheme,
			Host:     host,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
		}
	}

	host := strings.ToLower(targetURL.Hostname())
	if err := proxy.policy.checkHost(host); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL.String(), r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	outReq.Header = cloneHeader(r.Header)
	stripProxyHeaders(outReq.Header)
	proxy.applySecrets(host, outReq.Header)
	outReq.Host = targetURL.Host

	resp, err := proxy.transport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (proxy *proxyServer) handleConnect(w http.ResponseWriter, r *http.Request) {
	host, port := splitHostPort(r.Host)
	if host == "" {
		http.Error(w, "missing host", http.StatusBadRequest)
		return
	}
	if port == "" {
		port = "443"
	}

	if err := proxy.policy.checkHost(host); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	if proxy.mitm != nil && shouldMitmHost(host, proxy.secrets) {
		proxy.handleMitm(w, r, host)
		return
	}

	proxy.handleConnectTunnel(w, r, host, port)
}

func (proxy *proxyServer) handleConnectTunnel(w http.ResponseWriter, r *http.Request, host, port string) {
	dialer := net.Dialer{Timeout: proxyDialTimeout}
	upstream, err := dialer.DialContext(r.Context(), "tcp", net.JoinHostPort(host, port))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}

	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))

	go proxyTunnel(upstream, clientConn)
	go proxyTunnel(clientConn, upstream)
}

func (proxy *proxyServer) handleMitm(w http.ResponseWriter, r *http.Request, host string) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		return
	}

	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))

	cert, err := proxy.mitm.certificateForHost(host)
	if err != nil {
		_ = clientConn.Close()
		return
	}

	tlsConn := tls.Server(clientConn, &tls.Config{
		Certificates: []tls.Certificate{*cert},
		NextProtos:   []string{"http/1.1"},
	})

	if err := tlsConn.Handshake(); err != nil {
		_ = tlsConn.Close()
		return
	}

	proxy.serveMitm(tlsConn, host)
}

func (proxy *proxyServer) serveMitm(conn net.Conn, host string) {
	defer func() {
		_ = conn.Close()
	}()

	reader := bufio.NewReader(conn)
	for {
		req, err := http.ReadRequest(reader)
		if err != nil {
			return
		}
		req.RequestURI = ""

		targetHost := req.Host
		if targetHost == "" {
			targetHost = host
		}

		req.URL.Scheme = "https"
		req.URL.Host = targetHost
		req.Host = targetHost

		hostname := strings.ToLower(req.URL.Hostname())
		if err := proxy.policy.checkHost(hostname); err != nil {
			writeProxyError(conn, http.StatusForbidden, err.Error())
			return
		}

		stripProxyHeaders(req.Header)
		proxy.applySecrets(hostname, req.Header)

		resp, err := proxy.transport.RoundTrip(req)
		if err != nil {
			writeProxyError(conn, http.StatusBadGateway, err.Error())
			return
		}
		err = resp.Write(conn)
		resp.Body.Close()
		if err != nil {
			return
		}
		if req.Close || resp.Close {
			return
		}
	}
}

func proxyTunnel(dst net.Conn, src net.Conn) {
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = src.Close()
}

func writeProxyError(conn net.Conn, status int, message string) {
	body := []byte(message)
	statusText := http.StatusText(status)
	if statusText == "" {
		statusText = "Error"
	}
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Length: %d\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\n%s", status, statusText, len(body), body)
}

func (policy networkPolicy) checkHost(host string) error {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return errors.New("empty host")
	}

	if policy.matchesAny(policy.denyHosts, host) {
		return fmt.Errorf("blocked host: %s", host)
	}

	if len(policy.allowHosts) > 0 && !policy.matchesAny(policy.allowHosts, host) {
		return fmt.Errorf("blocked host: %s", host)
	}

	if policy.blockPrivateRange {
		if private, err := hostResolvesToPrivate(host); err != nil {
			return fmt.Errorf("blocked host: %s", host)
		} else if private {
			return fmt.Errorf("blocked host: %s", host)
		}
	}

	return nil
}

func (policy networkPolicy) matchesAny(patterns []string, host string) bool {
	for _, pattern := range patterns {
		if matchHost(pattern, host) {
			return true
		}
	}
	return false
}

func matchHost(pattern string, host string) bool {
	if pattern == host {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := strings.TrimPrefix(pattern, "*")
		return strings.HasSuffix(host, suffix) && host != strings.TrimPrefix(suffix, ".")
	}
	return false
}

func hostResolvesToPrivate(host string) (bool, error) {
	if ip := net.ParseIP(host); ip != nil {
		return isPrivateIP(ip), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), dnsLookupTimeout)
	defer cancel()

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return true, err
	}
	for _, ip := range ips {
		if isPrivateIP(ip.IP) {
			return true, nil
		}
	}
	return false, nil
}

func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}
	return false
}

func (proxy *proxyServer) applySecrets(host string, header http.Header) {
	applySecrets(host, header, proxy.secrets)
}

func cloneHeader(header http.Header) http.Header {
	out := make(http.Header, len(header))
	copyHeader(out, header)
	return out
}

func copyHeader(dst http.Header, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func stripProxyHeaders(header http.Header) {
	header.Del("Proxy-Connection")
	header.Del("Proxy-Authenticate")
	header.Del("Proxy-Authorization")
	header.Del("Connection")
}

func splitHostPort(hostport string) (string, string) {
	if hostport == "" {
		return "", ""
	}
	if strings.Contains(hostport, ":") {
		host, port, err := net.SplitHostPort(hostport)
		if err == nil {
			return host, port
		}
	}
	return hostport, ""
}

func joinHostPort(host string, port int) string {
	return net.JoinHostPort(host, fmt.Sprintf("%d", port))
}

func newProxyTransport() (*http.Transport, error) {
	tlsConfig := &tls.Config{}
	if strings.TrimSpace(os.Getenv("KLITKA_PROXY_INSECURE")) != "" {
		tlsConfig.InsecureSkipVerify = true
	}
	return &http.Transport{
		Proxy:           nil,
		TLSClientConfig: tlsConfig,
	}, nil
}
