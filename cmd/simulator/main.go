package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
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
	tokenFile := flags.String("token-file", "simulator-token", "Private local operator access-token file (created with mode 0600)")
	unsafe := flags.Bool("unsafe-no-auth", false, "Development-only: disable access tokens on numeric loopback")
	showToken := flags.Bool("show-token", false, "Display the operator token only on an interactive terminal with CI unset")
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
		return fmt.Errorf("non-loopback binding requires --allow-remote; use a trusted authenticated host proxy")
	}
	if *unsafe && *showToken {
		return fmt.Errorf("--show-token requires authentication; it cannot be combined with --unsafe-no-auth")
	}
	if *unsafe && *remote {
		return fmt.Errorf("--unsafe-no-auth cannot be combined with --allow-remote")
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
	if !*unsafe {
		token, err := operatorToken(*tokenFile)
		if err != nil {
			_ = l.Close()
			return err
		}
		handler, err = server.NewAccess(handler, token, *remote)
		if err != nil {
			_ = l.Close()
			return err
		}
		fmt.Fprintf(output, "Operator token file: %s (never share with participants)\n", *tokenFile)
		if *showToken && interactiveTokenOutput(output) {
			fmt.Fprintf(output, "Operator access token: %s\n", token)
		} else {
			fmt.Fprintln(output, "Sign in using the operator token saved in that file.")
		}
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	fmt.Fprintf(output, "TenkaCloud Simulator · independent local simulation\nConsole http://%s/_simulator/console/\nAWS simulation endpoint http://%s\n", l.Addr(), l.Addr())
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

func operatorToken(path string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		data := make([]byte, 32)
		if _, err = rand.Read(data); err != nil {
			return "", err
		}
		token := hex.EncodeToString(data)
		file, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return "", e
		}
		_, e = file.WriteString(token + "\n")
		closeErr := file.Close()
		if e != nil {
			return "", e
		}
		return token, closeErr
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 512 || info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("token file must be a regular private file (0600) of at most 512 bytes")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if len(token) < 32 || len(token) > 256 {
		return "", fmt.Errorf("invalid operator token file length")
	}
	return token, nil
}
