package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func localCognitoTLSFixture(t *testing.T, address string, expires time.Time) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-2 * time.Hour), NotAfter: expires,
		IPAddresses: []net.IP{net.ParseIP(address)},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	certPath = filepath.Join(scratch, "synthetic.pem")
	keyPath = filepath.Join(scratch, "synthetic-key.pem")
	for file, value := range map[string]*pem.Block{
		certPath: {Type: "CERTIFICATE", Bytes: der},
		keyPath:  {Type: "PRIVATE KEY", Bytes: pk},
	} {
		if err := os.WriteFile(file, pem.EncodeToMemory(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return certPath, keyPath
}

func TestCognitoLocalTLSRequiresExplicitLocalAuthorityAndCertificate(t *testing.T) {
	if enabled, err := configureCognitoLocalTLS(false, "", "", ""); err != nil || enabled != nil {
		t.Fatal("TLS must be default-off")
	}
	cert, key := localCognitoTLSFixture(t, "127.0.0.1", time.Now().Add(time.Hour))
	foreign, foreignKey := localCognitoTLSFixture(t, "127.0.0.2", time.Now().Add(time.Hour))
	expired, expiredKey := localCognitoTLSFixture(t, "127.0.0.1", time.Now().Add(-time.Minute))
	config, err := configureCognitoLocalTLS(true, "127.0.0.1", cert, key)
	if err != nil || config == nil || len(config.Certificates) != 1 {
		t.Fatalf("approved loopback TLS refused: %v", err)
	}
	for _, tc := range []struct {
		name            string
		enabled         bool
		host, cert, key string
	}{
		{"not opted in", false, "127.0.0.1", cert, key},
		{"no key", true, "127.0.0.1", cert, ""},
		{"DNS alias", true, "localhost", cert, key},
		{"non-local", true, "192.0.2.1", cert, key},
		{"mapped loopback", true, "::ffff:127.0.0.1", cert, key},
		{"wrong SAN", true, "127.0.0.1", foreign, foreignKey},
		{"wrong key", true, "127.0.0.1", cert, foreignKey},
		{"expired", true, "127.0.0.1", expired, expiredKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if result, err := configureCognitoLocalTLS(tc.enabled, tc.host, tc.cert, tc.key); err == nil || result != nil {
				t.Fatal("unqualified TLS inputs were accepted")
			}
		})
	}
}
