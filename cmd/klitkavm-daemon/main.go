package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/klitkavm/klitkavm/daemon"
	klitkavmv1connect "github.com/klitkavm/klitkavm/proto/gen/go/klitkavm/v1/klitkavmv1connect"
)

func main() {
	socketPath := flag.String("socket", "", "unix socket path (default: platform-specific)")
	tcpAddr := flag.String("tcp", "", "tcp listen address (optional)")
	flag.Parse()

	service := daemon.NewService()
	path, handler := klitkavmv1connect.NewDaemonServiceHandler(service)
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
