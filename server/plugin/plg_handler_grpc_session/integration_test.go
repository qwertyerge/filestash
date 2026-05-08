package plg_handler_grpc_session

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mickael-kerjean/filestash/server/common"
	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
)

func TestSidecarServiceSmokeOpenRenewClose(t *testing.T) {
	now := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	sessions := newSessionManager(sessionManagerOptions{
		now: func() time.Time { return now },
		id:  func() (string, error) { return "smoke-s1", nil },
	})
	policies := &policyEngine{clients: []clientPolicy{{
		Identity:    "client-a",
		AccessModes: []string{"read", "write"},
		Lease: leaseJSON{
			DurationSeconds:    900,
			IdleTimeoutSeconds: 300,
			MaxLifetimeSeconds: 3600,
			Renewable:          true,
		},
	}}}
	svc := &sidecarService{
		sessionManager: sessions,
		policies:       policies,
		maxSessions:    10,
		maxStreamBytes: 1 << 20,
	}

	backend := &smokeBackend{}
	driver := &smokeBackendDriver{backend: backend}
	backendName := uniqueSmokeBackendName(t)
	common.Backend.Register(backendName, driver)

	openRes, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{
		BackendType: backendName,
		RootPath:    "/work",
		Mode:        pb.AccessMode_ACCESS_MODE_READ_WRITE,
		Lease: &pb.LeaseOptions{
			DurationSeconds:    600,
			IdleTimeoutSeconds: 120,
			MaxLifetimeSeconds: 1800,
			Renewable:          true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if openRes.GetSessionId() != "smoke-s1" {
		t.Fatalf("session_id=%q", openRes.GetSessionId())
	}
	if openRes.GetEffectiveMode() != pb.AccessMode_ACCESS_MODE_READ_WRITE {
		t.Fatalf("effective_mode=%s", openRes.GetEffectiveMode())
	}
	if !openRes.GetLease().GetRenewable() {
		t.Fatalf("expected renewable lease: %+v", openRes.GetLease())
	}
	if got := backend.statPath(); got != "/work" {
		t.Fatalf("stat path=%q", got)
	}

	now = now.Add(time.Minute)
	renewed, err := svc.RenewSession(identityContext("client-a"), &pb.RenewSessionRequest{SessionId: openRes.GetSessionId()})
	if err != nil {
		t.Fatal(err)
	}
	if renewed.GetSessionId() != openRes.GetSessionId() || !renewed.GetRenewable() {
		t.Fatalf("unexpected renewed lease: %+v", renewed)
	}
	if got, want := renewed.GetExpiresAt().AsTime(), now.Add(10*time.Minute); !got.Equal(want) {
		t.Fatalf("renewed expires_at=%s want=%s", got, want)
	}

	closed, err := svc.Close(identityContext("client-a"), &pb.CloseSessionRequest{SessionId: openRes.GetSessionId()})
	if err != nil {
		t.Fatal(err)
	}
	if closed.GetSessionId() != openRes.GetSessionId() || closed.GetState() != pb.SessionState_SESSION_STATE_CLOSED {
		t.Fatalf("unexpected close response: %+v", closed)
	}
	if got := backend.closedCount(); got != 1 {
		t.Fatalf("backend closed=%d", got)
	}
	if ctx := driver.context(); ctx == nil {
		t.Fatal("backend init did not receive session context")
	} else {
		select {
		case <-ctx.Done():
		default:
			t.Fatal("backend session context was not canceled on close")
		}
	}
}

func uniqueSmokeBackendName(t *testing.T) string {
	t.Helper()
	replacer := strings.NewReplacer("/", "_", " ", "_")
	return "test-sidecar-smoke-" + replacer.Replace(t.Name())
}

type smokeBackendDriver struct {
	common.Nothing

	backend *smokeBackend
	initCtx context.Context
	mu      sync.Mutex
}

func (d *smokeBackendDriver) Init(_ map[string]string, app *common.App) (common.IBackend, error) {
	if app == nil || app.Context == nil {
		return nil, common.ErrInternal
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.initCtx = app.Context
	if d.backend == nil {
		d.backend = &smokeBackend{}
	}
	return d.backend, nil
}

func (d *smokeBackendDriver) context() context.Context {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.initCtx
}

type smokeBackend struct {
	common.Nothing

	mu       sync.Mutex
	statSeen string
	closed   int
}

func (b *smokeBackend) Stat(path string) (os.FileInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.statSeen = path
	return smokeFileInfo{name: "work", isDir: true, mode: os.ModeDir | 0o755}, nil
}

func (b *smokeBackend) Ls(path string) ([]os.FileInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return []os.FileInfo{smokeFileInfo{name: "work", isDir: true, mode: os.ModeDir | 0o755}}, nil
}

func (b *smokeBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed++
	return nil
}

func (b *smokeBackend) statPath() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.statSeen
}

func (b *smokeBackend) closedCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

type smokeFileInfo struct {
	name  string
	isDir bool
	mode  os.FileMode
}

func (f smokeFileInfo) Name() string       { return f.name }
func (f smokeFileInfo) Size() int64        { return 0 }
func (f smokeFileInfo) Mode() os.FileMode  { return f.mode }
func (f smokeFileInfo) ModTime() time.Time { return time.Time{} }
func (f smokeFileInfo) IsDir() bool        { return f.isDir }
func (f smokeFileInfo) Sys() any           { return nil }
