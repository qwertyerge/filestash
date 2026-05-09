package client

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTLSConfigLoadsClientCertificateAndCA(t *testing.T) {
	certs := writeClientCertBundle(t)

	cfg, err := TLSConfig(Options{
		ClientCertFile: certs.clientCert,
		ClientKeyFile:  certs.clientKey,
		CAFile:         certs.caCert,
		ServerName:     "localhost",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("certificates=%d", len(cfg.Certificates))
	}
	if cfg.RootCAs == nil || len(cfg.RootCAs.Subjects()) == 0 {
		t.Fatal("missing root CA")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("min version=%x", cfg.MinVersion)
	}
	if cfg.ServerName != "localhost" {
		t.Fatalf("server name=%q", cfg.ServerName)
	}
}

func TestTLSConfigReportsInvalidInputs(t *testing.T) {
	certs := writeClientCertBundle(t)
	missing := filepath.Join(t.TempDir(), "missing.pem")
	invalid := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(invalid, []byte("not pem"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		opts Options
		want string
	}{
		{
			name: "missing client cert",
			opts: Options{ClientCertFile: missing, ClientKeyFile: certs.clientKey, CAFile: certs.caCert},
			want: "client certificate",
		},
		{
			name: "invalid client key",
			opts: Options{ClientCertFile: certs.clientCert, ClientKeyFile: invalid, CAFile: certs.caCert},
			want: "client certificate or key",
		},
		{
			name: "missing CA",
			opts: Options{ClientCertFile: certs.clientCert, ClientKeyFile: certs.clientKey, CAFile: missing},
			want: "CA certificate",
		},
		{
			name: "invalid CA",
			opts: Options{ClientCertFile: certs.clientCert, ClientKeyFile: certs.clientKey, CAFile: invalid},
			want: "CA certificate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := TLSConfig(tt.opts)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.want)
			}
		})
	}
}

func TestDialReturnsErrorWhenSidecarUnavailable(t *testing.T) {
	certs := writeClientCertBundle(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	conn, err := Dial(ctx, Options{
		Addr:           addr,
		ClientCertFile: certs.clientCert,
		ClientKeyFile:  certs.clientKey,
		CAFile:         certs.caCert,
		ServerName:     "localhost",
	})
	if err == nil {
		conn.Close()
		t.Fatal("expected unavailable sidecar dial to fail")
	}
}

type clientCertBundle struct {
	caCert     string
	clientCert string
	clientKey  string
}

func writeClientCertBundle(t *testing.T) clientCertBundle {
	t.Helper()

	dir := t.TempDir()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "sidecar client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	caPath := filepath.Join(dir, "ca.crt")
	clientCertPath := filepath.Join(dir, "client.crt")
	clientKeyPath := filepath.Join(dir, "client.key")
	writePEMFile(t, caPath, "CERTIFICATE", caDER)
	writePEMFile(t, clientCertPath, "CERTIFICATE", clientDER)
	writePEMFile(t, clientKeyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(clientKey))

	return clientCertBundle{
		caCert:     caPath,
		clientCert: clientCertPath,
		clientKey:  clientKeyPath,
	}
}

func writePEMFile(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: typ, Bytes: der}); err != nil {
		t.Fatal(err)
	}
}
