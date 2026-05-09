package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func TLSConfig(opts Options) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(opts.ClientCertFile, opts.ClientKeyFile)
	if err != nil {
		if _, statErr := os.Stat(opts.ClientCertFile); statErr != nil {
			return nil, fmt.Errorf("load client certificate %q: %w", opts.ClientCertFile, statErr)
		}
		if _, statErr := os.Stat(opts.ClientKeyFile); statErr != nil {
			return nil, fmt.Errorf("load client key %q: %w", opts.ClientKeyFile, statErr)
		}
		return nil, fmt.Errorf("load client certificate or key: %w", err)
	}

	caBytes, err := os.ReadFile(opts.CAFile)
	if err != nil {
		return nil, fmt.Errorf("load CA certificate %q: %w", opts.CAFile, err)
	}
	rootCAs := x509.NewCertPool()
	if !rootCAs.AppendCertsFromPEM(caBytes) {
		return nil, fmt.Errorf("load CA certificate %q: invalid PEM", opts.CAFile)
	}

	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		RootCAs:      rootCAs,
		ServerName:   opts.ServerName,
	}, nil
}

func Dial(ctx context.Context, opts Options) (*grpc.ClientConn, error) {
	tlsConfig, err := TLSConfig(opts)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.DialContext(ctx, opts.Addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	if err != nil {
		return nil, fmt.Errorf("dial sidecar %q: %w", opts.Addr, err)
	}
	return conn, nil
}
