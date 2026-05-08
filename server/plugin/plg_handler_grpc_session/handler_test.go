package plg_handler_grpc_session

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/mickael-kerjean/filestash/server/common"
	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func TestRenewSessionRPCRequiresOwner(t *testing.T) {
	svc := newTestSidecarService(t, nil, []openSessionInput{testOpenInput("client-a", "s1")})

	lease, err := svc.RenewSession(identityContext("client-a"), &pb.RenewSessionRequest{SessionId: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if lease.GetSessionId() != "s1" {
		t.Fatalf("session_id=%q", lease.GetSessionId())
	}
	if !lease.GetRenewable() {
		t.Fatal("expected renewable lease")
	}

	_, err = svc.RenewSession(identityContext("client-b"), &pb.RenewSessionRequest{SessionId: "s1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
}

func TestRenewSessionNonRenewableIsFailedPrecondition(t *testing.T) {
	input := testOpenInput("client-a", "s1")
	input.lease.renewable = false
	svc := newTestSidecarService(t, nil, []openSessionInput{input})

	_, err := svc.RenewSession(identityContext("client-a"), &pb.RenewSessionRequest{SessionId: "s1"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
}

func TestCloseSessionRPCIsIdempotent(t *testing.T) {
	backend := &closeTrackingBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{testOpenInputWithBackend("client-a", "s1", backend)})

	for i := 0; i < 2; i++ {
		res, err := svc.Close(identityContext("client-a"), &pb.CloseSessionRequest{SessionId: "s1"})
		if err != nil {
			t.Fatal(err)
		}
		if res.GetSessionId() != "s1" || res.GetState() != pb.SessionState_SESSION_STATE_CLOSED {
			t.Fatalf("unexpected close response: %+v", res)
		}
	}
	if backend.closed != 1 {
		t.Fatalf("closed=%d", backend.closed)
	}
}

func TestCloseSessionRPCSucceedsWhenBackendCloseReturnsError(t *testing.T) {
	backend := &closeErrorBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{testOpenInputWithBackend("client-a", "s1", backend)})

	for i := 0; i < 2; i++ {
		res, err := svc.Close(identityContext("client-a"), &pb.CloseSessionRequest{SessionId: "s1"})
		if err != nil {
			t.Fatalf("close %d: %v", i+1, err)
		}
		if res.GetSessionId() != "s1" || res.GetState() != pb.SessionState_SESSION_STATE_CLOSED {
			t.Fatalf("unexpected close response: %+v", res)
		}
	}
	if backend.closed != 1 {
		t.Fatalf("closed=%d", backend.closed)
	}
}

func TestIdentityFromContextMissingOrBlankTLSIdentity(t *testing.T) {
	if _, err := identityFromContext(context.Background()); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing peer code=%s err=%v", status.Code(err), err)
	}

	noCertCtx := peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{}},
	})
	if _, err := identityFromContext(noCertCtx); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing cert code=%s err=%v", status.Code(err), err)
	}

	blankCtx := peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}},
	})
	if _, err := identityFromContext(blankCtx); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("blank identity code=%s err=%v", status.Code(err), err)
	}
}

func TestForceCloseOperatorCanCloseAnotherOwnersSession(t *testing.T) {
	policies := testPolicyEngine(t, map[string]string{
		"client-a": "",
		"operator": "operator",
	})
	svc := newTestSidecarService(t, policies, []openSessionInput{testOpenInput("client-a", "s1")})

	res, err := svc.ForceClose(identityContext("operator"), &pb.ForceCloseRequest{SessionId: "s1", Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetSessionId() != "s1" || res.GetState() != pb.SessionState_SESSION_STATE_CLOSED {
		t.Fatalf("unexpected force close response: %+v", res)
	}
}

func TestForceCloseNonOperatorIsPermissionDenied(t *testing.T) {
	policies := testPolicyEngine(t, map[string]string{
		"client-a": "",
		"client-b": "",
	})
	svc := newTestSidecarService(t, policies, []openSessionInput{testOpenInput("client-a", "s1")})

	_, err := svc.ForceClose(identityContext("client-b"), &pb.ForceCloseRequest{SessionId: "s1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
}

func TestSidecarServiceRejectsBlankSessionID(t *testing.T) {
	policies := testPolicyEngine(t, map[string]string{"operator": "operator"})
	svc := newTestSidecarService(t, policies, nil)
	ctx := identityContext("operator")

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "renew",
			call: func() error {
				_, err := svc.RenewSession(ctx, &pb.RenewSessionRequest{})
				return err
			},
		},
		{
			name: "close",
			call: func() error {
				_, err := svc.Close(ctx, &pb.CloseSessionRequest{})
				return err
			},
		},
		{
			name: "get",
			call: func() error {
				_, err := svc.GetSession(ctx, &pb.GetSessionRequest{})
				return err
			},
		},
		{
			name: "force close",
			call: func() error {
				_, err := svc.ForceClose(ctx, &pb.ForceCloseRequest{})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("code=%s err=%v", status.Code(err), err)
			}
		})
	}
}

func TestGetSessionReturnsRedactedMetadataAndRejectsNonOwner(t *testing.T) {
	svc := newTestSidecarService(t, nil, []openSessionInput{testOpenInput("client-a", "s1")})

	info, err := svc.GetSession(identityContext("client-a"), &pb.GetSessionRequest{SessionId: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if info.GetSessionId() != "s1" ||
		info.GetOwnerIdentity() != "client-a" ||
		info.GetBackendType() != "sftp" ||
		info.GetRootPath() != "/work" ||
		info.GetMode() != pb.AccessMode_ACCESS_MODE_READ_WRITE ||
		info.GetState() != pb.SessionState_SESSION_STATE_ACTIVE {
		t.Fatalf("unexpected session info: %+v", info)
	}
	if info.GetExternalRef().GetKind() != "job" || info.GetExternalRef().GetId() != "job-s1" {
		t.Fatalf("unexpected external ref: %+v", info.GetExternalRef())
	}
	if info.GetRedactedTarget() != "" {
		t.Fatalf("redacted target=%q", info.GetRedactedTarget())
	}
	if info.GetOpenedAt() == nil || info.GetLastUsedAt() == nil || info.GetLease() == nil {
		t.Fatalf("missing timestamps or lease: %+v", info)
	}

	_, err = svc.GetSession(identityContext("client-b"), &pb.GetSessionRequest{SessionId: "s1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
}

func TestListSessionsFiltersOwnershipAndClosedSessions(t *testing.T) {
	policies := testPolicyEngine(t, map[string]string{
		"client-a": "",
		"client-b": "",
		"operator": "operator",
	})
	svc := newTestSidecarService(t, policies, []openSessionInput{
		testOpenInput("client-a", "s1"),
		testOpenInput("client-b", "s2"),
		testOpenInput("client-a", "s3"),
	})
	if _, err := svc.Close(identityContext("client-a"), &pb.CloseSessionRequest{SessionId: "s3"}); err != nil {
		t.Fatal(err)
	}

	clientList, err := svc.ListSessions(identityContext("client-a"), &pb.ListSessionsRequest{IncludeClosed: false})
	if err != nil {
		t.Fatal(err)
	}
	assertSessionIDs(t, clientList.GetSessions(), []string{"s1"})

	clientWithClosed, err := svc.ListSessions(identityContext("client-a"), &pb.ListSessionsRequest{IncludeClosed: true})
	if err != nil {
		t.Fatal(err)
	}
	assertSessionIDs(t, clientWithClosed.GetSessions(), []string{"s1", "s3"})

	operatorList, err := svc.ListSessions(identityContext("operator"), &pb.ListSessionsRequest{IncludeClosed: false})
	if err != nil {
		t.Fatal(err)
	}
	assertSessionIDs(t, operatorList.GetSessions(), []string{"s1", "s2"})
}

func TestListSessionsClosesExpiredSessionBackendOnce(t *testing.T) {
	backend := &closeTrackingBackend{}
	input := testOpenInputWithBackend("client-a", "s1", backend)
	input.lease.duration = time.Minute
	input.lease.idleTimeout = time.Minute
	input.lease.maxLifetime = time.Minute
	svc := newTestSidecarService(t, nil, []openSessionInput{input})
	svc.sessionManager.now = func() time.Time { return time.Date(2026, 5, 8, 10, 2, 0, 0, time.UTC) }

	activeOnly, err := svc.ListSessions(identityContext("client-a"), &pb.ListSessionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(activeOnly.GetSessions()) != 0 {
		t.Fatalf("default list should hide expired sessions: %+v", activeOnly.GetSessions())
	}

	for i := 0; i < 2; i++ {
		res, err := svc.ListSessions(identityContext("client-a"), &pb.ListSessionsRequest{IncludeClosed: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.GetSessions()) != 1 || res.GetSessions()[0].GetState() != pb.SessionState_SESSION_STATE_EXPIRED {
			t.Fatalf("unexpected sessions: %+v", res.GetSessions())
		}
	}
	if backend.closed != 1 {
		t.Fatalf("closed=%d", backend.closed)
	}
}

func TestGrpcErrorMapsConflictToAlreadyExists(t *testing.T) {
	err := grpcError(ErrConflict)
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if status.Code(grpcError(ErrFilesystemError)) != codes.Unavailable {
		t.Fatalf("filesystem code=%s", status.Code(grpcError(ErrFilesystemError)))
	}
	if grpcError(nil) != nil {
		t.Fatal("nil error should map to nil")
	}
	if status.Code(grpcError(errors.New("boom"))) != codes.Internal {
		t.Fatalf("default code=%s", status.Code(grpcError(errors.New("boom"))))
	}
}

func TestSidecarServiceConstructorRequiresProductionDependencies(t *testing.T) {
	sessions := newSessionManager(sessionManagerOptions{})
	policies := testPolicyEngine(t, map[string]string{"operator": "operator"})

	if _, err := newSidecarService(sessions, policies); err != nil {
		t.Fatal(err)
	}
	if _, err := newSidecarService(nil, policies); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("nil sessions code=%s err=%v", status.Code(err), err)
	}
	if _, err := newSidecarService(sessions, nil); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("nil policies code=%s err=%v", status.Code(err), err)
	}
}

func TestOpenUnknownBackendRejectsNotFound(t *testing.T) {
	svc := newOpenTestSidecarService([]string{"open-s1"}, testOpenPolicy("client-a"))

	_, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{
		BackendType:   uniqueOpenBackendName(t),
		BackendParams: map[string]string{"hostname": "10.0.0.10"},
		RootPath:      "/work",
		Mode:          pb.AccessMode_ACCESS_MODE_READ,
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
}

func TestOpenMissingRootOrBackendRejectsInvalidArgument(t *testing.T) {
	driver := registerOpenTestBackend(t, &openTestDriver{})
	svc := newOpenTestSidecarService([]string{"open-s1", "open-s2"}, testOpenPolicy("client-a"))

	tests := []struct {
		name string
		req  *pb.OpenRequest
	}{
		{
			name: "missing backend",
			req:  &pb.OpenRequest{RootPath: "/work", Mode: pb.AccessMode_ACCESS_MODE_READ},
		},
		{
			name: "missing root",
			req:  &pb.OpenRequest{BackendType: driver.name, Mode: pb.AccessMode_ACCESS_MODE_READ},
		},
		{
			name: "invalid root",
			req:  &pb.OpenRequest{BackendType: driver.name, RootPath: "../escape", Mode: pb.AccessMode_ACCESS_MODE_READ},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Open(identityContext("client-a"), tt.req)
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("code=%s err=%v", status.Code(err), err)
			}
			if driver.initCalls != 0 {
				t.Fatalf("backend init was called %d times", driver.initCalls)
			}
		})
	}
}

func TestOpenUnmappedIdentityRejectsUnauthenticated(t *testing.T) {
	driver := registerOpenTestBackend(t, &openTestDriver{})
	svc := newOpenTestSidecarService([]string{"open-s1"}, testOpenPolicy("client-a"))

	_, err := svc.Open(identityContext("client-b"), &pb.OpenRequest{
		BackendType: driver.name,
		RootPath:    "/work",
		Mode:        pb.AccessMode_ACCESS_MODE_READ,
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if driver.initCalls != 0 {
		t.Fatalf("backend init was called %d times", driver.initCalls)
	}
}

func TestOpenRequiresPolicyEngine(t *testing.T) {
	driver := registerOpenTestBackend(t, &openTestDriver{})
	svc := newOpenTestSidecarService([]string{"open-s1"}, nil)

	_, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{
		BackendType: driver.name,
		RootPath:    "/work",
		Mode:        pb.AccessMode_ACCESS_MODE_READ,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if driver.initCalls != 0 {
		t.Fatalf("backend init was called %d times", driver.initCalls)
	}
}

func TestOpenPolicyClampsReadWriteRequestToReadAndLease(t *testing.T) {
	driver := registerOpenTestBackend(t, &openTestDriver{})
	svc := newOpenTestSidecarService([]string{"open-s1"}, &policyEngine{clients: []clientPolicy{{
		Identity:    "client-a",
		AccessModes: []string{"read"},
		Lease: leaseJSON{
			DurationSeconds:    300,
			IdleTimeoutSeconds: 60,
			MaxLifetimeSeconds: 900,
			Renewable:          false,
		},
	}}})

	res, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{
		BackendType:   driver.name,
		BackendParams: map[string]string{"hostname": "10.0.0.10"},
		RootPath:      "/work/",
		Mode:          pb.AccessMode_ACCESS_MODE_READ_WRITE,
		Lease: &pb.LeaseOptions{
			DurationSeconds:    3600,
			IdleTimeoutSeconds: 120,
			MaxLifetimeSeconds: 3600,
			Renewable:          true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetEffectiveMode() != pb.AccessMode_ACCESS_MODE_READ {
		t.Fatalf("mode=%s", res.GetEffectiveMode())
	}
	if res.GetLease().GetSessionId() != "open-s1" ||
		!res.GetLease().GetExpiresAt().AsTime().Equal(openTestNow.Add(5*time.Minute)) ||
		!res.GetLease().GetIdleExpiresAt().AsTime().Equal(openTestNow.Add(time.Minute)) ||
		!res.GetLease().GetMaxExpiresAt().AsTime().Equal(openTestNow.Add(15*time.Minute)) ||
		res.GetLease().GetRenewable() {
		t.Fatalf("unexpected lease: %+v", res.GetLease())
	}
}

func TestOpenAppliesCachedGlobalDefaultLeaseWhenPolicyOmitsDurations(t *testing.T) {
	oldDefaultLease := PluginDefaultLeaseSeconds
	oldDefaultIdle := PluginDefaultIdleTimeoutSeconds
	oldDefaultMaxLifetime := PluginDefaultMaxLifetimeSeconds
	defaultCalls := 0
	PluginDefaultLeaseSeconds = func() int {
		defaultCalls++
		return 600
	}
	PluginDefaultIdleTimeoutSeconds = func() int {
		defaultCalls++
		return 120
	}
	PluginDefaultMaxLifetimeSeconds = func() int {
		defaultCalls++
		return 1800
	}
	t.Cleanup(func() {
		PluginDefaultLeaseSeconds = oldDefaultLease
		PluginDefaultIdleTimeoutSeconds = oldDefaultIdle
		PluginDefaultMaxLifetimeSeconds = oldDefaultMaxLifetime
	})

	driver := registerOpenTestBackend(t, &openTestDriver{})
	sessions := newSessionManager(sessionManagerOptions{
		now: func() time.Time { return openTestNow },
		id:  func() (string, error) { return "open-s1", nil },
	})
	svc, err := newSidecarService(sessions, &policyEngine{clients: []clientPolicy{{
		Identity:    "client-a",
		AccessModes: []string{"read"},
		Lease:       leaseJSON{Renewable: true},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if defaultCalls != 3 {
		t.Fatalf("default lease calls=%d", defaultCalls)
	}
	PluginDefaultLeaseSeconds = func() int { panic("PluginDefaultLeaseSeconds called after service construction") }
	PluginDefaultIdleTimeoutSeconds = func() int { panic("PluginDefaultIdleTimeoutSeconds called after service construction") }
	PluginDefaultMaxLifetimeSeconds = func() int { panic("PluginDefaultMaxLifetimeSeconds called after service construction") }

	res, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{
		BackendType: driver.name,
		RootPath:    "/work",
		Mode:        pb.AccessMode_ACCESS_MODE_READ,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.GetLease().GetExpiresAt().AsTime().Equal(openTestNow.Add(10*time.Minute)) ||
		!res.GetLease().GetIdleExpiresAt().AsTime().Equal(openTestNow.Add(2*time.Minute)) ||
		!res.GetLease().GetMaxExpiresAt().AsTime().Equal(openTestNow.Add(30*time.Minute)) {
		t.Fatalf("unexpected lease: %+v", res.GetLease())
	}
}

func TestOpenInitializesBackendAndCreatesSessionWithNormalizedRootAndExternalRef(t *testing.T) {
	driver := registerOpenTestBackend(t, &openTestDriver{mutateParams: true})
	svc := newOpenTestSidecarService([]string{"open-s1"}, testOpenPolicy("client-a"))
	params := map[string]string{
		"hostname": "10.0.0.10",
		"username": "alice",
	}

	res, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{
		BackendType:   driver.name,
		BackendParams: params,
		RootPath:      "/work/",
		Mode:          pb.AccessMode_ACCESS_MODE_READ,
		ExternalRef:   &pb.ExternalRef{Kind: "job", Id: "job-123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetSessionId() != "open-s1" {
		t.Fatalf("session_id=%q", res.GetSessionId())
	}
	if params["mutated"] != "" {
		t.Fatalf("request params were mutated: %+v", params)
	}
	if driver.initParams["hostname"] != "10.0.0.10" || driver.initParams["username"] != "alice" {
		t.Fatalf("init params=%+v", driver.initParams)
	}
	session := svc.sessionManager.sessions["open-s1"]
	if session == nil {
		t.Fatal("session was not created")
	}
	if session.rootPath != "/work" ||
		session.backendType != driver.name ||
		session.mode != pb.AccessMode_ACCESS_MODE_READ ||
		session.externalRef != (externalRef{kind: "job", id: "job-123"}) {
		t.Fatalf("unexpected session: %+v", session)
	}
	if driver.handle.statPath != "/work" {
		t.Fatalf("stat path=%q", driver.handle.statPath)
	}
}

func TestOpenBackendContextOutlivesOpenRPCAndCancelsOnClose(t *testing.T) {
	driver := registerOpenTestBackend(t, &openTestDriver{})
	svc := newOpenTestSidecarService([]string{"open-s1"}, testOpenPolicy("client-a"))
	openCtx, cancelOpen := context.WithCancel(identityContext("client-a"))

	res, err := svc.Open(openCtx, &pb.OpenRequest{
		BackendType: driver.name,
		RootPath:    "/work",
		Mode:        pb.AccessMode_ACCESS_MODE_READ,
	})
	if err != nil {
		t.Fatal(err)
	}
	if driver.initCtx == nil {
		t.Fatal("backend init did not receive context")
	}

	cancelOpen()
	select {
	case <-driver.initCtx.Done():
		t.Fatal("backend context was canceled when Open RPC context was canceled")
	default:
	}

	if _, err := svc.Close(identityContext("client-a"), &pb.CloseSessionRequest{SessionId: res.GetSessionId()}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-driver.initCtx.Done():
	default:
		t.Fatal("backend context was not canceled after session close")
	}
}

func TestOpenRootVerificationFailureClosesBackendAndCreatesNoSession(t *testing.T) {
	handle := &openTestBackend{statErr: ErrNotFound, lsErr: ErrNotFound}
	driver := registerOpenTestBackend(t, &openTestDriver{handle: handle})
	svc := newOpenTestSidecarService([]string{"open-s1"}, testOpenPolicy("client-a"))

	_, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{
		BackendType: driver.name,
		RootPath:    "/missing",
		Mode:        pb.AccessMode_ACCESS_MODE_READ,
	})
	if status.Code(err) == codes.OK {
		t.Fatal("expected root verification error")
	}
	if handle.closedCount() != 1 {
		t.Fatalf("closed=%d", handle.closedCount())
	}
	if len(svc.sessionManager.sessions) != 0 {
		t.Fatalf("sessions=%+v", svc.sessionManager.sessions)
	}
}

func TestOpenBackendInitFailureCreatesNoSession(t *testing.T) {
	driver := registerOpenTestBackend(t, &openTestDriver{initErr: ErrNotReachable})
	svc := newOpenTestSidecarService([]string{"open-s1"}, testOpenPolicy("client-a"))

	_, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{
		BackendType: driver.name,
		RootPath:    "/work",
		Mode:        pb.AccessMode_ACCESS_MODE_READ,
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if len(svc.sessionManager.sessions) != 0 {
		t.Fatalf("sessions=%+v", svc.sessionManager.sessions)
	}
}

func TestOpenCIDRPolicyRejectsDisallowedNetworkTargetBeforeInit(t *testing.T) {
	driver := registerOpenTestBackend(t, &openTestDriver{})
	policies, err := parsePolicyConfig([]byte(`{
	  "clients": [{
	    "identity": "client-a",
	    "host_cidrs": ["10.0.0.0/8"],
	    "access_modes": ["read"],
	    "lease": {"duration_seconds": 300, "idle_timeout_seconds": 60, "max_lifetime_seconds": 900}
	  }]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	svc := newOpenTestSidecarService([]string{"open-s1"}, policies)

	_, err = svc.Open(identityContext("client-a"), &pb.OpenRequest{
		BackendType:   driver.name,
		BackendParams: map[string]string{"hostname": "192.168.1.10"},
		RootPath:      "/work",
		Mode:          pb.AccessMode_ACCESS_MODE_READ,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if driver.initCalls != 0 {
		t.Fatalf("backend init was called %d times", driver.initCalls)
	}
}

func TestOpenRedactedTargetInGetSessionAndListSessionsDoesNotLeakSecrets(t *testing.T) {
	driver := registerOpenTestBackend(t, &openTestDriver{})
	svc := newOpenTestSidecarService([]string{"open-s1"}, testOpenPolicy("client-a"))

	_, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{
		BackendType: driver.name,
		BackendParams: map[string]string{
			"url":           "https://user:secret-pass@example.com:8443/root?token=secret-token",
			"password":      "secret-pass",
			"token":         "secret-token",
			"secret":        "secret-value",
			"access-grant":  "grant-value",
			"refresh_token": "refresh-value",
		},
		RootPath: "/work",
		Mode:     pb.AccessMode_ACCESS_MODE_READ,
	})
	if err != nil {
		t.Fatal(err)
	}

	info, err := svc.GetSession(identityContext("client-a"), &pb.GetSessionRequest{SessionId: "open-s1"})
	if err != nil {
		t.Fatal(err)
	}
	assertSafeRedactedTarget(t, info.GetRedactedTarget(), "example.com")

	list, err := svc.ListSessions(identityContext("client-a"), &pb.ListSessionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.GetSessions()) != 1 {
		t.Fatalf("sessions=%+v", list.GetSessions())
	}
	assertSafeRedactedTarget(t, list.GetSessions()[0].GetRedactedTarget(), "example.com")
}

func TestOpenPerIdentityMaxSessionsRejectsResourceExhausted(t *testing.T) {
	driver := registerOpenTestBackend(t, &openTestDriver{})
	svc := newOpenTestSidecarService([]string{"open-s1", "open-s2"}, &policyEngine{clients: []clientPolicy{{
		Identity:    "client-a",
		AccessModes: []string{"read"},
		MaxSessions: 1,
		Lease:       leaseJSON{DurationSeconds: 300, IdleTimeoutSeconds: 60, MaxLifetimeSeconds: 900},
	}}})

	if _, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{BackendType: driver.name, RootPath: "/work", Mode: pb.AccessMode_ACCESS_MODE_READ}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{BackendType: driver.name, RootPath: "/work", Mode: pb.AccessMode_ACCESS_MODE_READ})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
}

func TestOpenConcurrentPerIdentityMaxSessionsDoesNotInitializeSecondBackend(t *testing.T) {
	initStarted := make(chan struct{}, 2)
	releaseInit := make(chan struct{})
	releaseOnce := sync.Once{}
	defer releaseOnce.Do(func() { close(releaseInit) })

	driver := registerOpenTestBackend(t, &openTestDriver{
		initStarted: initStarted,
		releaseInit: releaseInit,
	})
	svc := newOpenTestSidecarService([]string{"open-s1", "open-s2"}, &policyEngine{clients: []clientPolicy{{
		Identity:    "client-a",
		AccessModes: []string{"read"},
		MaxSessions: 1,
		Lease:       leaseJSON{DurationSeconds: 300, IdleTimeoutSeconds: 60, MaxLifetimeSeconds: 900},
	}}})

	errs := make(chan error, 2)
	go func() {
		_, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{BackendType: driver.name, RootPath: "/work", Mode: pb.AccessMode_ACCESS_MODE_READ})
		errs <- err
	}()
	waitOpenTestInitStarted(t, initStarted)

	go func() {
		_, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{BackendType: driver.name, RootPath: "/work", Mode: pb.AccessMode_ACCESS_MODE_READ})
		errs <- err
	}()

	secondInitialized := false
	var successCount, exhaustedCount int
	select {
	case <-initStarted:
		secondInitialized = true
	case err := <-errs:
		if status.Code(err) != codes.ResourceExhausted {
			t.Fatalf("second open returned before first session was inserted with code=%s err=%v", status.Code(err), err)
		}
		exhaustedCount++
	case <-time.After(time.Second):
		t.Fatal("second open neither rejected nor initialized")
	}

	releaseOnce.Do(func() { close(releaseInit) })
	for i := successCount + exhaustedCount; i < 2; i++ {
		err := <-errs
		switch status.Code(err) {
		case codes.OK:
			successCount++
		case codes.ResourceExhausted:
			exhaustedCount++
		default:
			t.Fatalf("unexpected open error code=%s err=%v", status.Code(err), err)
		}
	}

	if secondInitialized {
		t.Fatalf("second concurrent Open initialized backend before max_sessions admission; init_calls=%d", driver.initCallCount())
	}
	if successCount != 1 || exhaustedCount != 1 {
		t.Fatalf("success=%d exhausted=%d", successCount, exhaustedCount)
	}
	if driver.initCallCount() != 1 {
		t.Fatalf("init_calls=%d", driver.initCallCount())
	}
	if len(svc.sessionManager.sessions) != 1 {
		t.Fatalf("sessions=%+v", svc.sessionManager.sessions)
	}
}

func TestOpenCanceledDuringBackendInitReleasesReservationAndClosesEventualBackend(t *testing.T) {
	initStarted := make(chan struct{}, 1)
	releaseInit := make(chan struct{})
	driver := registerOpenTestBackend(t, &openTestDriver{
		initStarted: initStarted,
		releaseInit: releaseInit,
	})
	nextDriver := registerNamedOpenTestBackend(t, uniqueOpenBackendName(t)+"-next", &openTestDriver{})
	svc := newOpenTestSidecarService([]string{"open-s1", "open-s2"}, &policyEngine{clients: []clientPolicy{{
		Identity:    "client-a",
		AccessModes: []string{"read"},
		MaxSessions: 1,
		Lease:       leaseJSON{DurationSeconds: 300, IdleTimeoutSeconds: 60, MaxLifetimeSeconds: 900},
	}}})

	openCtx, cancelOpen := context.WithCancel(identityContext("client-a"))
	errs := make(chan error, 1)
	go func() {
		_, err := svc.Open(openCtx, &pb.OpenRequest{BackendType: driver.name, RootPath: "/work", Mode: pb.AccessMode_ACCESS_MODE_READ})
		errs <- err
	}()
	waitOpenTestInitStarted(t, initStarted)

	cancelOpen()
	select {
	case err := <-errs:
		if status.Code(err) != codes.Canceled {
			t.Fatalf("code=%s err=%v", status.Code(err), err)
		}
	case <-time.After(time.Second):
		close(releaseInit)
		t.Fatal("Open did not return after RPC context cancellation")
	}

	if _, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{BackendType: nextDriver.name, RootPath: "/work", Mode: pb.AccessMode_ACCESS_MODE_READ}); err != nil {
		close(releaseInit)
		t.Fatalf("subsequent open should proceed after canceled reservation: %v", err)
	}
	close(releaseInit)
	waitOpenTestEventually(t, func() bool { return driver.handle.closedCount() == 1 }, "eventual canceled backend close")
	if len(svc.sessionManager.sessions) != 1 {
		t.Fatalf("sessions=%+v", svc.sessionManager.sessions)
	}
}

func TestOpenCanceledAfterBackendReadyBeforeSessionInsertCreatesNoSession(t *testing.T) {
	driver := registerOpenTestBackend(t, &openTestDriver{})
	svc := newOpenTestSidecarService([]string{"open-s1"}, testOpenPolicy("client-a"))
	openCtx, cancelOpen := context.WithCancel(identityContext("client-a"))
	original := beforeOpenSessionInsert
	beforeOpenSessionInsert = func(context.Context) { cancelOpen() }
	t.Cleanup(func() { beforeOpenSessionInsert = original })

	_, err := svc.Open(openCtx, &pb.OpenRequest{
		BackendType: driver.name,
		RootPath:    "/work",
		Mode:        pb.AccessMode_ACCESS_MODE_READ,
	})
	if status.Code(err) != codes.Canceled {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if len(svc.sessionManager.sessions) != 0 {
		t.Fatalf("sessions=%+v", svc.sessionManager.sessions)
	}
	if driver.handle.closedCount() != 1 {
		t.Fatalf("closed=%d", driver.handle.closedCount())
	}
}

func TestOpenGlobalPluginMaxSessionsRejectsResourceExhausted(t *testing.T) {
	original := PluginMaxSessions
	PluginMaxSessions = func() int { return 1 }
	t.Cleanup(func() { PluginMaxSessions = original })

	driver := registerOpenTestBackend(t, &openTestDriver{})
	svc := newOpenTestSidecarService([]string{"open-s1", "open-s2"}, testOpenPolicy("client-a", "client-b"))

	if _, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{BackendType: driver.name, RootPath: "/work", Mode: pb.AccessMode_ACCESS_MODE_READ}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Open(identityContext("client-b"), &pb.OpenRequest{BackendType: driver.name, RootPath: "/work", Mode: pb.AccessMode_ACCESS_MODE_READ})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
}

func TestOpenUsesCachedGlobalMaxSessions(t *testing.T) {
	original := PluginMaxSessions
	calls := 0
	PluginMaxSessions = func() int {
		calls++
		return 1
	}
	t.Cleanup(func() { PluginMaxSessions = original })

	driver := registerOpenTestBackend(t, &openTestDriver{})
	svc := newOpenTestSidecarService([]string{"open-s1", "open-s2"}, testOpenPolicy("client-a", "client-b"))
	if calls != 1 {
		t.Fatalf("PluginMaxSessions calls=%d", calls)
	}
	PluginMaxSessions = func() int { panic("PluginMaxSessions called after service construction") }

	if _, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{BackendType: driver.name, RootPath: "/work", Mode: pb.AccessMode_ACCESS_MODE_READ}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Open(identityContext("client-b"), &pb.OpenRequest{BackendType: driver.name, RootPath: "/work", Mode: pb.AccessMode_ACCESS_MODE_READ})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
}

func TestSidecarServiceRPCsRequireSessionManager(t *testing.T) {
	svc := &sidecarService{}
	ctx := identityContext("client-a")

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "renew",
			call: func() error {
				_, err := svc.RenewSession(ctx, &pb.RenewSessionRequest{SessionId: "s1"})
				return err
			},
		},
		{
			name: "close",
			call: func() error {
				_, err := svc.Close(ctx, &pb.CloseSessionRequest{SessionId: "s1"})
				return err
			},
		},
		{
			name: "get",
			call: func() error {
				_, err := svc.GetSession(ctx, &pb.GetSessionRequest{SessionId: "s1"})
				return err
			},
		},
		{
			name: "list",
			call: func() error {
				_, err := svc.ListSessions(ctx, &pb.ListSessionsRequest{})
				return err
			},
		},
		{
			name: "force close",
			call: func() error {
				_, err := svc.ForceClose(ctx, &pb.ForceCloseRequest{SessionId: "s1"})
				return err
			},
		},
		{
			name: "list files",
			call: func() error {
				_, err := svc.List(ctx, &pb.ListRequest{SessionId: "s1"})
				return err
			},
		},
		{
			name: "stat",
			call: func() error {
				_, err := svc.Stat(ctx, &pb.StatRequest{SessionId: "s1"})
				return err
			},
		},
		{
			name: "read file",
			call: func() error {
				return svc.ReadFile(&pb.ReadFileRequest{SessionId: "s1"}, newTestReadFileStream(ctx))
			},
		},
		{
			name: "write file",
			call: func() error {
				return svc.WriteFile(newTestWriteFileStream(ctx, []*pb.WriteFileRequest{
					writeFileHeader("s1", "note.txt", 0),
				}))
			},
		},
		{
			name: "mkdir",
			call: func() error {
				_, err := svc.Mkdir(ctx, &pb.MkdirRequest{SessionId: "s1"})
				return err
			},
		},
		{
			name: "remove",
			call: func() error {
				_, err := svc.Remove(ctx, &pb.RemoveRequest{SessionId: "s1"})
				return err
			},
		},
		{
			name: "rename",
			call: func() error {
				_, err := svc.Rename(ctx, &pb.RenameRequest{SessionId: "s1", From: "a", To: "b"})
				return err
			},
		},
		{
			name: "touch",
			call: func() error {
				_, err := svc.Touch(ctx, &pb.TouchRequest{SessionId: "s1"})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("code=%s err=%v", status.Code(err), err)
			}
		})
	}
}

func TestListRPCUsesSessionRootAndRelativePathResolution(t *testing.T) {
	backend := &filesystemRPCBackend{
		lsEntries: []os.FileInfo{
			fakeInfo{name: "report.txt", size: 11, mode: 0o644},
			fakeInfo{name: "docs", isDir: true, mode: os.ModeDir | 0o755},
		},
	}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})

	res, err := svc.List(identityContext("client-a"), &pb.ListRequest{SessionId: "s1", Path: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	if backend.lsPath != "/work/docs" {
		t.Fatalf("ls path=%q", backend.lsPath)
	}
	if len(res.GetFiles()) != 2 {
		t.Fatalf("file count=%d files=%+v", len(res.GetFiles()), res.GetFiles())
	}
	if res.GetFiles()[0].GetName() != "report.txt" || res.GetFiles()[1].GetType() != "directory" {
		t.Fatalf("unexpected files: %+v", res.GetFiles())
	}
}

func TestListRPCRootPathsUseSessionRoot(t *testing.T) {
	for _, inputPath := range []string{"", "."} {
		t.Run("path "+inputPath, func(t *testing.T) {
			backend := &filesystemRPCBackend{}
			svc := newTestSidecarService(t, nil, []openSessionInput{
				testOpenInputWithBackend("client-a", "s1", backend),
			})

			if _, err := svc.List(identityContext("client-a"), &pb.ListRequest{SessionId: "s1", Path: inputPath}); err != nil {
				t.Fatal(err)
			}
			if backend.lsPath != "/work" {
				t.Fatalf("ls path=%q", backend.lsPath)
			}
		})
	}
}

func TestStatRPCReturnsFileInfo(t *testing.T) {
	mtime := time.Unix(1_700_000_123, 456_000_000)
	backend := &filesystemRPCBackend{
		statInfo: fakeInfo{name: "report.txt", size: 99, mod: mtime, mode: 0o600},
	}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})

	res, err := svc.Stat(identityContext("client-a"), &pb.StatRequest{SessionId: "s1", Path: "docs/report.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if backend.statPath != "/work/docs/report.txt" {
		t.Fatalf("stat path=%q", backend.statPath)
	}
	if res.GetFile().GetName() != "report.txt" ||
		res.GetFile().GetType() != "file" ||
		res.GetFile().GetSize() != 99 ||
		res.GetFile().GetModTimeUnixMs() != unixMillis(mtime) {
		t.Fatalf("unexpected file info: %+v", res.GetFile())
	}
}

func TestStatRPCNilFileInfoReturnsGrpcError(t *testing.T) {
	backend := &filesystemRPCBackend{statNil: true}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})

	_, err := svc.Stat(identityContext("client-a"), &pb.StatRequest{SessionId: "s1", Path: "missing"})
	if status.Code(err) == codes.OK {
		t.Fatal("expected gRPC error")
	}
}

func TestMkdirRejectsReadOnlySession(t *testing.T) {
	backend := &filesystemRPCBackend{}
	input := testOpenInputWithBackend("client-a", "s1", backend)
	input.mode = pb.AccessMode_ACCESS_MODE_READ
	svc := newTestSidecarService(t, nil, []openSessionInput{input})

	_, err := svc.Mkdir(identityContext("client-a"), &pb.MkdirRequest{SessionId: "s1", Path: "docs"})
	if status.Code(err) != codes.PermissionDenied && status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.mkdirPath != "" {
		t.Fatalf("mkdir was called with path=%q", backend.mkdirPath)
	}
}

func TestRemoveRejectsMutatingRootPath(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})

	_, err := svc.Remove(identityContext("client-a"), &pb.RemoveRequest{SessionId: "s1", Path: "."})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.rmPath != "" {
		t.Fatalf("rm was called with path=%q", backend.rmPath)
	}
}

func TestRenameRejectsTraversalDestination(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})

	_, err := svc.Rename(identityContext("client-a"), &pb.RenameRequest{SessionId: "s1", From: "docs/report.txt", To: "../escape.txt"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.mvFrom != "" || backend.mvTo != "" {
		t.Fatalf("mv was called from=%q to=%q", backend.mvFrom, backend.mvTo)
	}
}

func TestListRejectsAbsoluteAndTraversalPathsAsInvalidArgument(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})

	for _, path := range []string{"/etc/passwd", "../escape"} {
		t.Run(path, func(t *testing.T) {
			_, err := svc.List(identityContext("client-a"), &pb.ListRequest{SessionId: "s1", Path: path})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("code=%s err=%v", status.Code(err), err)
			}
		})
	}
	if backend.lsPath != "" {
		t.Fatalf("ls was called with path=%q", backend.lsPath)
	}
}

func TestReadFileStreamsDataWithOffsetLimit(t *testing.T) {
	backend := &filesystemRPCBackend{catData: "0123456789"}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestReadFileStream(identityContext("client-a"))

	err := svc.ReadFile(&pb.ReadFileRequest{SessionId: "s1", Path: "data.bin", Offset: 3, Limit: 4}, stream)
	if err != nil {
		t.Fatal(err)
	}
	if backend.catPath != "/work/data.bin" {
		t.Fatalf("cat path=%q", backend.catPath)
	}
	if !backend.catClosed {
		t.Fatal("reader was not closed")
	}
	if got := stream.data(); got != "3456" {
		t.Fatalf("streamed data=%q", got)
	}
}

func TestReadFileLimitZeroExceedingConfiguredStreamLimitReturnsResourceExhausted(t *testing.T) {
	backend := &filesystemRPCBackend{catData: "hello"}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	svc.maxStreamBytes = 4
	stream := newTestReadFileStream(identityContext("client-a"))

	err := svc.ReadFile(&pb.ReadFileRequest{SessionId: "s1", Path: "data.bin", Limit: 0}, stream)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if got := stream.data(); got != "hell" {
		t.Fatalf("streamed data=%q", got)
	}
	if !backend.catClosed {
		t.Fatal("reader was not closed")
	}
}

func TestReadFileUserLimitBelowConfiguredStreamLimitSucceeds(t *testing.T) {
	backend := &filesystemRPCBackend{catData: "hello"}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	svc.maxStreamBytes = 4
	stream := newTestReadFileStream(identityContext("client-a"))

	if err := svc.ReadFile(&pb.ReadFileRequest{SessionId: "s1", Path: "data.bin", Limit: 3}, stream); err != nil {
		t.Fatal(err)
	}
	if got := stream.data(); got != "hel" {
		t.Fatalf("streamed data=%q", got)
	}
}

func TestReadFilePolicyStreamLimitOverridesGlobalLimit(t *testing.T) {
	oldMaxStreamBytes := PluginMaxStreamBytes
	PluginMaxStreamBytes = func() int64 { return 10 }
	t.Cleanup(func() { PluginMaxStreamBytes = oldMaxStreamBytes })

	backend := &filesystemRPCBackend{
		statInfo: fakeInfo{name: "work", isDir: true, mode: os.ModeDir | 0o755},
		catData:  "hello",
	}
	backendName := registerFilesystemOpenTestBackend(t, backend)
	svc := newOpenTestSidecarService([]string{"open-s1"}, &policyEngine{clients: []clientPolicy{{
		Identity:    "client-a",
		AccessModes: []string{"read"},
		Lease:       leaseJSON{DurationSeconds: 300, IdleTimeoutSeconds: 60, MaxLifetimeSeconds: 900},
		Limits:      limitsJSON{MaxStreamBytes: 4},
	}}})

	res, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{
		BackendType: backendName,
		RootPath:    "/work",
		Mode:        pb.AccessMode_ACCESS_MODE_READ,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := newTestReadFileStream(identityContext("client-a"))
	err = svc.ReadFile(&pb.ReadFileRequest{SessionId: res.GetSessionId(), Path: "data.bin"}, stream)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if got := stream.data(); got != "hell" {
		t.Fatalf("streamed data=%q", got)
	}
}

func TestReadFileNilReaderReturnsGrpcError(t *testing.T) {
	backend := &filesystemRPCBackend{catNil: true}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestReadFileStream(identityContext("client-a"))

	err := svc.ReadFile(&pb.ReadFileRequest{SessionId: "s1", Path: "data.bin"}, stream)
	if status.Code(err) == codes.OK {
		t.Fatal("expected gRPC error")
	}
}

func TestWriteFileWritesChunksAndReportsBytesWritten(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", int64(len("hello sidecar"))),
		writeFileData("hello "),
		writeFileData("sidecar"),
	})

	if err := svc.WriteFile(stream); err != nil {
		t.Fatal(err)
	}
	if backend.savePath != "/work/out.txt" {
		t.Fatalf("save path=%q", backend.savePath)
	}
	if got := backend.savedData; got != "hello sidecar" {
		t.Fatalf("saved data=%q", got)
	}
	if stream.closedResponse == nil || stream.closedResponse.GetBytesWritten() != int64(len("hello sidecar")) {
		t.Fatalf("unexpected response: %+v", stream.closedResponse)
	}
}

func TestWriteFileOverwriteFalseExistingTargetRejectsBeforeBodyAndSave(t *testing.T) {
	backend := &filesystemRPCBackend{statInfo: fakeInfo{name: "out.txt", size: 99}}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeaderOverwrite("s1", "out.txt", 0, false),
		writeFileData("hello"),
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
	if len(stream.requests) != 1 {
		t.Fatalf("body messages consumed=%d want 0", 1-len(stream.requests))
	}
}

func TestWriteFileOverwriteTrueAllowsSaveWhenTargetExists(t *testing.T) {
	backend := &filesystemRPCBackend{statInfo: fakeInfo{name: "out.txt", size: 99}}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeaderOverwrite("s1", "out.txt", int64(len("hello")), true),
		writeFileData("hello"),
	})

	if err := svc.WriteFile(stream); err != nil {
		t.Fatal(err)
	}
	if backend.savePath != "/work/out.txt" {
		t.Fatalf("save path=%q", backend.savePath)
	}
	if backend.savedData != "hello" {
		t.Fatalf("saved data=%q", backend.savedData)
	}
}

func TestWriteFilePolicyStreamLimitOverridesGlobalLimit(t *testing.T) {
	oldMaxStreamBytes := PluginMaxStreamBytes
	PluginMaxStreamBytes = func() int64 { return 10 }
	t.Cleanup(func() { PluginMaxStreamBytes = oldMaxStreamBytes })

	backend := &filesystemRPCBackend{
		statInfo: fakeInfo{name: "work", isDir: true, mode: os.ModeDir | 0o755},
	}
	backendName := registerFilesystemOpenTestBackend(t, backend)
	svc := newOpenTestSidecarService([]string{"open-s1"}, &policyEngine{clients: []clientPolicy{{
		Identity:    "client-a",
		AccessModes: []string{"read", "write"},
		Lease:       leaseJSON{DurationSeconds: 300, IdleTimeoutSeconds: 60, MaxLifetimeSeconds: 900},
		Limits:      limitsJSON{MaxStreamBytes: 4},
	}}})

	res, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{
		BackendType: backendName,
		RootPath:    "/work",
		Mode:        pb.AccessMode_ACCESS_MODE_READ_WRITE,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeaderOverwrite(res.GetSessionId(), "out.txt", 0, true),
		writeFileData("hello"),
	})
	err = svc.WriteFile(stream)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestWriteFileNegativeExpectedSizeRejectsInvalidArgument(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", -1),
		writeFileData("hello"),
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestWriteFileExpectedSizeTooSmallRejectsBeforeSave(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", 3),
		writeFileData("hello"),
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestWriteFileExpectedSizeTooLargeRejectsBeforeSave(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", 8),
		writeFileData("hello"),
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestWriteFileSaveEarlyFailurePreservesBackendError(t *testing.T) {
	backend := &filesystemRPCBackend{saveErr: ErrFilesystemError}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", 0),
		writeFileData(strings.Repeat("x", 128*1024)),
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
}

func TestWriteFileSecondHeaderRejectsInvalidArgument(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", 0),
		writeFileHeader("s1", "other.txt", 0),
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestWriteFileSecondHeaderAfterDataRejectsBeforeSave(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", 0),
		writeFileData("hello"),
		writeFileHeader("s1", "other.txt", 0),
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestWriteFileUnsetPayloadAfterHeaderRejectsInvalidArgument(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", 0),
		{},
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestWriteFileUnsetPayloadAfterDataRejectsBeforeSave(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", 0),
		writeFileData("hello"),
		{},
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestWriteFileRejectsWhenStreamLimitExceededBeforeSave(t *testing.T) {
	original := PluginMaxStreamBytes
	PluginMaxStreamBytes = func() int64 { return 4 }
	t.Cleanup(func() { PluginMaxStreamBytes = original })

	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", 0),
		writeFileData("hello"),
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestWriteFileUsesCachedStreamLimit(t *testing.T) {
	original := PluginMaxStreamBytes
	calls := 0
	PluginMaxStreamBytes = func() int64 {
		calls++
		return 4
	}
	t.Cleanup(func() { PluginMaxStreamBytes = original })

	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	if calls != 1 {
		t.Fatalf("PluginMaxStreamBytes calls=%d", calls)
	}
	PluginMaxStreamBytes = func() int64 { panic("PluginMaxStreamBytes called after service construction") }

	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", 0),
		writeFileData("hello"),
	})
	err := svc.WriteFile(stream)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestWriteFileMissingHeaderRejectsInvalidArgument(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileData("hello"),
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestFilesystemOperationRequiresOwner(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})

	_, err := svc.List(identityContext("client-b"), &pb.ListRequest{SessionId: "s1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.lsPath != "" {
		t.Fatalf("ls was called with path=%q", backend.lsPath)
	}
}

func TestFilesystemOperationCloseWaitsForInFlightOperation(t *testing.T) {
	backend := &filesystemRPCBackend{
		lsStarted:   make(chan struct{}),
		releaseLs:   make(chan struct{}),
		closeCalled: make(chan struct{}),
	}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})

	listDone := make(chan error, 1)
	go func() {
		_, err := svc.List(identityContext("client-a"), &pb.ListRequest{SessionId: "s1", Path: "."})
		listDone <- err
	}()
	waitOpenTestInitStarted(t, backend.lsStarted)

	closeDone := make(chan error, 1)
	go func() {
		_, err := svc.Close(identityContext("client-a"), &pb.CloseSessionRequest{SessionId: "s1"})
		closeDone <- err
	}()
	waitOpenTestEventually(t, func() bool {
		session := svc.sessionManager.sessions["s1"]
		session.mu.Lock()
		defer session.mu.Unlock()
		return session.state == pb.SessionState_SESSION_STATE_CLOSED
	}, "session close state")

	select {
	case <-backend.closeCalled:
		t.Fatal("backend closed before in-flight list completed")
	default:
	}
	close(backend.releaseLs)
	if err := <-listDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if backend.closed != 1 {
		t.Fatalf("closed=%d", backend.closed)
	}
}

func TestFilesystemOperationOnClosedSessionIsFailedPrecondition(t *testing.T) {
	svc := newTestSidecarService(t, nil, []openSessionInput{testOpenInput("client-a", "s1")})
	if _, err := svc.Close(identityContext("client-a"), &pb.CloseSessionRequest{SessionId: "s1"}); err != nil {
		t.Fatal(err)
	}

	_, err := svc.List(identityContext("client-a"), &pb.ListRequest{SessionId: "s1"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
}

func TestFilesystemOperationMarksSessionUsedOnSuccess(t *testing.T) {
	backend := &filesystemRPCBackend{statInfo: fakeInfo{name: "report.txt"}}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	before := svc.sessionManager.sessions["s1"].lastUsedAt
	later := before.Add(2 * time.Minute)
	svc.sessionManager.now = func() time.Time { return later }

	if _, err := svc.Stat(identityContext("client-a"), &pb.StatRequest{SessionId: "s1", Path: "report.txt"}); err != nil {
		t.Fatal(err)
	}
	session := svc.sessionManager.sessions["s1"]
	if !session.lastUsedAt.Equal(later) {
		t.Fatalf("last_used_at=%s want=%s", session.lastUsedAt, later)
	}
	if want := later.Add(5 * time.Minute); !session.idleExpiresAt.Equal(want) {
		t.Fatalf("idle_expires_at=%s want=%s", session.idleExpiresAt, want)
	}
}

func newTestSidecarService(t *testing.T, policies *policyEngine, inputs []openSessionInput) *sidecarService {
	t.Helper()
	now := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	ids := make([]string, 0, len(inputs))
	for _, in := range inputs {
		ids = append(ids, in.externalRef.id[len("job-"):])
	}
	nextID := 0
	m := newSessionManager(sessionManagerOptions{
		now: func() time.Time { return now },
		id: func() (string, error) {
			id := ids[nextID]
			nextID++
			return id, nil
		},
	})
	for _, in := range inputs {
		if _, err := m.open(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	return &sidecarService{
		sessionManager: m,
		policies:       policies,
		maxSessions:    PluginMaxSessions(),
		maxStreamBytes: PluginMaxStreamBytes(),
	}
}

var openTestNow = time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)

func newOpenTestSidecarService(ids []string, policies *policyEngine) *sidecarService {
	nextID := 0
	m := newSessionManager(sessionManagerOptions{
		now: func() time.Time { return openTestNow },
		id: func() (string, error) {
			id := ids[nextID]
			nextID++
			return id, nil
		},
	})
	return &sidecarService{
		sessionManager: m,
		policies:       policies,
		maxSessions:    PluginMaxSessions(),
		maxStreamBytes: PluginMaxStreamBytes(),
	}
}

func testOpenPolicy(identities ...string) *policyEngine {
	clients := make([]clientPolicy, 0, len(identities))
	for _, identity := range identities {
		clients = append(clients, clientPolicy{
			Identity:    identity,
			AccessModes: []string{"read", "write"},
			Lease: leaseJSON{
				DurationSeconds:    900,
				IdleTimeoutSeconds: 300,
				MaxLifetimeSeconds: 3600,
				Renewable:          true,
			},
		})
	}
	return &policyEngine{clients: clients}
}

func uniqueOpenBackendName(t *testing.T) string {
	t.Helper()
	replacer := strings.NewReplacer("/", "_", " ", "_")
	return "test-open-" + replacer.Replace(t.Name())
}

func registerOpenTestBackend(t *testing.T, driver *openTestDriver) *openTestDriver {
	t.Helper()
	return registerNamedOpenTestBackend(t, uniqueOpenBackendName(t), driver)
}

func registerNamedOpenTestBackend(t *testing.T, name string, driver *openTestDriver) *openTestDriver {
	t.Helper()
	if driver == nil {
		driver = &openTestDriver{}
	}
	driver.name = name
	if driver.handle == nil {
		driver.handle = &openTestBackend{statInfo: fakeInfo{name: "work", isDir: true, mode: os.ModeDir | 0o755}}
	}
	Backend.Register(driver.name, driver)
	return driver
}

func registerFilesystemOpenTestBackend(t *testing.T, backend *filesystemRPCBackend) string {
	t.Helper()
	name := uniqueOpenBackendName(t)
	Backend.Register(name, &filesystemOpenTestDriver{backend: backend})
	return name
}

type filesystemOpenTestDriver struct {
	Nothing

	backend *filesystemRPCBackend
	initCtx context.Context
}

func (d *filesystemOpenTestDriver) Init(_ map[string]string, app *App) (IBackend, error) {
	if app == nil || app.Context == nil {
		return nil, ErrInternal
	}
	d.initCtx = app.Context
	return d.backend, nil
}

type openTestDriver struct {
	Nothing

	name         string
	handle       *openTestBackend
	initErr      error
	initCalls    int
	initParams   map[string]string
	initCtx      context.Context
	mutateParams bool
	initStarted  chan struct{}
	releaseInit  chan struct{}
	mu           sync.Mutex
}

func (d *openTestDriver) Init(params map[string]string, app *App) (IBackend, error) {
	d.mu.Lock()
	d.initCalls++
	d.initParams = copyTestStringMap(params)
	if app == nil || app.Context == nil {
		d.mu.Unlock()
		return nil, ErrInternal
	}
	d.initCtx = app.Context
	initStarted := d.initStarted
	releaseInit := d.releaseInit
	d.mu.Unlock()

	if initStarted != nil {
		initStarted <- struct{}{}
	}
	if releaseInit != nil {
		<-releaseInit
	}

	if d.mutateParams {
		params["mutated"] = "yes"
	}
	if d.initErr != nil {
		return nil, d.initErr
	}
	return d.handle, nil
}

func (d *openTestDriver) initCallCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.initCalls
}

func waitOpenTestInitStarted(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for backend init")
	}
}

func waitOpenTestEventually(t *testing.T, ok func() bool, label string) {
	t.Helper()
	deadline := time.After(time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if ok() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", label)
		case <-tick.C:
		}
	}
}

func copyTestStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type openTestBackend struct {
	Nothing

	statPath string
	statInfo os.FileInfo
	statErr  error
	statNil  bool

	lsPath    string
	lsEntries []os.FileInfo
	lsErr     error

	closed int
	mu     sync.Mutex
}

func (b *openTestBackend) Stat(path string) (os.FileInfo, error) {
	b.statPath = path
	if b.statErr != nil {
		return nil, b.statErr
	}
	if b.statNil {
		return nil, nil
	}
	return b.statInfo, nil
}

func (b *openTestBackend) Ls(path string) ([]os.FileInfo, error) {
	b.lsPath = path
	if b.lsErr != nil {
		return nil, b.lsErr
	}
	return b.lsEntries, nil
}

func (b *openTestBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed++
	return nil
}

func (b *openTestBackend) closedCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

func assertSafeRedactedTarget(t *testing.T, got, wantContains string) {
	t.Helper()
	if !strings.Contains(got, wantContains) {
		t.Fatalf("redacted_target=%q does not contain %q", got, wantContains)
	}
	for _, secret := range []string{"secret-pass", "secret-token", "secret-value", "grant-value", "refresh-value"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted_target leaked %q: %q", secret, got)
		}
	}
}

func testOpenInput(owner, id string) openSessionInput {
	return testOpenInputWithBackend(owner, id, &closeTrackingBackend{})
}

func testOpenInputWithBackend(owner, id string, backend IBackend) openSessionInput {
	return openSessionInput{
		ownerIdentity: owner,
		backendType:   "sftp",
		backend:       backend,
		rootPath:      "/work",
		mode:          pb.AccessMode_ACCESS_MODE_READ_WRITE,
		externalRef:   externalRef{kind: "job", id: "job-" + id},
		lease: effectiveLease{
			duration:    15 * time.Minute,
			idleTimeout: 5 * time.Minute,
			maxLifetime: time.Hour,
			renewable:   true,
		},
	}
}

type filesystemRPCBackend struct {
	Nothing

	lsPath    string
	lsEntries []os.FileInfo
	lsStarted chan struct{}
	releaseLs chan struct{}

	statPath string
	statInfo os.FileInfo
	statNil  bool

	catPath   string
	catData   string
	catNil    bool
	catClosed bool

	mkdirPath string
	rmPath    string
	mvFrom    string
	mvTo      string
	touchPath string
	savePath  string
	savedData string
	saveErr   error

	closeCalled chan struct{}
	closed      int
}

func (b *filesystemRPCBackend) Ls(path string) ([]os.FileInfo, error) {
	b.lsPath = path
	if b.lsStarted != nil {
		close(b.lsStarted)
	}
	if b.releaseLs != nil {
		<-b.releaseLs
	}
	return b.lsEntries, nil
}

func (b *filesystemRPCBackend) Stat(path string) (os.FileInfo, error) {
	b.statPath = path
	if b.statNil {
		return nil, nil
	}
	return b.statInfo, nil
}

func (b *filesystemRPCBackend) Cat(path string) (io.ReadCloser, error) {
	b.catPath = path
	if b.catNil {
		return nil, nil
	}
	return &closeTrackingReadCloser{
		reader: strings.NewReader(b.catData),
		close:  func() { b.catClosed = true },
	}, nil
}

func (b *filesystemRPCBackend) Mkdir(path string) error {
	b.mkdirPath = path
	return nil
}

func (b *filesystemRPCBackend) Rm(path string) error {
	b.rmPath = path
	return nil
}

func (b *filesystemRPCBackend) Mv(from string, to string) error {
	b.mvFrom = from
	b.mvTo = to
	return nil
}

func (b *filesystemRPCBackend) Touch(path string) error {
	b.touchPath = path
	return nil
}

func (b *filesystemRPCBackend) Save(path string, file io.Reader) error {
	b.savePath = path
	if b.saveErr != nil {
		return b.saveErr
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	b.savedData = string(data)
	return nil
}

func (b *filesystemRPCBackend) Close() error {
	b.closed++
	if b.closeCalled != nil {
		close(b.closeCalled)
	}
	return nil
}

type closeErrorBackend struct {
	Nothing
	closed int
}

func (b *closeErrorBackend) Close() error {
	b.closed++
	return errors.New("close failed")
}

type closeTrackingReadCloser struct {
	reader io.Reader
	close  func()
}

func (r *closeTrackingReadCloser) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r *closeTrackingReadCloser) Close() error {
	if r.close != nil {
		r.close()
	}
	return nil
}

type testReadFileStream struct {
	ctx    context.Context
	chunks [][]byte
}

func newTestReadFileStream(ctx context.Context) *testReadFileStream {
	return &testReadFileStream{ctx: ctx}
}

func (s *testReadFileStream) Send(chunk *pb.ReadFileChunk) error {
	s.chunks = append(s.chunks, append([]byte(nil), chunk.GetData()...))
	return nil
}

func (s *testReadFileStream) data() string {
	var out []byte
	for _, chunk := range s.chunks {
		out = append(out, chunk...)
	}
	return string(out)
}

func (s *testReadFileStream) SetHeader(metadata.MD) error {
	return nil
}

func (s *testReadFileStream) SendHeader(metadata.MD) error {
	return nil
}

func (s *testReadFileStream) SetTrailer(metadata.MD) {}

func (s *testReadFileStream) Context() context.Context {
	return s.ctx
}

func (s *testReadFileStream) SendMsg(any) error {
	return nil
}

func (s *testReadFileStream) RecvMsg(any) error {
	return io.EOF
}

type testWriteFileStream struct {
	ctx            context.Context
	requests       []*pb.WriteFileRequest
	closedResponse *pb.WriteFileResponse
}

func newTestWriteFileStream(ctx context.Context, requests []*pb.WriteFileRequest) *testWriteFileStream {
	return &testWriteFileStream{ctx: ctx, requests: requests}
}

func (s *testWriteFileStream) Recv() (*pb.WriteFileRequest, error) {
	if len(s.requests) == 0 {
		return nil, io.EOF
	}
	req := s.requests[0]
	s.requests = s.requests[1:]
	return req, nil
}

func (s *testWriteFileStream) SendAndClose(res *pb.WriteFileResponse) error {
	s.closedResponse = res
	return nil
}

func (s *testWriteFileStream) SetHeader(metadata.MD) error {
	return nil
}

func (s *testWriteFileStream) SendHeader(metadata.MD) error {
	return nil
}

func (s *testWriteFileStream) SetTrailer(metadata.MD) {}

func (s *testWriteFileStream) Context() context.Context {
	return s.ctx
}

func (s *testWriteFileStream) SendMsg(any) error {
	return nil
}

func (s *testWriteFileStream) RecvMsg(any) error {
	return io.EOF
}

func writeFileHeader(sessionID, path string, expectedSize int64) *pb.WriteFileRequest {
	return writeFileHeaderOverwrite(sessionID, path, expectedSize, false)
}

func writeFileHeaderOverwrite(sessionID, path string, expectedSize int64, overwrite bool) *pb.WriteFileRequest {
	return &pb.WriteFileRequest{
		Payload: &pb.WriteFileRequest_Header{
			Header: &pb.WriteFileHeader{
				SessionId:    sessionID,
				Path:         path,
				Overwrite:    overwrite,
				ExpectedSize: expectedSize,
			},
		},
	}
}

func writeFileData(data string) *pb.WriteFileRequest {
	return &pb.WriteFileRequest{
		Payload: &pb.WriteFileRequest_Data{Data: []byte(data)},
	}
}

func identityContext(identity string) context.Context {
	cert := &x509.Certificate{Subject: pkix.Name{CommonName: identity}}
	if u, err := url.Parse(identity); err == nil && u.Scheme != "" {
		cert.URIs = []*url.URL{u}
	}
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}},
	})
}

func testPolicyEngine(t *testing.T, clients map[string]string) *policyEngine {
	t.Helper()
	engine := &policyEngine{}
	for identity, role := range clients {
		engine.clients = append(engine.clients, clientPolicy{Identity: identity, Role: role})
	}
	return engine
}

func assertSessionIDs(t *testing.T, sessions []*pb.SessionInfo, want []string) {
	t.Helper()
	if len(sessions) != len(want) {
		t.Fatalf("session count=%d want=%d sessions=%+v", len(sessions), len(want), sessions)
	}
	got := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		got[s.GetSessionId()] = true
	}
	for _, id := range want {
		if !got[id] {
			t.Fatalf("missing session %q in %+v", id, sessions)
		}
	}
}
