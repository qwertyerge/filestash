package plg_handler_grpc_session

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"net/url"
	"testing"
	"time"

	. "github.com/mickael-kerjean/filestash/server/common"
	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("code=%s err=%v", status.Code(err), err)
			}
		})
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
	return &sidecarService{sessionManager: m, policies: policies}
}

func testOpenInput(owner, id string) openSessionInput {
	return testOpenInputWithBackend(owner, id, &closeTrackingBackend{})
}

func testOpenInputWithBackend(owner, id string, backend *closeTrackingBackend) openSessionInput {
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
