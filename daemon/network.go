package daemon

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	klitkav1 "github.com/yevhen/klitka/proto/gen/go/klitka/v1"
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
	egressMode        klitkav1.EgressMode
	dnsMode           klitkav1.DNSMode
	trustedDNSServers []string
}

func newNetworkManager(req *klitkav1.StartVMRequest) (*networkManager, error) {
	policy, err := buildNetworkPolicy(req.GetNetwork())
	if err != nil {
		return nil, err
	}
	if err := validateNetworkPolicy(policy); err != nil {
		return nil, err
	}

	secrets, secretEnv, err := buildSecrets(req.GetSecrets())
	if err != nil {
		return nil, err
	}

	if !policy.requiresProxy() && len(secrets) == 0 {
		return nil, nil
	}

	if policy.egressMode == klitkav1.EgressMode_EGRESS_MODE_COMPAT {
		log.Printf("warning: network_enforcement=compat leaves direct guest egress path available")
	}

	proxy, err := newProxyServer(policy, secrets)
	if err != nil {
		return nil, err
	}

	proxyURL := fmt.Sprintf("http://%s", joinHostPort(guestProxyHost, proxy.Port()))
	env := []string{
		"HTTP_PROXY=" + proxyURL,
		"HTTPS_PROXY=" + proxyURL,
		"ALL_PROXY=" + proxyURL,
		"http_proxy=" + proxyURL,
		"https_proxy=" + proxyURL,
		"all_proxy=" + proxyURL,
	}
	env = append(env, secretEnv...)

	caCertPath := ""
	if proxy.mitm != nil {
		caCertPath = proxy.mitm.CertPath()
	}

	log.Printf(
		"network policy configured network_enforcement=%s dns_mode=%s allow_hosts=%d deny_hosts=%d trusted_dns_servers=%d block_private_ranges=%t",
		policy.egressModeName(),
		policy.dnsModeName(),
		len(policy.allowHosts),
		len(policy.denyHosts),
		len(policy.trustedDNSServers),
		policy.blockPrivateRange,
	)

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

func buildNetworkPolicy(cfg *klitkav1.NetworkPolicy) (networkPolicy, error) {
	policy := networkPolicy{
		egressMode: klitkav1.EgressMode_EGRESS_MODE_COMPAT,
		dnsMode:    klitkav1.DNSMode_DNS_MODE_OPEN,
	}
	if cfg == nil {
		return policy, nil
	}

	policy.allowHosts = normalizeHosts(cfg.GetAllowHosts())
	policy.denyHosts = normalizeHosts(cfg.GetDenyHosts())
	policy.blockPrivateRange = cfg.GetBlockPrivateRanges()
	if cfg.GetEgressMode() != klitkav1.EgressMode_EGRESS_MODE_UNSPECIFIED {
		policy.egressMode = cfg.GetEgressMode()
	}
	if cfg.GetDnsMode() != klitkav1.DNSMode_DNS_MODE_UNSPECIFIED {
		policy.dnsMode = cfg.GetDnsMode()
	}

	trustedDNSServers, err := normalizeDNSServers(cfg.GetTrustedDnsServers())
	if err != nil {
		return networkPolicy{}, err
	}
	policy.trustedDNSServers = trustedDNSServers
	return policy, nil
}

func validateNetworkPolicy(policy networkPolicy) error {
	switch policy.egressMode {
	case klitkav1.EgressMode_EGRESS_MODE_COMPAT, klitkav1.EgressMode_EGRESS_MODE_STRICT:
	default:
		return fmt.Errorf("unsupported egress mode: %v", policy.egressMode)
	}

	switch policy.dnsMode {
	case klitkav1.DNSMode_DNS_MODE_OPEN, klitkav1.DNSMode_DNS_MODE_TRUSTED, klitkav1.DNSMode_DNS_MODE_SYNTHETIC:
	default:
		return fmt.Errorf("unsupported dns mode: %v", policy.dnsMode)
	}

	if policy.dnsMode == klitkav1.DNSMode_DNS_MODE_TRUSTED && len(policy.trustedDNSServers) == 0 {
		return fmt.Errorf("trusted dns mode requires at least one trusted dns server")
	}
	if policy.dnsMode != klitkav1.DNSMode_DNS_MODE_TRUSTED && len(policy.trustedDNSServers) > 0 {
		return fmt.Errorf("trusted dns servers can only be used with dns mode trusted")
	}

	return nil
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

func normalizeDNSServers(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		normalized, err := normalizeDNSServer(value)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}

func normalizeDNSServer(value string) (string, error) {
	host := value
	port := "53"

	if parsedHost, parsedPort, err := net.SplitHostPort(value); err == nil {
		host = parsedHost
		port = parsedPort
	}

	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return "", fmt.Errorf("invalid trusted dns server %q: expected IP or [IPv6]:port", value)
	}

	portNum, err := strconv.Atoi(strings.TrimSpace(port))
	if err != nil || portNum <= 0 || portNum > 65535 {
		return "", fmt.Errorf("invalid trusted dns server port in %q", value)
	}

	return net.JoinHostPort(ip.String(), strconv.Itoa(portNum)), nil
}

func (policy networkPolicy) requiresProxy() bool {
	return len(policy.allowHosts) > 0 || len(policy.denyHosts) > 0 || policy.blockPrivateRange ||
		policy.egressMode == klitkav1.EgressMode_EGRESS_MODE_STRICT ||
		policy.dnsMode != klitkav1.DNSMode_DNS_MODE_OPEN ||
		len(policy.trustedDNSServers) > 0
}

func (policy networkPolicy) egressModeName() string {
	switch policy.egressMode {
	case klitkav1.EgressMode_EGRESS_MODE_STRICT:
		return "strict"
	case klitkav1.EgressMode_EGRESS_MODE_COMPAT:
		fallthrough
	default:
		return "compat"
	}
}

func (policy networkPolicy) dnsModeName() string {
	switch policy.dnsMode {
	case klitkav1.DNSMode_DNS_MODE_TRUSTED:
		return "trusted"
	case klitkav1.DNSMode_DNS_MODE_SYNTHETIC:
		return "synthetic"
	case klitkav1.DNSMode_DNS_MODE_OPEN:
		fallthrough
	default:
		return "open"
	}
}

type proxyServer struct {
	listener      net.Listener
	server        *http.Server
	policy        networkPolicy
	transport     *http.Transport
	secrets       []secretSpec
	mitm          *mitmCA
	resolver      *net.Resolver
	blockedEgress uint64
	dnsBlocked    uint64
	nextDNSServer uint32
}

func newProxyServer(policy networkPolicy, secrets []secretSpec) (*proxyServer, error) {
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, err
	}

	proxy := &proxyServer{
		listener: listener,
		policy:   policy,
		secrets:  secrets,
	}
	if policy.dnsMode == klitkav1.DNSMode_DNS_MODE_TRUSTED {
		proxy.resolver = proxy.newTrustedResolver(policy.trustedDNSServers)
	}

	transport, err := newProxyTransport(proxy.dialContext)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	proxy.transport = transport

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
		err = nil
	}
	log.Printf(
		"network proxy stopped network_enforcement=%s dns_mode=%s blocked_egress=%d dns_blocked=%d",
		proxy.policy.egressModeName(),
		proxy.policy.dnsModeName(),
		atomic.LoadUint64(&proxy.blockedEgress),
		atomic.LoadUint64(&proxy.dnsBlocked),
	)
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
	if err := proxy.enforceHost(r.Context(), host); err != nil {
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

	if err := proxy.enforceHost(r.Context(), host); err != nil {
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
	upstream, err := proxy.dialContext(r.Context(), "tcp", net.JoinHostPort(host, port))
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
		if err := proxy.enforceHost(req.Context(), hostname); err != nil {
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

func (proxy *proxyServer) enforceHost(ctx context.Context, host string) error {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if err := proxy.policy.checkHost(host); err != nil {
		proxy.markBlockedEgress()
		return err
	}

	if proxy.policy.blockPrivateRange {
		private, err := proxy.hostResolvesToPrivate(ctx, host)
		if err != nil || private {
			proxy.markBlockedEgress()
			return fmt.Errorf("blocked host: %s", host)
		}
	}

	return nil
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

func (proxy *proxyServer) hostResolvesToPrivate(ctx context.Context, host string) (bool, error) {
	ips, err := proxy.lookupIPs(ctx, host)
	if err != nil {
		return true, err
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return true, nil
		}
	}
	return false, nil
}

func (proxy *proxyServer) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		dialer := net.Dialer{Timeout: proxyDialTimeout}
		return dialer.DialContext(ctx, network, address)
	}

	ips, err := proxy.lookupIPs(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no ip for host %s", host)
	}

	dialer := net.Dialer{Timeout: proxyDialTimeout}
	var lastErr error
	for _, ip := range ips {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("unable to dial host %s", host)
	}
	return nil, lastErr
}

func (proxy *proxyServer) lookupIPs(ctx context.Context, host string) ([]net.IP, error) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return nil, fmt.Errorf("empty host")
	}
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}

	switch proxy.policy.dnsMode {
	case klitkav1.DNSMode_DNS_MODE_SYNTHETIC:
		if host == "localhost" {
			return []net.IP{net.IPv4(127, 0, 0, 1), net.ParseIP("::1")}, nil
		}
		proxy.markDNSBlocked()
		return nil, fmt.Errorf("blocked dns lookup for host: %s", host)
	case klitkav1.DNSMode_DNS_MODE_OPEN, klitkav1.DNSMode_DNS_MODE_TRUSTED:
		lookupCtx, cancel := context.WithTimeout(ctx, dnsLookupTimeout)
		defer cancel()

		resolver := proxy.resolver
		if resolver == nil {
			resolver = net.DefaultResolver
		}

		addrs, err := resolver.LookupIPAddr(lookupCtx, host)
		if err != nil {
			if proxy.policy.dnsMode == klitkav1.DNSMode_DNS_MODE_TRUSTED {
				proxy.markDNSBlocked()
			}
			return nil, err
		}

		ips := make([]net.IP, 0, len(addrs))
		for _, addr := range addrs {
			if addr.IP != nil {
				ips = append(ips, addr.IP)
			}
		}
		if len(ips) == 0 {
			if proxy.policy.dnsMode == klitkav1.DNSMode_DNS_MODE_TRUSTED {
				proxy.markDNSBlocked()
			}
			return nil, fmt.Errorf("no ip for host %s", host)
		}
		return ips, nil
	default:
		return nil, fmt.Errorf("unsupported dns mode: %v", proxy.policy.dnsMode)
	}
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

func (proxy *proxyServer) markBlockedEgress() {
	atomic.AddUint64(&proxy.blockedEgress, 1)
}

func (proxy *proxyServer) markDNSBlocked() {
	atomic.AddUint64(&proxy.dnsBlocked, 1)
}

func (proxy *proxyServer) newTrustedResolver(servers []string) *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			if len(servers) == 0 {
				return nil, fmt.Errorf("trusted dns server list is empty")
			}
			idx := int(atomic.AddUint32(&proxy.nextDNSServer, 1)-1) % len(servers)
			target := servers[idx]
			dialer := net.Dialer{Timeout: dnsLookupTimeout}
			return dialer.DialContext(ctx, network, target)
		},
	}
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

func newProxyTransport(dialContext func(ctx context.Context, network, addr string) (net.Conn, error)) (*http.Transport, error) {
	tlsConfig := &tls.Config{}
	if strings.TrimSpace(os.Getenv("KLITKA_PROXY_INSECURE")) != "" {
		tlsConfig.InsecureSkipVerify = true
	}
	return &http.Transport{
		Proxy:           nil,
		TLSClientConfig: tlsConfig,
		DialContext:     dialContext,
	}, nil
}
