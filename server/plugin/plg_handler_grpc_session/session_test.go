package plg_handler_grpc_session

import (
	"context"
	"testing"
	"time"

	. "github.com/mickael-kerjean/filestash/server/common"
	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
)

type closeTrackingBackend struct {
	Nothing
	closed int
}

func (b *closeTrackingBackend) Close() error {
	b.closed++
	return nil
}

func TestSessionRenewClampsToMaxLifetime(t *testing.T) {
	now := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	m := newSessionManager(sessionManagerOptions{
		now: func() time.Time { return now },
		id:  func() (string, error) { return "s1", nil },
	})
	backend := &closeTrackingBackend{}
	s, err := m.open(context.Background(), openSessionInput{
		ownerIdentity: "client-a",
		backendType:   "sftp",
		backend:       backend,
		rootPath:      "/work",
		mode:          pb.AccessMode_ACCESS_MODE_READ_WRITE,
		lease: effectiveLease{
			duration:    15 * time.Minute,
			idleTimeout: 15 * time.Minute,
			maxLifetime: 20 * time.Minute,
			renewable:   true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	lease, err := m.renew("client-a", false, s.id)
	if err != nil {
		t.Fatal(err)
	}
	if !lease.expiresAt.Equal(s.openedAt.Add(20 * time.Minute)) {
		t.Fatalf("expiresAt=%s", lease.expiresAt)
	}
}

func TestSessionRenewRejectsNonOwner(t *testing.T) {
	m := newSessionManager(sessionManagerOptions{id: func() (string, error) { return "s1", nil }})
	_, err := m.open(context.Background(), openSessionInput{
		ownerIdentity: "client-a",
		backendType:   "sftp",
		backend:       &closeTrackingBackend{},
		rootPath:      "/work",
		mode:          pb.AccessMode_ACCESS_MODE_READ,
		lease: effectiveLease{
			duration:    time.Minute,
			idleTimeout: time.Minute,
			maxLifetime: time.Hour,
			renewable:   true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.renew("client-b", false, "s1"); err == nil {
		t.Fatal("expected non-owner renewal to fail")
	}
}

func TestSessionRenewRejectsIdleExpiredSession(t *testing.T) {
	now := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	m := newSessionManager(sessionManagerOptions{
		now: func() time.Time { return now },
		id:  func() (string, error) { return "s1", nil },
	})
	_, err := m.open(context.Background(), openSessionInput{
		ownerIdentity: "client-a",
		backendType:   "sftp",
		backend:       &closeTrackingBackend{},
		rootPath:      "/work",
		mode:          pb.AccessMode_ACCESS_MODE_READ,
		lease: effectiveLease{
			duration:    10 * time.Minute,
			idleTimeout: time.Minute,
			maxLifetime: time.Hour,
			renewable:   true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := m.renew("client-a", false, "s1"); err == nil {
		t.Fatal("expected idle-expired renewal to fail")
	}
}

func TestSessionCloseIsIdempotentAndClosesBackend(t *testing.T) {
	m := newSessionManager(sessionManagerOptions{id: func() (string, error) { return "s1", nil }})
	backend := &closeTrackingBackend{}
	_, err := m.open(context.Background(), openSessionInput{
		ownerIdentity: "client-a",
		backendType:   "local",
		backend:       backend,
		rootPath:      "/work",
		mode:          pb.AccessMode_ACCESS_MODE_READ,
		lease: effectiveLease{
			duration:    time.Minute,
			idleTimeout: time.Minute,
			maxLifetime: time.Hour,
			renewable:   true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.close("client-a", false, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := m.close("client-a", false, "s1"); err != nil {
		t.Fatal(err)
	}
	if backend.closed != 1 {
		t.Fatalf("closed=%d", backend.closed)
	}
}

func TestSidecarJanitorScanExpiresSessionsWithoutRPC(t *testing.T) {
	now := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	ids := []string{"ttl", "idle", "max"}
	nextID := 0
	m := newSessionManager(sessionManagerOptions{
		now: func() time.Time { return now },
		id: func() (string, error) {
			id := ids[nextID]
			nextID++
			return id, nil
		},
	})

	type opened struct {
		session *sidecarSession
		backend *closeTrackingBackend
		ctx     context.Context
	}
	openedSessions := make([]opened, 0, len(ids))
	for _, tc := range []struct {
		name  string
		lease effectiveLease
	}{
		{
			name: "ttl",
			lease: effectiveLease{
				duration:    time.Minute,
				idleTimeout: time.Hour,
				maxLifetime: time.Hour,
			},
		},
		{
			name: "idle",
			lease: effectiveLease{
				duration:    time.Hour,
				idleTimeout: time.Minute,
				maxLifetime: time.Hour,
			},
		},
		{
			name: "max",
			lease: effectiveLease{
				duration:    time.Hour,
				idleTimeout: time.Hour,
				maxLifetime: time.Minute,
			},
		},
	} {
		backend := &closeTrackingBackend{}
		ctx, cancel := context.WithCancel(context.Background())
		session, err := m.open(context.Background(), openSessionInput{
			ownerIdentity: "client-a",
			backendType:   "local",
			backend:       backend,
			rootPath:      "/work",
			mode:          pb.AccessMode_ACCESS_MODE_READ,
			lease:         tc.lease,
			ctx:           ctx,
			cancel:        cancel,
		})
		if err != nil {
			t.Fatalf("%s open: %v", tc.name, err)
		}
		openedSessions = append(openedSessions, opened{session: session, backend: backend, ctx: ctx})
	}

	now = now.Add(time.Minute)
	if expired := m.scanExpiredSessions(); expired != len(openedSessions) {
		t.Fatalf("expired=%d want=%d", expired, len(openedSessions))
	}
	for _, opened := range openedSessions {
		if opened.backend.closed != 1 {
			t.Fatalf("%s closed=%d", opened.session.id, opened.backend.closed)
		}
		if opened.ctx.Err() == nil {
			t.Fatalf("%s context was not canceled", opened.session.id)
		}
		opened.session.mu.Lock()
		state := opened.session.state
		opened.session.mu.Unlock()
		if state != pb.SessionState_SESSION_STATE_EXPIRED {
			t.Fatalf("%s state=%s", opened.session.id, state)
		}
	}

	if expired := m.scanExpiredSessions(); expired != 0 {
		t.Fatalf("second scan expired=%d", expired)
	}
	for _, opened := range openedSessions {
		if opened.backend.closed != 1 {
			t.Fatalf("%s closed after second scan=%d", opened.session.id, opened.backend.closed)
		}
	}
}
