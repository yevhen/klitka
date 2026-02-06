package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/klitka/klitka/daemon"
	klitkav1connect "github.com/klitka/klitka/proto/gen/go/klitka/v1/klitkav1connect"
)

func main() {
	socketPath := flag.String("socket", "", "unix socket path (default: platform-specific)")
	tcpAddr := flag.String("tcp", "", "tcp listen address (optional)")
	flag.Parse()

	service := daemon.NewService()
	path, handler := klitkav1connect.NewDaemonServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)

	server, err := daemon.StartServer(mux, daemon.ServerOptions{
		SocketPath: *socketPath,
		TCPAddr:    *tcpAddr,
	})
	if err != nil {
		log.Fatalf("failed to start daemon: %v", err)
	}

	for _, listener := range server.Listeners {
		log.Printf("daemon listening on %s", listener.Addr())
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("shutting down daemon")
	_ = server.HTTP.Close()
}
