package plg_handler_grpc_session

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"net/url"
	"testing"
	"time"

	. "github.com/mickael-kerjean/filestash/server/common"
	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
)

func TestPolicyMatchesIdentityAndClampsLease(t *testing.T) {
	engine, err := parsePolicyConfig([]byte(`{
	  "clients": [{
	    "identity": "spiffe://workspace/orchestrator",
	    "role": "operator",
	    "host_cidrs": ["10.0.0.0/8"],
	    "access_modes": ["read", "write"],
	    "max_sessions": 2,
	    "lease": {
	      "duration_seconds": 900,
	      "idle_timeout_seconds": 120,
	      "max_lifetime_seconds": 3600,
	      "renewable": true
	    },
	    "limits": {"max_stream_bytes": 1024}
	  }]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := engine.policyFor("spiffe://workspace/orchestrator")
	if err != nil {
		t.Fatal(err)
	}
	effective, err := p.effectiveOpen(openPolicyRequest{
		mode: pb.AccessMode_ACCESS_MODE_READ_WRITE,
		lease: leaseRequest{
			duration:    24 * time.Hour,
			idleTimeout: 0,
			maxLifetime: 24 * time.Hour,
			renewable:   true,
		},
		backendParams: map[string]string{"hostname": "10.1.2.3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !effective.operator {
		t.Fatal("expected operator")
	}
	if effective.mode != pb.AccessMode_ACCESS_MODE_READ_WRITE {
		t.Fatalf("mode=%s", effective.mode)
	}
	if effective.lease.duration != 15*time.Minute {
		t.Fatalf("duration=%s", effective.lease.duration)
	}
	if effective.lease.idleTimeout != 2*time.Minute {
		t.Fatalf("idleTimeout=%s", effective.lease.idleTimeout)
	}
	if effective.lease.maxLifetime != time.Hour {
		t.Fatalf("maxLifetime=%s", effective.lease.maxLifetime)
	}
	if !effective.lease.renewable {
		t.Fatal("expected renewable")
	}
	if effective.maxSessions != 2 {
		t.Fatalf("maxSessions=%d", effective.maxSessions)
	}
	if effective.maxStreamBytes != 1024 {
		t.Fatalf("maxStreamBytes=%d", effective.maxStreamBytes)
	}
}

func TestPolicyRejectsHostOutsideCIDR(t *testing.T) {
	engine, err := parsePolicyConfig([]byte(`{
	  "clients": [{
	    "identity": "client-a",
	    "host_cidrs": ["10.0.0.0/8"],
	    "access_modes": ["read"],
	    "lease": {
	      "duration_seconds": 60,
	      "idle_timeout_seconds": 60,
	      "max_lifetime_seconds": 60,
	      "renewable": true
	    }
	  }]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := engine.policyFor("client-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.effectiveOpen(openPolicyRequest{
		mode:          pb.AccessMode_ACCESS_MODE_READ,
		backendParams: map[string]string{"hostname": "http://192.168.1.10:8080/path"},
	}); err == nil {
		t.Fatal("expected CIDR rejection")
	}
}

func TestPolicyRejectsHostnameUnderCIDRPolicyWithoutLookup(t *testing.T) {
	engine, err := parsePolicyConfig([]byte(`{
	  "clients": [{
	    "identity": "client-a",
	    "host_cidrs": ["127.0.0.0/8", "::1/128"],
	    "access_modes": ["read"],
	    "lease": {
	      "duration_seconds": 60,
	      "idle_timeout_seconds": 60,
	      "max_lifetime_seconds": 60,
	      "renewable": true
	    }
	  }]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := engine.policyFor("client-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.effectiveOpen(openPolicyRequest{
		mode:          pb.AccessMode_ACCESS_MODE_READ,
		backendParams: map[string]string{"hostname": "localhost"},
	}); err == nil {
		t.Fatal("expected hostname rejection under CIDR policy")
	}
}

func TestPolicyRejectsHostlessTargetsUnderCIDRPolicy(t *testing.T) {
	engine, err := parsePolicyConfig([]byte(`{
	  "clients": [{
	    "identity": "client-a",
	    "host_cidrs": ["10.0.0.0/8"],
	    "access_modes": ["read"],
	    "lease": {"duration_seconds": 60}
	  }]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := engine.policyFor("client-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"http://", "[]"} {
		t.Run(target, func(t *testing.T) {
			if _, err := p.effectiveOpen(openPolicyRequest{
				backendType:   "sftp",
				backendParams: map[string]string{"url": target},
				mode:          pb.AccessMode_ACCESS_MODE_READ,
			}); err == nil {
				t.Fatal("expected hostless target rejection")
			}
		})
	}
}

func TestPolicyRejectsNetworkBackendWithoutRecognizedTargetUnderCIDRPolicy(t *testing.T) {
	engine, err := parsePolicyConfig([]byte(`{
	  "clients": [{
	    "identity": "client-a",
	    "host_cidrs": ["10.0.0.0/8"],
	    "access_modes": ["read"],
	    "lease": {"duration_seconds": 60}
	  }]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := engine.policyFor("client-a")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		params map[string]string
	}{
		{name: "nil params", params: nil},
		{name: "empty map", params: map[string]string{}},
		{name: "empty hostname", params: map[string]string{"hostname": ""}},
		{name: "unrecognized target key", params: map[string]string{"address": "10.1.2.3"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := p.effectiveOpen(openPolicyRequest{
				backendType:   "sftp",
				backendParams: tt.params,
				mode:          pb.AccessMode_ACCESS_MODE_READ,
			}); err == nil {
				t.Fatal("expected missing target rejection")
			}
		})
	}
}

func TestPolicyAllowsKnownNonNetworkBackendWithoutTargetUnderCIDRPolicy(t *testing.T) {
	engine, err := parsePolicyConfig([]byte(`{
	  "clients": [{
	    "identity": "client-a",
	    "host_cidrs": ["10.0.0.0/8"],
	    "access_modes": ["read"],
	    "lease": {"duration_seconds": 60}
	  }]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := engine.policyFor("client-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.effectiveOpen(openPolicyRequest{
		backendType:   "local",
		backendParams: map[string]string{},
		mode:          pb.AccessMode_ACCESS_MODE_READ,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyDoesNotFilterBackendType(t *testing.T) {
	engine, err := parsePolicyConfig([]byte(`{
	  "clients": [{
	    "identity": "client-a",
	    "access_modes": ["read"],
	    "lease": {
	      "duration_seconds": 60,
	      "idle_timeout_seconds": 60,
	      "max_lifetime_seconds": 60,
	      "renewable": true
	    }
	  }]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := engine.policyFor("client-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.effectiveOpen(openPolicyRequest{
		mode:          pb.AccessMode_ACCESS_MODE_READ,
		backendType:   "made-up-for-test",
		backendParams: map[string]string{},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyRejectsUnknownAccessMode(t *testing.T) {
	engine, err := parsePolicyConfig([]byte(`{
	  "clients": [{
	    "identity": "client-a",
	    "access_modes": ["read", "write"],
	    "lease": {"duration_seconds": 60}
	  }]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := engine.policyFor("client-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.effectiveOpen(openPolicyRequest{
		mode: pb.AccessMode(99),
	}); err == nil {
		t.Fatal("expected unknown access mode to fail")
	}
}

func TestPolicyRejectsBlankConfiguredIdentity(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "missing identity", raw: `{"clients": [{"access_modes": ["read"]}]}`},
		{name: "blank identity", raw: `{"clients": [{"identity": "  ", "access_modes": ["read"]}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parsePolicyConfig([]byte(tt.raw)); err == nil {
				t.Fatal("expected blank identity rejection")
			}
		})
	}
}

func TestPolicyRejectsBlankLookupIdentity(t *testing.T) {
	engine, err := parsePolicyConfig([]byte(`{
	  "clients": [{"identity": "client-a", "access_modes": ["read"]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.policyFor(""); err != ErrNotAuthorized {
		t.Fatalf("err=%v", err)
	}
	if _, err := engine.policyFor("  "); err != ErrNotAuthorized {
		t.Fatalf("err=%v", err)
	}
}

func TestPolicyRejectsEmptyCertificateIdentity(t *testing.T) {
	engine, err := parsePolicyConfig([]byte(`{
	  "clients": [{"identity": "client-a", "access_modes": ["read"]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.policyFor(identityFromCertificate(nil)); err != ErrNotAuthorized {
		t.Fatalf("nil cert err=%v", err)
	}
	if _, err := engine.policyFor(identityFromCertificate(&x509.Certificate{})); err != ErrNotAuthorized {
		t.Fatalf("empty cert err=%v", err)
	}
}

func TestPolicyRejectsUnmappedIdentity(t *testing.T) {
	engine, err := parsePolicyConfig([]byte(`{
	  "clients": [{"identity": "client-a", "access_modes": ["read"]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.policyFor("client-b"); err != ErrNotAuthorized {
		t.Fatalf("err=%v", err)
	}
}

func TestPolicyParsesEmptyConfigWithNoClients(t *testing.T) {
	engine, err := parsePolicyConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.policyFor("client-a"); err != ErrNotAuthorized {
		t.Fatalf("err=%v", err)
	}
}

func TestPolicyClampsReadWriteRequestToRead(t *testing.T) {
	engine, err := parsePolicyConfig([]byte(`{
	  "clients": [{
	    "identity": "client-a",
	    "access_modes": ["read"],
	    "lease": {"duration_seconds": 60}
	  }]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := engine.policyFor("client-a")
	if err != nil {
		t.Fatal(err)
	}
	effective, err := p.effectiveOpen(openPolicyRequest{
		mode: pb.AccessMode_ACCESS_MODE_READ_WRITE,
	})
	if err != nil {
		t.Fatal(err)
	}
	if effective.mode != pb.AccessMode_ACCESS_MODE_READ {
		t.Fatalf("mode=%s", effective.mode)
	}
}

func TestIdentityFromCertificatePrefersSANsThenSubject(t *testing.T) {
	uri, err := url.Parse("spiffe://workspace/orchestrator")
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		URIs:           []*url.URL{uri},
		DNSNames:       []string{"dns.example.test"},
		EmailAddresses: []string{"client@example.test"},
		IPAddresses:    []net.IP{net.ParseIP("10.1.2.3")},
		Subject:        pkix.Name{CommonName: "legacy-cn"},
	}
	if got := identityFromCertificate(cert); got != "spiffe://workspace/orchestrator" {
		t.Fatalf("identity=%q", got)
	}

	cert.URIs = nil
	if got := identityFromCertificate(cert); got != "dns.example.test" {
		t.Fatalf("identity=%q", got)
	}
}

func TestIdentityFromCertificateFallsBackToCommonName(t *testing.T) {
	cert := &x509.Certificate{Subject: pkix.Name{CommonName: "legacy-cn"}}
	if got := identityFromCertificate(cert); got != "legacy-cn" {
		t.Fatalf("identity=%q", got)
	}
}
