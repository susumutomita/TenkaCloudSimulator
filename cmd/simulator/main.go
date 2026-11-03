package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	listen := flag.String("listen", "127.0.0.1:4566", "HTTP listen address (console: /_simulator/console/)")
	statePath := flag.String("state", ":memory:", "SQLite state path; default is ephemeral memory")
	remote := flag.Bool("allow-remote", false, "Allow binding beyond loopback; use only in an isolated trusted network")
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if !*remote && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("non-loopback binding requires --allow-remote; the simulator has no authentication")
	}
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	handler, closeState, err := server.New(*statePath)
	if err != nil {
		_ = l.Close()
		return err
	}
	defer closeState()
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	fmt.Printf("TenkaCloud Simulator · independent, in-memory simulation\nConsole http://%s/_simulator/console/\nS3 endpoint http://%s\n", l.Addr(), l.Addr())
	select {
	case err := <-done:
		if err != http.ErrServerClosed {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}
