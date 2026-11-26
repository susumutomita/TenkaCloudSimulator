package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/azure/entra"
)

const entraFixtureCommand = "entra-verification-fixture"

func runEntraVerificationFixture(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet(entraFixtureCommand, flag.ContinueOnError)
	flags.SetOutput(output)
	listen := flags.String("listen", "127.0.0.1:0", "fixture-only numeric IPv4 loopback; 0 picks an available port")
	nonce := flags.String("test-id-token-nonce", "", "emit one 60-second synthetic ID-token fixture for this nonce")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected verification fixture arguments")
	}
	address, err := netip.ParseAddrPort(*listen)
	if err != nil || !entra.ValidListen(address) {
		return errors.New("Entra verification fixture requires numeric IPv4 loopback (127.0.0.1)")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", address.String())
	if err != nil {
		return err
	}
	defer listener.Close()
	f, err := entra.NewVerificationFixture(listener.Addr().(*net.TCPAddr).AddrPort())
	if err != nil {
		return err
	}
	meta := f.Metadata()
	ready := struct {
		entra.VerificationMetadata
		MetadataURL string `json:"verification_metadata_url"`
		TestIDToken string `json:"test_id_token,omitempty"`
	}{VerificationMetadata: meta, MetadataURL: "http://" + listener.Addr().String() + entra.MetadataPath}
	emit := false
	flags.Visit(func(f *flag.Flag) { emit = emit || f.Name == "test-id-token-nonce" })
	if emit {
		ready.TestIDToken, err = f.TestIDToken(*nonce)
		if err != nil {
			return err
		}
	}
	if err := json.NewEncoder(output).Encode(ready); err != nil {
		return err
	}
	srv := &http.Server{
		Handler: f, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 5 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8192,
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			return errors.Join(err, srv.Close())
		}
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
