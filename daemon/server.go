package daemon

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

type ServerOptions struct {
	SocketPath string
	TCPAddr    string
}

type Server struct {
	HTTP      *http.Server
	Listeners []net.Listener
}

func StartServer(handler http.Handler, options ServerOptions) (*Server, error) {
	listeners := []net.Listener{}

	if options.TCPAddr != "" {
		listener, err := net.Listen("tcp", options.TCPAddr)
		if err != nil {
			return nil, fmt.Errorf("listen tcp %s: %w", options.TCPAddr, err)
		}
		listeners = append(listeners, listener)
	}

	socketPath := options.SocketPath
	if socketPath == "" && options.TCPAddr == "" {
		socketPath = DefaultSocketPath()
	}
	if socketPath != "" {
		if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
			return nil, fmt.Errorf("create socket dir: %w", err)
		}
		_ = os.Remove(socketPath)
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			return nil, fmt.Errorf("listen unix %s: %w", socketPath, err)
		}
		listeners = append(listeners, listener)
	}

	if len(listeners) == 0 {
		return nil, fmt.Errorf("no listeners configured")
	}

	server := &http.Server{Handler: handler}
	for _, listener := range listeners {
		go func(l net.Listener) {
			if err := server.Serve(l); err != nil && err != http.ErrServerClosed {
				log.Printf("server error on %s: %v", l.Addr(), err)
			}
		}(listener)
	}

	return &Server{HTTP: server, Listeners: listeners}, nil
}

func DefaultSocketPath() string {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support", "klitkavm", "daemon.sock")
	}
	if runtime.GOOS == "linux" {
		return "/var/run/klitkavm.sock"
	}
	return ""
}
