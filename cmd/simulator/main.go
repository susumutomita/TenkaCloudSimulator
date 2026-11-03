package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/server"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil && err != flag.ErrHelp {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("simulator", flag.ContinueOnError)
	flags.SetOutput(output)
	listen := flags.String("listen", "127.0.0.1:4566", "HTTP listen address (console: /_simulator/console/)")
	statePath := flags.String("state", ":memory:", "SQLite state path; default is ephemeral memory")
	remote := flags.Bool("allow-remote", false, "Allow binding beyond loopback; use only in an isolated trusted network")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
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
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	fmt.Fprintf(output, "TenkaCloud Simulator · independent local simulation\nConsole http://%s/_simulator/console/\nS3 endpoint http://%s\n", l.Addr(), l.Addr())
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
