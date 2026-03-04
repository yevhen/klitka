//go:build !windows

package tests

import (
	"crypto/tls"
	"net"
	"net/http"
	"strings"

	"golang.org/x/net/http2"

	klitkav1connect "github.com/yevhen/klitka/proto/gen/go/klitka/v1/klitkav1connect"
)

func newTCPDaemonClient(addr string) klitkav1connect.DaemonServiceClient {
	baseURL := addr
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	transport := &http2.Transport{
		AllowHTTP: true,
		DialTLS: func(network, target string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).Dial(network, target)
		},
	}
	client := &http.Client{Transport: transport}
	return klitkav1connect.NewDaemonServiceClient(client, baseURL)
}
