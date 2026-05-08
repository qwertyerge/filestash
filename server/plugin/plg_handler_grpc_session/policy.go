package plg_handler_grpc_session

import (
	"crypto/x509"
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"time"

	. "github.com/mickael-kerjean/filestash/server/common"
	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
)

type policyEngine struct {
	clients []clientPolicy
}

type clientPolicy struct {
	Identity    string     `json:"identity"`
	Role        string     `json:"role"`
	HostCIDRs   []string   `json:"host_cidrs"`
	AccessModes []string   `json:"access_modes"`
	MaxSessions int        `json:"max_sessions"`
	Lease       leaseJSON  `json:"lease"`
	Limits      limitsJSON `json:"limits"`

	parsedCIDRs []*net.IPNet
}

type leaseJSON struct {
	DurationSeconds    uint32 `json:"duration_seconds"`
	IdleTimeoutSeconds uint32 `json:"idle_timeout_seconds"`
	MaxLifetimeSeconds uint32 `json:"max_lifetime_seconds"`
	Renewable          bool   `json:"renewable"`
}

type limitsJSON struct {
	MaxStreamBytes int64 `json:"max_stream_bytes"`
}

type policyConfig struct {
	Clients []clientPolicy `json:"clients"`
}

type leaseRequest struct {
	duration    time.Duration
	idleTimeout time.Duration
	maxLifetime time.Duration
	renewable   bool
}

type openPolicyRequest struct {
	backendType   string
	backendParams map[string]string
	mode          pb.AccessMode
	lease         leaseRequest
}

type effectivePolicy struct {
	operator       bool
	mode           pb.AccessMode
	lease          effectiveLease
	maxSessions    int
	maxStreamBytes int64
}

func parsePolicyConfig(raw []byte) (*policyEngine, error) {
	cfg := policyConfig{}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return &policyEngine{clients: nil}, nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	for i := range cfg.Clients {
		if strings.TrimSpace(cfg.Clients[i].Identity) == "" {
			return nil, ErrNotValid
		}
		for _, cidr := range cfg.Clients[i].HostCIDRs {
			cidr = strings.TrimSpace(cidr)
			_, parsed, err := net.ParseCIDR(cidr)
			if err != nil {
				return nil, err
			}
			cfg.Clients[i].parsedCIDRs = append(cfg.Clients[i].parsedCIDRs, parsed)
		}
	}
	return &policyEngine{clients: cfg.Clients}, nil
}

func (e *policyEngine) policyFor(identity string) (*clientPolicy, error) {
	if e == nil || strings.TrimSpace(identity) == "" {
		return nil, ErrNotAuthorized
	}
	for i := range e.clients {
		if e.clients[i].Identity == identity {
			return &e.clients[i], nil
		}
	}
	return nil, ErrNotAuthorized
}

func (p *clientPolicy) effectiveOpen(req openPolicyRequest) (effectivePolicy, error) {
	if err := p.validateTarget(req.backendType, req.backendParams); err != nil {
		return effectivePolicy{}, err
	}
	mode := p.clampMode(req.mode)
	if mode == pb.AccessMode_ACCESS_MODE_UNSPECIFIED {
		return effectivePolicy{}, ErrPermissionDenied
	}
	return effectivePolicy{
		operator:       p.Role == "operator",
		mode:           mode,
		lease:          p.clampLease(req.lease),
		maxSessions:    p.MaxSessions,
		maxStreamBytes: p.Limits.MaxStreamBytes,
	}, nil
}

func (p *clientPolicy) clampMode(requested pb.AccessMode) pb.AccessMode {
	switch requested {
	case pb.AccessMode_ACCESS_MODE_UNSPECIFIED:
		requested = pb.AccessMode_ACCESS_MODE_READ
	case pb.AccessMode_ACCESS_MODE_READ, pb.AccessMode_ACCESS_MODE_READ_WRITE:
	default:
		return pb.AccessMode_ACCESS_MODE_UNSPECIFIED
	}

	canRead := false
	canWrite := false
	for _, m := range p.AccessModes {
		switch strings.ToLower(strings.TrimSpace(m)) {
		case "read":
			canRead = true
		case "write", "read_write", "read-write", "rw":
			canWrite = true
		}
	}

	if requested == pb.AccessMode_ACCESS_MODE_READ_WRITE && canWrite {
		return pb.AccessMode_ACCESS_MODE_READ_WRITE
	}
	if canRead || canWrite {
		return pb.AccessMode_ACCESS_MODE_READ
	}
	return pb.AccessMode_ACCESS_MODE_UNSPECIFIED
}

func (p *clientPolicy) clampLease(req leaseRequest) effectiveLease {
	maxDuration := seconds(p.Lease.DurationSeconds)
	maxIdle := seconds(p.Lease.IdleTimeoutSeconds)
	maxLifetime := seconds(p.Lease.MaxLifetimeSeconds)
	return effectiveLease{
		duration:    clampDuration(req.duration, maxDuration),
		idleTimeout: clampDuration(req.idleTimeout, maxIdle),
		maxLifetime: clampDuration(req.maxLifetime, maxLifetime),
		renewable:   req.renewable && p.Lease.Renewable,
	}
}

func seconds(v uint32) time.Duration {
	return time.Duration(v) * time.Second
}

func clampDuration(requested, policyDefault time.Duration) time.Duration {
	if requested <= 0 {
		return policyDefault
	}
	if policyDefault <= 0 || requested < policyDefault {
		return requested
	}
	return policyDefault
}

func (p *clientPolicy) validateTarget(backendType string, params map[string]string) error {
	if len(p.parsedCIDRs) == 0 {
		return nil
	}
	targets := networkTargets(params)
	if len(targets) == 0 {
		if isNonNetworkBackendType(backendType) {
			return nil
		}
		return ErrPermissionDenied
	}
	for _, target := range targets {
		if err := p.validateTargetHost(target); err != nil {
			return err
		}
	}
	return nil
}

func (p *clientPolicy) validateTargetHost(target string) error {
	host := extractHost(target)
	if host == "" {
		return ErrPermissionDenied
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return ErrPermissionDenied
	}

	for _, cidr := range p.parsedCIDRs {
		if cidr.Contains(ip) {
			return nil
		}
	}
	return ErrPermissionDenied
}

func networkTargets(params map[string]string) []string {
	keys := []string{"hostname", "host", "endpoint", "url"}
	targets := []string{}
	seen := map[string]bool{}
	for _, key := range keys {
		target := strings.TrimSpace(params[key])
		if target == "" || seen[target] {
			continue
		}
		seen[target] = true
		targets = append(targets, target)
	}
	return targets
}

func isNonNetworkBackendType(backendType string) bool {
	switch strings.ToLower(strings.TrimSpace(backendType)) {
	case "local", "tmp", "blackhole", BACKEND_NIL:
		return true
	default:
		return false
	}
}

func extractHost(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}

	if u, err := url.Parse(target); err == nil && u.Host != "" {
		return trimHost(u.Host)
	}
	return trimHost(target)
}

func trimHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "[") {
		if h, _, err := net.SplitHostPort(host); err == nil {
			return strings.Trim(h, "[]")
		}
		return strings.Trim(host, "[]")
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	if strings.Count(host, ":") == 1 {
		if before, _, ok := strings.Cut(host, ":"); ok {
			return before
		}
	}
	return host
}

func identityFromCertificate(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	if len(cert.URIs) > 0 && cert.URIs[0] != nil {
		return cert.URIs[0].String()
	}
	if len(cert.DNSNames) > 0 {
		return cert.DNSNames[0]
	}
	if len(cert.EmailAddresses) > 0 {
		return cert.EmailAddresses[0]
	}
	if len(cert.IPAddresses) > 0 {
		return cert.IPAddresses[0].String()
	}
	return cert.Subject.CommonName
}
