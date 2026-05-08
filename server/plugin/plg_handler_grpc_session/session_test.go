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
