package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/cognitoidp"
	"github.com/susumutomita/TenkaCloudSimulator/internal/azure/entraidp"
	"github.com/susumutomita/TenkaCloudSimulator/internal/server"
)

const (
	localSSOCommand = "local-sso"
	localSSOLimit   = 64 << 10
	localSSORegion  = "us-east-1"
	localSSOService = "cognito-idp"
)

var localSSOPoolID = regexp.MustCompile(`^us-east-1_[A-Za-z0-9]+$`)
var localSSOClientID = regexp.MustCompile(`^[0-9a-f]{24}$`)

var (
	errLocalSSOCloseListener = errors.New("could not close a local SSO listener")
	errLocalSSOCloseState    = errors.New("could not close local SSO state")
	errLocalSSORemoveState   = errors.New("could not remove private local SSO state")
)

// Cleanup errors are reported even if startup already failed, without
// disclosing temporary paths, transport internals or credentials.
func localSSOJoinCleanup(primary, actual, public error) error {
	if actual == nil {
		return primary
	}
	return errors.Join(primary, public)
}

type localSSOListener struct {
	net.Listener
	server        *http.Server
	done          chan error
	serveFinished bool
	mu            sync.Mutex
	closing       bool
	active        sync.WaitGroup
}

func (l *localSSOListener) origin() string { return "https://" + l.Addr().String() }
func (l *localSSOListener) serve(handler http.Handler, leaf tls.Certificate) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{leaf}}
	admit := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		if l.closing {
			l.mu.Unlock()
			http.Error(w, "Local SSO stopping.", http.StatusServiceUnavailable)
			return
		}
		l.active.Add(1)
		l.mu.Unlock()
		defer l.active.Done()
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
	l.server = &http.Server{Handler: admit, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 6 * time.Second,
		WriteTimeout: 8 * time.Second, IdleTimeout: 6 * time.Second, MaxHeaderBytes: 16 << 10, TLSConfig: cfg,
		ErrorLog: log.New(io.Discard, "", 0)}
	l.done = make(chan error, 1)
	go func() { l.done <- l.server.Serve(tls.NewListener(l.Listener, cfg)) }()
}
func (l *localSSOListener) close() error {
	l.mu.Lock()
	l.closing = true
	l.mu.Unlock()
	var closeErr error
	if l.server != nil {
		closeErr = l.server.Close()
		if !l.serveFinished {
			<-l.done
			l.serveFinished = true
		}
		l.active.Wait()
		l.server = nil
	} else {
		closeErr = l.Listener.Close()
	}
	if errors.Is(closeErr, net.ErrClosed) {
		return nil
	}
	return closeErr
}

func localSSOEntropy() string {
	var b [32]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func localSSOCertSerial() (*big.Int, error) {
	n, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e != nil {
		return nil, e
	}
	return n.Add(n, big.NewInt(1)), nil
}

func localSSOTLS(n int, now time.Time) ([]byte, *x509.CertPool, []tls.Certificate, error) {
	makeKey := func() (*ecdsa.PrivateKey, error) { return ecdsa.GenerateKey(elliptic.P256(), rand.Reader) }
	key, e := makeKey()
	if e != nil {
		return nil, nil, nil, e
	}
	serial, e := localSSOCertSerial()
	if e != nil {
		return nil, nil, nil, e
	}
	ca := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "TenkaCloudSimulator ephemeral local SSO"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(15 * time.Minute), IsCA: true, BasicConstraintsValid: true,
		MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, e := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if e != nil {
		return nil, nil, nil, e
	}
	ca, e = x509.ParseCertificate(caDER)
	if e != nil {
		return nil, nil, nil, e
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	leaves := make([]tls.Certificate, 0, n)
	for range n {
		leafKey, e := makeKey()
		if e != nil {
			return nil, nil, nil, e
		}
		serial, e := localSSOCertSerial()
		if e != nil {
			return nil, nil, nil, e
		}
		t := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "TenkaCloudSimulator loopback"},
			NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, e := x509.CreateCertificate(rand.Reader, t, ca, &leafKey.PublicKey, key)
		if e != nil {
			return nil, nil, nil, e
		}
		leaves = append(leaves, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: leafKey})
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), roots, leaves, nil
}

func localSSOHTTP(roots *x509.CertPool, listeners []*localSSOListener) (*http.Client, func()) {
	allowed := map[string]bool{}
	for _, l := range listeners {
		allowed[l.Addr().String()] = true
	}
	dialer := net.Dialer{Timeout: 2 * time.Second}
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "127.0.0.1"},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" || !allowed[address] {
				return nil, errors.New("local SSO destination refused")
			}
			return dialer.DialContext(ctx, "tcp4", address)
		},
		DisableKeepAlives: true, TLSHandshakeTimeout: 2 * time.Second, ResponseHeaderTimeout: 4 * time.Second}
	return &http.Client{Timeout: 5 * time.Second, Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, tr.CloseIdleConnections
}

func localSSODo(client *http.Client, req *http.Request) ([]byte, error) {
	resp, e := client.Do(req)
	if e != nil {
		return nil, errors.New("local SSO endpoint not ready or transport refused")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("local SSO endpoint refused the operation")
	}
	body, e := io.ReadAll(io.LimitReader(resp.Body, localSSOLimit+1))
	if e != nil || len(body) > localSSOLimit {
		return nil, errors.New("local SSO response exceeds its limit")
	}
	return body, nil
}

// Native management accepts exactly the ordinary operator signature. This
// ephemeral launcher generates that key and does not expose it or the port.
func localSSOBootstrapSign(r *http.Request, body []byte, secret string, now time.Time) {
	day, stamp := now.UTC().Format("20060102"), now.UTC().Format("20060102T150405Z")
	r.Header.Set("X-Amz-Date", stamp)
	names := "content-type;host;x-amz-date;x-amz-target"
	wire := fmt.Sprintf("POST\n/\n\ncontent-type:%s\nhost:%s\nx-amz-date:%s\nx-amz-target:%s\n\n%s\n%x",
		r.Header.Get("Content-Type"), r.Host, stamp, r.Header.Get("X-Amz-Target"), names, sha256.Sum256(body))
	scope := day + "/" + localSSORegion + "/" + localSSOService + "/aws4_request"
	mac := func(key []byte, value string) []byte {
		h := hmac.New(sha256.New, key)
		_, _ = h.Write([]byte(value))
		return h.Sum(nil)
	}
	k := mac(mac(mac(mac([]byte("AWS4"+secret), day), localSSORegion), localSSOService), "aws4_request")
	sig := mac(k, fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%x", stamp, scope, sha256.Sum256([]byte(wire))))
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=TCSIMOPERATOR/"+scope+", SignedHeaders="+names+", Signature="+hex.EncodeToString(sig))
}
func localSSOAWS(ctx context.Context, client *http.Client, origin, secret, op string, input any, output any) error {
	body, e := json.Marshal(input)
	if e != nil {
		return e
	}
	r, e := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/", bytes.NewReader(body))
	if e != nil {
		return e
	}
	r.Header.Set("Content-Type", cognitoidp.JSONMediaType)
	r.Header.Set("X-Amz-Target", cognitoidp.TargetPrefix+op)
	localSSOBootstrapSign(r, body, secret, time.Now())
	out, e := localSSODo(client, r)
	if e != nil {
		return errors.New("signed local Cognito " + op + " failed")
	}
	if output != nil && json.Unmarshal(out, output) != nil {
		return errors.New("invalid local Cognito " + op + " response")
	}
	return nil
}
func localSSOGet(ctx context.Context, client *http.Client, address string) ([]byte, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if e != nil {
		return nil, e
	}
	return localSSODo(client, req)
}

func runLocalSSO(parent context.Context, args []string, output io.Writer) error {
	return runLocalSSOWithListener(parent, args, output, net.Listen)
}

func runLocalSSOWithListener(parent context.Context, args []string, output io.Writer, listen func(string, string) (net.Listener, error)) (err error) {
	flags := flag.NewFlagSet(localSSOCommand, flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() {
		fmt.Fprintln(output, "Usage: tenkacloud-simulator local-sso (private, synthetic, ephemeral Cognito + Entra sample)")
	}
	if e := flags.Parse(args); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return errors.New("local-sso takes no arguments")
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	dir, e := os.MkdirTemp("", "tenkacloud-local-sso-")
	if e != nil {
		return errors.New("cannot allocate private SSO state")
	}
	defer func() {
		err = localSSOJoinCleanup(err, os.RemoveAll(dir), errLocalSSORemoveState)
	}()
	var sockets []*localSSOListener
	for range 4 {
		l, e := listen("tcp4", "127.0.0.1:0")
		if e != nil {
			early := errors.New("cannot bind isolated local SSO listeners")
			for i := len(sockets) - 1; i >= 0; i-- {
				early = localSSOJoinCleanup(early, sockets[i].close(), errLocalSSOCloseListener)
			}
			return early
		}
		sockets = append(sockets, &localSSOListener{Listener: l})
	}
	aws, hosted, entra, relying := sockets[0], sockets[1], sockets[2], sockets[3]
	var closeState func() error
	var idp *entraidp.Fixture
	defer func() {
		if idp != nil {
			idp.Close()
		}
		for i := len(sockets) - 1; i >= 0; i-- {
			err = localSSOJoinCleanup(err, sockets[i].close(), errLocalSSOCloseListener)
		}
		if closeState != nil {
			err = localSSOJoinCleanup(err, closeState(), errLocalSSOCloseState)
		}
	}()
	capem, roots, leaves, e := localSSOTLS(4, time.Now())
	if e != nil {
		return errors.New("could not create ephemeral SSO trust")
	}
	client, stopClient := localSSOHTTP(roots, sockets)
	defer stopClient()
	options := server.DefaultProviderConfig()
	options.CognitoLocalFederationConfig = true
	pair, closeState, e := server.NewWithLocalCognitoHosted(filepath.Join(dir, "state.sqlite"), options, hosted.origin())
	if e != nil {
		return errors.New("could not initialize local Cognito")
	}
	secret := localSSOEntropy() + localSSOEntropy()
	protected, e := server.NewAccess(pair.AWS, secret, false)
	if e != nil {
		return errors.New("could not protect local Cognito bootstrap")
	}
	aws.serve(protected, leaves[0])
	var pool struct{ UserPool struct{ ID string } }
	if e = localSSOAWS(ctx, client, aws.origin(), secret, "CreateUserPool", map[string]any{"PoolName": "Local synthetic SSO"}, &pool); e != nil {
		return e
	}
	if !localSSOPoolID.MatchString(pool.UserPool.ID) || len(pool.UserPool.ID) > 96 {
		return errors.New("invalid local Cognito pool")
	}
	sp, e := pair.Browser.ServiceProvider(ctx, pool.UserPool.ID)
	if e != nil {
		return errors.New("local Cognito SAML service provider unavailable")
	}
	idp, code, e := entraidp.New(entraidp.Config{Origin: entra.origin(), ServiceProvider: entraidp.ServiceProvider{
		EntityID: sp.EntityID, ACS: sp.ACS, SigningCertificateDER: sp.SigningCertificateDER}})
	if e != nil {
		return errors.New("could not initialize local Entra")
	}
	public := idp.Public()
	entra.serve(idp, leaves[2])
	metadata, e := localSSOGet(ctx, client, public.MetadataURL)
	if e != nil {
		return errors.New("local Entra metadata could not be confirmed")
	}
	callback := relying.origin() + "/callback"
	if e = localSSOAWS(ctx, client, aws.origin(), secret, "CreateIdentityProvider", map[string]any{
		"UserPoolId": pool.UserPool.ID, "ProviderName": "Entra", "ProviderType": "SAML",
		"ProviderDetails": map[string]string{"MetadataFile": string(metadata)}}, nil); e != nil {
		return e
	}
	var app struct {
		UserPoolClient struct {
			ClientID string `json:"ClientId"`
		}
	}
	if e = localSSOAWS(ctx, client, aws.origin(), secret, "CreateUserPoolClient", map[string]any{
		"UserPoolId": pool.UserPool.ID, "ClientName": "Local browser sample", "GenerateSecret": false,
		"AllowedOAuthFlowsUserPoolClient": true, "AllowedOAuthFlows": []string{"code"},
		"AllowedOAuthScopes": []string{"openid"}, "CallbackURLs": []string{callback},
		"SupportedIdentityProviders": []string{"Entra"}}, &app); e != nil {
		return e
	}
	if !localSSOClientID.MatchString(app.UserPoolClient.ClientID) {
		return errors.New("invalid local Cognito app client")
	}
	// Never publish readiness unless the private management socket closed.
	if e = aws.close(); e != nil {
		return errLocalSSOCloseListener
	}
	hosted.serve(pair.Browser, leaves[1])
	rp := newLocalSSORelyingParty(client, hosted.origin()+"/_simulator/cognito/"+pool.UserPool.ID,
		app.UserPoolClient.ClientID, relying.origin())
	relying.serve(rp, leaves[3])
	if _, e = localSSOGet(ctx, client, sp.MetadataURL); e != nil {
		return errors.New("signed Cognito SP metadata could not be confirmed")
	}
	if _, e = localSSOGet(ctx, client, relying.origin()+"/"); e != nil {
		return errors.New("local sample browser page unavailable")
	}
	caPath, codePath := filepath.Join(dir, "local-ca.pem"), filepath.Join(dir, "login-code")
	if e = os.WriteFile(caPath, capem, 0600); e != nil {
		return errors.New("cannot store public per-run browser CA")
	}
	if e = os.WriteFile(codePath, []byte(code+"\n"), 0600); e != nil {
		return errors.New("cannot store private one-shot Entra code")
	}
	if _, e = fmt.Fprintf(output, "Local SSO sample ready (synthetic identity; expires within 10 minutes).\nOpen: %s/\nTrust this per-run CA only in an isolated browser profile: %s\nOne-shot Entra code file (private): %s\nStop with Ctrl+C; files and state are removed.\n", relying.origin(), caPath, codePath); e != nil {
		return e
	}
	select {
	case <-ctx.Done():
		return nil
	case <-hosted.done:
		hosted.serveFinished = true
	case <-entra.done:
		entra.serveFinished = true
	case <-relying.done:
		relying.serveFinished = true
	}
	return errors.New("an isolated local SSO listener stopped")
}
