package daemon

import (
	"bufio"
	"log"
	"net"
	"net/http"
	"os"
)

type debugListener struct {
	net.Listener
	label string
}

type debugConn struct {
	net.Conn
	label  string
	reader *bufio.Reader
	logged bool
}

func (c *debugConn) Read(p []byte) (int, error) {
	if !c.logged {
		c.logged = true
		peek, err := c.reader.Peek(24)
		if err != nil {
			log.Printf("%s conn %s peek error: %v (%q)", c.label, c.RemoteAddr().String(), err, string(peek))
		} else {
			log.Printf("%s conn %s first bytes: %q", c.label, c.RemoteAddr().String(), string(peek))
		}
	}
	return c.reader.Read(p)
}

func (l *debugListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &debugConn{
		Conn:   conn,
		label:  l.label,
		reader: bufio.NewReader(conn),
	}, nil
}

func wrapDebugListener(listener net.Listener, label string) net.Listener {
	if os.Getenv("KLITKAVM_DEBUG_CONN") == "1" {
		return &debugListener{Listener: listener, label: label}
	}
	return listener
}

func wrapDebugHandler(handler http.Handler) http.Handler {
	if os.Getenv("KLITKAVM_DEBUG_HTTP") != "1" {
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("http %s %s proto=%s", r.Method, r.URL.Path, r.Proto)
		handler.ServeHTTP(w, r)
	})
}
