package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/netip"
	"os"
	"time"
)

const maxCognitoLocalTLSFile = 128 << 10

// Only the ordinary authenticated local configuration listener can be upgraded.
// This does not create Hosted Login and does not replace any authorization check.
func configureCognitoLocalTLS(enabled bool, host, certPath, keyPath string) (*tls.Config, error) {
	if certPath == "" && keyPath == "" {
		return nil, nil
	}
	if !enabled || certPath == "" || keyPath == "" {
		return nil, errors.New("local Cognito TLS requires both files, federation config, and no Battle or owned PostgreSQL runtime")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() || ip.Is4In6() || ip.Zone() != "" || host != ip.String() {
		return nil, errors.New("local Cognito TLS requires a canonical numeric-loopback bind")
	}
	for _, path := range []string{certPath, keyPath} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxCognitoLocalTLSFile {
			return nil, errors.New("local Cognito TLS requires bounded regular certificate and key files")
		}
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil || len(pair.Certificate) == 0 {
		return nil, errors.New("local Cognito TLS certificate and key could not be loaded")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	now := time.Now()
	if err != nil || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) || leaf.VerifyHostname(host) != nil {
		return nil, errors.New("local Cognito TLS requires a current certificate covering the bind address")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}, nil
}
