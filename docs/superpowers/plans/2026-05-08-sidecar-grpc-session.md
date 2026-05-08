# Sidecar gRPC Session Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build an in-process Filestash plugin that exposes a TCP+mTLS gRPC service for one-time backend sessions with filesystem operations and operator session management.

**Architecture:** Add `server/plugin/plg_handler_grpc_session` as a focused plugin. Generated protobuf code defines the public contract, while small Go files own config, policy, path resolution, session lifecycle, backend adaptation, gRPC handlers, and server startup.

**Tech Stack:** Go 1.26, `google.golang.org/grpc`, `google.golang.org/protobuf`, Filestash `IBackend`, Filestash plugin hooks, mTLS via Go `crypto/tls`.

---

## File Structure

- Create `server/plugin/plg_handler_grpc_session/pb/sidecar.proto`: public gRPC contract.
- Generate `server/plugin/plg_handler_grpc_session/pb/sidecar.pb.go`: protobuf messages.
- Generate `server/plugin/plg_handler_grpc_session/pb/sidecar_grpc.pb.go`: gRPC client/server interfaces.
- Create `server/plugin/plg_handler_grpc_session/config.go`: `features.sidecar_grpc.*` config accessors and schema defaults.
- Create `server/plugin/plg_handler_grpc_session/policy.go`: mTLS identity, policy parsing, CIDR checks, lease clamping, access-mode clamping.
- Create `server/plugin/plg_handler_grpc_session/path.go`: chroot path resolver.
- Create `server/plugin/plg_handler_grpc_session/session.go`: session records, lease renewal, janitor, close semantics.
- Create `server/plugin/plg_handler_grpc_session/backend.go`: backend initialization, root verification, file info conversion, stream helpers, credential redaction.
- Create `server/plugin/plg_handler_grpc_session/errors.go`: Filestash/backend error to gRPC status mapping.
- Create `server/plugin/plg_handler_grpc_session/handler.go`: gRPC RPC implementation.
- Create `server/plugin/plg_handler_grpc_session/server.go`: TCP listener, mTLS setup, gRPC server lifecycle.
- Create `server/plugin/plg_handler_grpc_session/index.go`: plugin hook registration.
- Modify `server/plugin/index.go`: blank import the new plugin.
- Create focused tests next to each implementation file: `path_test.go`, `session_test.go`, `policy_test.go`, `backend_test.go`, `handler_test.go`, `server_test.go`.

---

### Task 1: Define the gRPC Contract

**Files:**
- Create: `server/plugin/plg_handler_grpc_session/pb/sidecar.proto`
- Generate: `server/plugin/plg_handler_grpc_session/pb/sidecar.pb.go`
- Generate: `server/plugin/plg_handler_grpc_session/pb/sidecar_grpc.pb.go`
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Create the proto contract**

Create `server/plugin/plg_handler_grpc_session/pb/sidecar.proto` with:

```proto
syntax = "proto3";

package filestash.sidecar.v1;

option go_package = "github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb;pb";

import "google/protobuf/timestamp.proto";

service FilestashSidecarService {
  rpc Open(OpenRequest) returns (OpenResponse);
  rpc Close(CloseSessionRequest) returns (CloseSessionResponse);
  rpc RenewSession(RenewSessionRequest) returns (SessionLease);
  rpc GetSession(GetSessionRequest) returns (SessionInfo);
  rpc ListSessions(ListSessionsRequest) returns (ListSessionsResponse);
  rpc ForceClose(ForceCloseRequest) returns (CloseSessionResponse);

  rpc List(ListRequest) returns (ListResponse);
  rpc Stat(StatRequest) returns (StatResponse);
  rpc ReadFile(ReadFileRequest) returns (stream ReadFileChunk);
  rpc WriteFile(stream WriteFileRequest) returns (WriteFileResponse);
  rpc Mkdir(MkdirRequest) returns (MutationResponse);
  rpc Remove(RemoveRequest) returns (MutationResponse);
  rpc Rename(RenameRequest) returns (MutationResponse);
  rpc Touch(TouchRequest) returns (MutationResponse);
}

enum AccessMode {
  ACCESS_MODE_UNSPECIFIED = 0;
  ACCESS_MODE_READ = 1;
  ACCESS_MODE_READ_WRITE = 2;
}

enum SessionState {
  SESSION_STATE_UNSPECIFIED = 0;
  SESSION_STATE_ACTIVE = 1;
  SESSION_STATE_CLOSED = 2;
  SESSION_STATE_EXPIRED = 3;
}

message LeaseOptions {
  uint32 duration_seconds = 1;
  uint32 idle_timeout_seconds = 2;
  uint32 max_lifetime_seconds = 3;
  bool renewable = 4;
}

message SessionLease {
  string session_id = 1;
  google.protobuf.Timestamp expires_at = 2;
  google.protobuf.Timestamp idle_expires_at = 3;
  google.protobuf.Timestamp max_expires_at = 4;
  bool renewable = 5;
}

message ExternalRef {
  string kind = 1;
  string id = 2;
}

message OpenRequest {
  string backend_type = 1;
  map<string, string> backend_params = 2;
  string root_path = 3;
  AccessMode mode = 4;
  LeaseOptions lease = 5;
  ExternalRef external_ref = 6;
  map<string, string> client_metadata = 7;
}

message OpenResponse {
  string session_id = 1;
  AccessMode effective_mode = 2;
  SessionLease lease = 3;
}

message CloseSessionRequest {
  string session_id = 1;
}

message ForceCloseRequest {
  string session_id = 1;
  string reason = 2;
}

message CloseSessionResponse {
  string session_id = 1;
  SessionState state = 2;
}

message RenewSessionRequest {
  string session_id = 1;
}

message GetSessionRequest {
  string session_id = 1;
}

message ListSessionsRequest {
  bool include_closed = 1;
}

message ListSessionsResponse {
  repeated SessionInfo sessions = 1;
}

message SessionInfo {
  string session_id = 1;
  string owner_identity = 2;
  string backend_type = 3;
  string redacted_target = 4;
  string root_path = 5;
  AccessMode mode = 6;
  SessionState state = 7;
  ExternalRef external_ref = 8;
  google.protobuf.Timestamp opened_at = 9;
  google.protobuf.Timestamp last_used_at = 10;
  SessionLease lease = 11;
}

message FileInfo {
  string name = 1;
  string type = 2;
  int64 size = 3;
  int64 mod_time_unix_ms = 4;
  uint32 mode = 5;
}

message ListRequest {
  string session_id = 1;
  string path = 2;
}

message ListResponse {
  repeated FileInfo files = 1;
}

message StatRequest {
  string session_id = 1;
  string path = 2;
}

message StatResponse {
  FileInfo file = 1;
}

message ReadFileRequest {
  string session_id = 1;
  string path = 2;
  int64 offset = 3;
  int64 limit = 4;
}

message ReadFileChunk {
  bytes data = 1;
}

message WriteFileRequest {
  oneof payload {
    WriteFileHeader header = 1;
    bytes data = 2;
  }
}

message WriteFileHeader {
  string session_id = 1;
  string path = 2;
  bool overwrite = 3;
  int64 expected_size = 4;
}

message WriteFileResponse {
  int64 bytes_written = 1;
}

message MkdirRequest {
  string session_id = 1;
  string path = 2;
}

message RemoveRequest {
  string session_id = 1;
  string path = 2;
}

message RenameRequest {
  string session_id = 1;
  string from = 2;
  string to = 3;
}

message TouchRequest {
  string session_id = 1;
  string path = 2;
}

message MutationResponse {
  bool ok = 1;
}
```

- [ ] **Step 2: Ensure protobuf generators exist**

Run:

```bash
command -v protoc
command -v protoc-gen-go || go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
command -v protoc-gen-go-grpc || go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
```

Expected: `protoc` prints an installed path. Missing Go generator binaries are installed under `$(go env GOPATH)/bin`.

- [ ] **Step 3: Generate Go protobuf files**

Run:

```bash
export PATH="$(go env GOPATH)/bin:$PATH"
protoc \
  --go_out=. \
  --go_opt=paths=source_relative \
  --go-grpc_out=. \
  --go-grpc_opt=paths=source_relative \
  server/plugin/plg_handler_grpc_session/pb/sidecar.proto
go get google.golang.org/grpc@v1.80.0 google.golang.org/protobuf@v1.36.11
go mod tidy
```

Expected: `sidecar.pb.go` and `sidecar_grpc.pb.go` are created, and `go.mod` keeps gRPC/protobuf dependencies available for direct imports.

- [ ] **Step 4: Verify generated code compiles**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session/pb
```

Expected: `? github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb [no test files]`.

- [ ] **Step 5: Commit**

Run:

```bash
git add go.mod go.sum server/plugin/plg_handler_grpc_session/pb
git commit -m "feat: add sidecar grpc contract"
```

---

### Task 2: Implement Chroot Path Resolution

**Files:**
- Create: `server/plugin/plg_handler_grpc_session/path.go`
- Create: `server/plugin/plg_handler_grpc_session/path_test.go`

- [ ] **Step 1: Write failing path resolver tests**

Create `server/plugin/plg_handler_grpc_session/path_test.go` with:

```go
package plg_handler_grpc_session

import "testing"

func TestResolvePathAllowsRootForRead(t *testing.T) {
	r, err := newPathResolver("/home/alice")
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.resolve(".", pathUseRead)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/home/alice" {
		t.Fatalf("got %q", got)
	}
}

func TestResolvePathRejectsRootForMutation(t *testing.T) {
	r, err := newPathResolver("/home/alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.resolve(".", pathUseMutateChild); err == nil {
		t.Fatal("expected root mutation to fail")
	}
}

func TestResolvePathRejectsEscape(t *testing.T) {
	r, err := newPathResolver("/home/alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"../bob", "docs/../../bob", "/etc/passwd"} {
		if _, err := r.resolve(input, pathUseRead); err == nil {
			t.Fatalf("expected %q to fail", input)
		}
	}
}

func TestResolvePathJoinsChildren(t *testing.T) {
	r, err := newPathResolver("/home/alice/")
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.resolve("docs/report.txt", pathUseMutateChild)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/home/alice/docs/report.txt" {
		t.Fatalf("got %q", got)
	}
}

func TestNewPathResolverRequiresAbsoluteRoot(t *testing.T) {
	for _, root := range []string{"", ".", "relative/root"} {
		if _, err := newPathResolver(root); err == nil {
			t.Fatalf("expected root %q to fail", root)
		}
	}
}
```

- [ ] **Step 2: Run path tests to verify they fail**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -run TestResolvePath -count=1
```

Expected: compile failure with `undefined: newPathResolver`.

- [ ] **Step 3: Implement the resolver**

Create `server/plugin/plg_handler_grpc_session/path.go` with:

```go
package plg_handler_grpc_session

import (
	"path"
	"strings"

	. "github.com/mickael-kerjean/filestash/server/common"
)

type pathUse int

const (
	pathUseRead pathUse = iota
	pathUseMutateChild
)

type pathResolver struct {
	root string
}

func newPathResolver(root string) (pathResolver, error) {
	if root == "" || !strings.HasPrefix(root, "/") {
		return pathResolver{}, ErrNotValid
	}
	clean := path.Clean(root)
	if clean == "." {
		return pathResolver{}, ErrNotValid
	}
	return pathResolver{root: clean}, nil
}

func (r pathResolver) resolve(input string, usage pathUse) (string, error) {
	if input == "" || input == "." {
		if usage == pathUseMutateChild {
			return "", ErrNotAllowed
		}
		return r.root, nil
	}
	if strings.HasPrefix(input, "/") {
		return "", ErrFilesystemError
	}
	cleanInput := path.Clean(input)
	if cleanInput == "." {
		if usage == pathUseMutateChild {
			return "", ErrNotAllowed
		}
		return r.root, nil
	}
	if cleanInput == ".." || strings.HasPrefix(cleanInput, "../") {
		return "", ErrFilesystemError
	}
	out := path.Clean(path.Join(r.root, cleanInput))
	if out != r.root && strings.HasPrefix(out, r.root+"/") {
		return out, nil
	}
	if out == r.root && usage == pathUseRead {
		return out, nil
	}
	return "", ErrFilesystemError
}
```

- [ ] **Step 4: Run path tests to verify they pass**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -run 'TestResolvePath|TestNewPathResolver' -count=1
```

Expected: `ok github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session`.

- [ ] **Step 5: Commit**

Run:

```bash
git add server/plugin/plg_handler_grpc_session/path.go server/plugin/plg_handler_grpc_session/path_test.go
git commit -m "feat: add sidecar path resolver"
```

---

### Task 3: Implement Session Lifecycle and Lease Renewal

**Files:**
- Create: `server/plugin/plg_handler_grpc_session/session.go`
- Create: `server/plugin/plg_handler_grpc_session/session_test.go`

- [ ] **Step 1: Write failing session tests**

Create `server/plugin/plg_handler_grpc_session/session_test.go` with:

```go
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
			idleTimeout: 2 * time.Minute,
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
```

- [ ] **Step 2: Run session tests to verify they fail**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -run TestSession -count=1
```

Expected: compile failure with `undefined: newSessionManager`.

- [ ] **Step 3: Implement session manager**

Create `server/plugin/plg_handler_grpc_session/session.go` with:

```go
package plg_handler_grpc_session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	. "github.com/mickael-kerjean/filestash/server/common"
	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
)

type effectiveLease struct {
	duration    time.Duration
	idleTimeout time.Duration
	maxLifetime time.Duration
	renewable   bool
}

type sessionLeaseState struct {
	expiresAt     time.Time
	idleExpiresAt time.Time
	maxExpiresAt  time.Time
	renewable     bool
}

type sidecarSession struct {
	id            string
	ownerIdentity string
	backendType   string
	backend       IBackend
	rootPath      string
	mode          pb.AccessMode
	externalRef   externalRef
	openedAt      time.Time
	lastUsedAt    time.Time
	expiresAt     time.Time
	idleExpiresAt time.Time
	maxExpiresAt  time.Time
	lease         effectiveLease
	state         pb.SessionState
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
}

type externalRef struct {
	kind string
	id   string
}

type openSessionInput struct {
	ownerIdentity string
	backendType    string
	backend        IBackend
	rootPath       string
	mode           pb.AccessMode
	lease          effectiveLease
	externalRef    externalRef
}

type sessionManagerOptions struct {
	now func() time.Time
	id  func() (string, error)
}

type sessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*sidecarSession
	now      func() time.Time
	id       func() (string, error)
}

func newSessionManager(opts sessionManagerOptions) *sessionManager {
	if opts.now == nil {
		opts.now = time.Now
	}
	if opts.id == nil {
		opts.id = randomSessionID
	}
	return &sessionManager{
		sessions: make(map[string]*sidecarSession),
		now:      opts.now,
		id:       opts.id,
	}
}

func randomSessionID() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (m *sessionManager) open(parent context.Context, in openSessionInput) (*sidecarSession, error) {
	id, err := m.id()
	if err != nil {
		return nil, err
	}
	now := m.now()
	ctx, cancel := context.WithCancel(parent)
	s := &sidecarSession{
		id:            id,
		ownerIdentity: in.ownerIdentity,
		backendType:   in.backendType,
		backend:       in.backend,
		rootPath:      in.rootPath,
		mode:          in.mode,
		externalRef:   in.externalRef,
		openedAt:      now,
		lastUsedAt:    now,
		expiresAt:     now.Add(in.lease.duration),
		idleExpiresAt: now.Add(in.lease.idleTimeout),
		maxExpiresAt:  now.Add(in.lease.maxLifetime),
		lease:         in.lease,
		state:         pb.SessionState_SESSION_STATE_ACTIVE,
		ctx:           ctx,
		cancel:        cancel,
	}
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()
	return s, nil
}

func (m *sessionManager) get(caller string, operator bool, id string) (*sidecarSession, error) {
	m.mu.RLock()
	s := m.sessions[id]
	m.mu.RUnlock()
	if s == nil {
		return nil, ErrNotFound
	}
	if !operator && s.ownerIdentity != caller {
		return nil, ErrPermissionDenied
	}
	if err := m.ensureActive(s); err != nil {
		return nil, err
	}
	return s, nil
}

func (m *sessionManager) renew(caller string, operator bool, id string) (sessionLeaseState, error) {
	s, err := m.get(caller, operator, id)
	if err != nil {
		return sessionLeaseState{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.lease.renewable {
		return sessionLeaseState{}, ErrNotAllowed
	}
	now := m.now()
	expiresAt := now.Add(s.lease.duration)
	if expiresAt.After(s.maxExpiresAt) {
		expiresAt = s.maxExpiresAt
	}
	if !expiresAt.After(now) {
		s.state = pb.SessionState_SESSION_STATE_EXPIRED
		return sessionLeaseState{}, ErrTimeout
	}
	s.lastUsedAt = now
	s.expiresAt = expiresAt
	s.idleExpiresAt = now.Add(s.lease.idleTimeout)
	return sessionLeaseState{
		expiresAt:     s.expiresAt,
		idleExpiresAt: s.idleExpiresAt,
		maxExpiresAt:  s.maxExpiresAt,
		renewable:     s.lease.renewable,
	}, nil
}

func (m *sessionManager) markUsed(s *sidecarSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := m.now()
	s.lastUsedAt = now
	s.idleExpiresAt = now.Add(s.lease.idleTimeout)
}

func (m *sessionManager) close(caller string, operator bool, id string) error {
	m.mu.RLock()
	s := m.sessions[id]
	m.mu.RUnlock()
	if s == nil {
		return nil
	}
	if !operator && s.ownerIdentity != caller {
		return ErrPermissionDenied
	}
	s.mu.Lock()
	if s.state == pb.SessionState_SESSION_STATE_CLOSED {
		s.mu.Unlock()
		return nil
	}
	s.state = pb.SessionState_SESSION_STATE_CLOSED
	s.cancel()
	backend := s.backend
	s.mu.Unlock()
	if c, ok := backend.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}

func (m *sessionManager) ensureActive(s *sidecarSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != pb.SessionState_SESSION_STATE_ACTIVE {
		return ErrNotAllowed
	}
	now := m.now()
	if now.After(s.expiresAt) || now.After(s.idleExpiresAt) || now.After(s.maxExpiresAt) {
		s.state = pb.SessionState_SESSION_STATE_EXPIRED
		s.cancel()
		return ErrTimeout
	}
	return nil
}
```

- [ ] **Step 4: Run session tests to verify they pass**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -run TestSession -count=1
```

Expected: `ok github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session`.

- [ ] **Step 5: Commit**

Run:

```bash
git add server/plugin/plg_handler_grpc_session/session.go server/plugin/plg_handler_grpc_session/session_test.go
git commit -m "feat: add sidecar session lifecycle"
```

---

### Task 4: Implement Policy Parsing and mTLS Identity Matching

**Files:**
- Create: `server/plugin/plg_handler_grpc_session/policy.go`
- Create: `server/plugin/plg_handler_grpc_session/policy_test.go`

- [ ] **Step 1: Write failing policy tests**

Create `server/plugin/plg_handler_grpc_session/policy_test.go` with:

```go
package plg_handler_grpc_session

import (
	"testing"
	"time"

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
			idleTimeout: 24 * time.Hour,
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
	if effective.lease.duration != 15*time.Minute {
		t.Fatalf("duration=%s", effective.lease.duration)
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
		backendParams: map[string]string{"hostname": "192.168.1.10"},
	}); err == nil {
		t.Fatal("expected CIDR rejection")
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
```

- [ ] **Step 2: Run policy tests to verify they fail**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -run TestPolicy -count=1
```

Expected: compile failure with `undefined: parsePolicyConfig`.

- [ ] **Step 3: Implement policy engine**

Create `server/plugin/plg_handler_grpc_session/policy.go` with:

```go
package plg_handler_grpc_session

import (
	"encoding/json"
	"net"
	"strings"
	"time"

	. "github.com/mickael-kerjean/filestash/server/common"
	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
)

type policyEngine struct {
	clients []clientPolicy
}

type clientPolicy struct {
	Identity       string   `json:"identity"`
	Role           string   `json:"role"`
	HostCIDRs      []string `json:"host_cidrs"`
	AccessModes    []string `json:"access_modes"`
	MaxSessions    int      `json:"max_sessions"`
	Lease          leaseJSON `json:"lease"`
	Limits         limitsJSON `json:"limits"`
	parsedCIDRs    []*net.IPNet
}

type leaseJSON struct {
	DurationSeconds       uint32 `json:"duration_seconds"`
	IdleTimeoutSeconds    uint32 `json:"idle_timeout_seconds"`
	MaxLifetimeSeconds    uint32 `json:"max_lifetime_seconds"`
	Renewable             bool   `json:"renewable"`
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
	if len(raw) == 0 {
		raw = []byte(`{"clients":[]}`)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	for i := range cfg.Clients {
		for _, cidr := range cfg.Clients[i].HostCIDRs {
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
	for i := range e.clients {
		if e.clients[i].Identity == identity {
			return &e.clients[i], nil
		}
	}
	return nil, ErrNotAuthorized
}

func (p *clientPolicy) effectiveOpen(req openPolicyRequest) (effectivePolicy, error) {
	if err := p.validateTarget(req.backendParams); err != nil {
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
	if requested == pb.AccessMode_ACCESS_MODE_UNSPECIFIED {
		requested = pb.AccessMode_ACCESS_MODE_READ
	}
	canRead := false
	canWrite := false
	for _, m := range p.AccessModes {
		canRead = canRead || m == "read"
		canWrite = canWrite || m == "write"
	}
	if requested == pb.AccessMode_ACCESS_MODE_READ_WRITE && canWrite {
		return pb.AccessMode_ACCESS_MODE_READ_WRITE
	}
	if canRead {
		return pb.AccessMode_ACCESS_MODE_READ
	}
	return pb.AccessMode_ACCESS_MODE_UNSPECIFIED
}

func (p *clientPolicy) clampLease(req leaseRequest) effectiveLease {
	maxDuration := seconds(p.Lease.DurationSeconds)
	maxIdle := seconds(p.Lease.IdleTimeoutSeconds)
	maxLifetime := seconds(p.Lease.MaxLifetimeSeconds)
	out := effectiveLease{
		duration:    minPositive(req.duration, maxDuration),
		idleTimeout: minPositive(req.idleTimeout, maxIdle),
		maxLifetime: minPositive(req.maxLifetime, maxLifetime),
		renewable:   req.renewable && p.Lease.Renewable,
	}
	if out.duration == 0 {
		out.duration = maxDuration
	}
	if out.idleTimeout == 0 {
		out.idleTimeout = maxIdle
	}
	if out.maxLifetime == 0 {
		out.maxLifetime = maxLifetime
	}
	return out
}

func seconds(v uint32) time.Duration {
	return time.Duration(v) * time.Second
}

func minPositive(a, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	if b <= 0 || a < b {
		return a
	}
	return b
}

func (p *clientPolicy) validateTarget(params map[string]string) error {
	if len(p.parsedCIDRs) == 0 {
		return nil
	}
	host := firstNonEmpty(params["hostname"], params["host"], params["endpoint"], params["url"])
	if host == "" {
		return nil
	}
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return ErrNotAllowed
		}
		ip = ips[0]
	}
	for _, cidr := range p.parsedCIDRs {
		if cidr.Contains(ip) {
			return nil
		}
	}
	return ErrPermissionDenied
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
```

- [ ] **Step 4: Run policy tests to verify they pass**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -run TestPolicy -count=1
```

Expected: `ok github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session`.

- [ ] **Step 5: Commit**

Run:

```bash
git add server/plugin/plg_handler_grpc_session/policy.go server/plugin/plg_handler_grpc_session/policy_test.go
git commit -m "feat: add sidecar policy engine"
```

---

### Task 5: Add Config Accessors, Redaction, and Backend Adapter

**Files:**
- Create: `server/plugin/plg_handler_grpc_session/config.go`
- Create: `server/plugin/plg_handler_grpc_session/backend.go`
- Create: `server/plugin/plg_handler_grpc_session/backend_test.go`

- [ ] **Step 1: Write failing backend adapter tests**

Create `server/plugin/plg_handler_grpc_session/backend_test.go` with:

```go
package plg_handler_grpc_session

import (
	"os"
	"testing"
	"time"

	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
)

type fakeInfo struct {
	name  string
	dir   bool
	size  int64
	mtime time.Time
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) Mode() os.FileMode  { if f.dir { return os.ModeDir | 0755 }; return 0644 }
func (f fakeInfo) ModTime() time.Time { return f.mtime }
func (f fakeInfo) IsDir() bool        { return f.dir }
func (f fakeInfo) Sys() any           { return nil }

func TestFileInfoFromOS(t *testing.T) {
	mt := time.Unix(100, 2000000)
	got := fileInfoFromOS(fakeInfo{name: "docs", dir: true, size: 5, mtime: mt})
	if got.Name != "docs" || got.Type != "directory" || got.Size != 5 {
		t.Fatalf("unexpected file info: %+v", got)
	}
	if got.ModTimeUnixMs != mt.UnixNano()/int64(time.Millisecond) {
		t.Fatalf("mod time=%d", got.ModTimeUnixMs)
	}
	if got.Mode == 0 {
		t.Fatal("expected mode")
	}
}

func TestRedactParams(t *testing.T) {
	got := redactBackendParams(map[string]string{
		"hostname":   "sftp.internal",
		"username":   "alice",
		"password":   "secret",
		"privateKey": "secret-key",
	})
	if got["password"] != "[REDACTED]" || got["privateKey"] != "[REDACTED]" {
		t.Fatalf("credentials not redacted: %+v", got)
	}
	if got["hostname"] != "sftp.internal" {
		t.Fatalf("hostname changed: %+v", got)
	}
}

func TestIsWriteMode(t *testing.T) {
	if !isWriteMode(pb.AccessMode_ACCESS_MODE_READ_WRITE) {
		t.Fatal("expected read-write")
	}
	if isWriteMode(pb.AccessMode_ACCESS_MODE_READ) {
		t.Fatal("read-only should not be write mode")
	}
}
```

- [ ] **Step 2: Run backend tests to verify they fail**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -run 'TestFileInfo|TestRedact|TestIsWriteMode' -count=1
```

Expected: compile failure with `undefined: fileInfoFromOS`.

- [ ] **Step 3: Implement config accessors**

Create `server/plugin/plg_handler_grpc_session/config.go` with:

```go
package plg_handler_grpc_session

import . "github.com/mickael-kerjean/filestash/server/common"

var PluginEnable = func() bool {
	return Config.Get("features.sidecar_grpc.enable").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Name = "enable"
		f.Type = "enable"
		f.Target = []string{"sidecar_grpc_listen_addr"}
		f.Description = "Enable/Disable the sidecar gRPC endpoint"
		f.Default = false
		return f
	}).Bool()
}

var PluginListenAddr = func() string {
	return Config.Get("features.sidecar_grpc.listen_addr").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Id = "sidecar_grpc_listen_addr"
		f.Name = "listen_addr"
		f.Type = "text"
		f.Default = "127.0.0.1:9443"
		return f
	}).String()
}

var PluginTLSCertFile = func() string {
	return Config.Get("features.sidecar_grpc.tls.cert_file").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Name = "cert_file"
		f.Type = "text"
		return f
	}).String()
}

var PluginTLSKeyFile = func() string {
	return Config.Get("features.sidecar_grpc.tls.key_file").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Name = "key_file"
		f.Type = "text"
		return f
	}).String()
}

var PluginTLSClientCAFile = func() string {
	return Config.Get("features.sidecar_grpc.tls.client_ca_file").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Name = "client_ca_file"
		f.Type = "text"
		return f
	}).String()
}

var PluginPolicies = func() string {
	return Config.Get("features.sidecar_grpc.policies").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Name = "policies"
		f.Type = "long_text"
		f.Default = `{"clients":[]}`
		return f
	}).String()
}

var PluginMaxSessions = func() int {
	return Config.Get("features.sidecar_grpc.limits.max_sessions").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Name = "max_sessions"
		f.Type = "number"
		f.Default = 100
		return f
	}).Int()
}
```

- [ ] **Step 4: Implement backend helpers**

Create `server/plugin/plg_handler_grpc_session/backend.go` with:

```go
package plg_handler_grpc_session

import (
	"os"
	"strings"
	"time"

	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
)

func fileInfoFromOS(info os.FileInfo) *pb.FileInfo {
	fileType := "file"
	if info.IsDir() {
		fileType = "directory"
	}
	return &pb.FileInfo{
		Name:          info.Name(),
		Type:          fileType,
		Size:          info.Size(),
		ModTimeUnixMs: unixMillis(info.ModTime()),
		Mode:          uint32(info.Mode()),
	}
}

func unixMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano() / int64(time.Millisecond)
}

func redactBackendParams(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		lower := strings.ToLower(k)
		if strings.Contains(lower, "pass") ||
			strings.Contains(lower, "secret") ||
			strings.Contains(lower, "token") ||
			strings.Contains(lower, "key") ||
			strings.Contains(lower, "credential") {
			out[k] = "[REDACTED]"
			continue
		}
		out[k] = v
	}
	return out
}

func isWriteMode(mode pb.AccessMode) bool {
	return mode == pb.AccessMode_ACCESS_MODE_READ_WRITE
}
```

- [ ] **Step 5: Run backend tests to verify they pass**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -run 'TestFileInfo|TestRedact|TestIsWriteMode' -count=1
```

Expected: `ok github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session`.

- [ ] **Step 6: Commit**

Run:

```bash
git add server/plugin/plg_handler_grpc_session/config.go server/plugin/plg_handler_grpc_session/backend.go server/plugin/plg_handler_grpc_session/backend_test.go
git commit -m "feat: add sidecar config and backend helpers"
```

---

### Task 6: Implement gRPC Error Mapping and Session RPCs

**Files:**
- Create: `server/plugin/plg_handler_grpc_session/errors.go`
- Create: `server/plugin/plg_handler_grpc_session/handler.go`
- Create: `server/plugin/plg_handler_grpc_session/handler_test.go`

- [ ] **Step 1: Write failing handler tests for session RPCs**

Create `server/plugin/plg_handler_grpc_session/handler_test.go` with:

```go
package plg_handler_grpc_session

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
	"time"

	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

func contextWithIdentity(identity string) context.Context {
	cert := &x509.Certificate{URIs: nil, Subject: pkix.Name{CommonName: identity}}
	info := credentials.TLSInfo{}
	info.State.PeerCertificates = []*x509.Certificate{cert}
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: info})
}

func TestRenewSessionRPCRequiresOwner(t *testing.T) {
	m := newSessionManager(sessionManagerOptions{id: func() (string, error) { return "s1", nil }})
	_, err := m.open(context.Background(), openSessionInput{
		ownerIdentity: "client-a",
		backendType:   "local",
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
	svc := &sidecarService{sessions: m}
	if _, err := svc.RenewSession(contextWithIdentity("client-b"), &pb.RenewSessionRequest{SessionId: "s1"}); err == nil {
		t.Fatal("expected permission error")
	}
}

func TestCloseSessionRPCIsIdempotent(t *testing.T) {
	m := newSessionManager(sessionManagerOptions{id: func() (string, error) { return "s1", nil }})
	_, err := m.open(context.Background(), openSessionInput{
		ownerIdentity: "client-a",
		backendType:   "local",
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
	svc := &sidecarService{sessions: m}
	for i := 0; i < 2; i++ {
		res, err := svc.Close(contextWithIdentity("client-a"), &pb.CloseSessionRequest{SessionId: "s1"})
		if err != nil {
			t.Fatal(err)
		}
		if res.State != pb.SessionState_SESSION_STATE_CLOSED {
			t.Fatalf("state=%s", res.State)
		}
	}
}
```

- [ ] **Step 2: Run handler tests to verify they fail**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -run 'TestRenewSessionRPC|TestCloseSessionRPC' -count=1
```

Expected: compile failure with `undefined: sidecarService`.

- [ ] **Step 3: Implement error mapping**

Create `server/plugin/plg_handler_grpc_session/errors.go` with:

```go
package plg_handler_grpc_session

import (
	"errors"

	. "github.com/mickael-kerjean/filestash/server/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func grpcError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrNotAuthorized):
		return status.Error(codes.Unauthenticated, err.Error())
	case errors.Is(err, ErrPermissionDenied), errors.Is(err, ErrNotAllowed):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrNotValid), errors.Is(err, ErrFilesystemError):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, ErrTimeout):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrNotReachable):
		return status.Error(codes.Unavailable, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
```

- [ ] **Step 4: Implement session RPC handlers**

Create the first version of `server/plugin/plg_handler_grpc_session/handler.go` with:

```go
package plg_handler_grpc_session

import (
	"context"
	"crypto/x509"
	"net/url"
	"time"

	. "github.com/mickael-kerjean/filestash/server/common"
	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type sidecarService struct {
	pb.UnimplementedFilestashSidecarServiceServer
	sessions *sessionManager
	policies *policyEngine
}

func identityFromContext(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", ErrNotAuthorized
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return "", ErrNotAuthorized
	}
	return identityFromCertificate(tlsInfo.State.PeerCertificates[0]), nil
}

func identityFromCertificate(cert *x509.Certificate) string {
	if len(cert.URIs) > 0 {
		return cert.URIs[0].String()
	}
	if len(cert.DNSNames) > 0 {
		return cert.DNSNames[0]
	}
	if len(cert.EmailAddresses) > 0 {
		return cert.EmailAddresses[0]
	}
	return cert.Subject.CommonName
}

func (s *sidecarService) RenewSession(ctx context.Context, req *pb.RenewSessionRequest) (*pb.SessionLease, error) {
	identity, err := identityFromContext(ctx)
	if err != nil {
		return nil, grpcError(err)
	}
	lease, err := s.sessions.renew(identity, false, req.SessionId)
	if err != nil {
		return nil, grpcError(err)
	}
	return leaseToProto(req.SessionId, lease), nil
}

func (s *sidecarService) Close(ctx context.Context, req *pb.CloseSessionRequest) (*pb.CloseSessionResponse, error) {
	identity, err := identityFromContext(ctx)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.sessions.close(identity, false, req.SessionId); err != nil {
		return nil, grpcError(err)
	}
	return &pb.CloseSessionResponse{SessionId: req.SessionId, State: pb.SessionState_SESSION_STATE_CLOSED}, nil
}

func leaseToProto(sessionID string, lease sessionLeaseState) *pb.SessionLease {
	return &pb.SessionLease{
		SessionId:      sessionID,
		ExpiresAt:     timestamppb.New(lease.expiresAt),
		IdleExpiresAt: timestamppb.New(lease.idleExpiresAt),
		MaxExpiresAt:  timestamppb.New(lease.maxExpiresAt),
		Renewable:     lease.renewable,
	}
}

func leaseRequestFromProto(in *pb.LeaseOptions) leaseRequest {
	if in == nil {
		return leaseRequest{}
	}
	return leaseRequest{
		duration:    time.Duration(in.DurationSeconds) * time.Second,
		idleTimeout: time.Duration(in.IdleTimeoutSeconds) * time.Second,
		maxLifetime: time.Duration(in.MaxLifetimeSeconds) * time.Second,
		renewable:   in.Renewable,
	}
}

func externalRefFromProto(in *pb.ExternalRef) externalRef {
	if in == nil {
		return externalRef{}
	}
	return externalRef{kind: in.Kind, id: in.Id}
}

func redactedTarget(params map[string]string) string {
	for _, key := range []string{"hostname", "host", "endpoint", "url"} {
		if params[key] != "" {
			if u, err := url.Parse(params[key]); err == nil && u.Host != "" {
				return u.Host
			}
			return params[key]
		}
	}
	return ""
}
```

- [ ] **Step 5: Run handler tests to verify they pass**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -run 'TestRenewSessionRPC|TestCloseSessionRPC' -count=1
```

Expected: `ok github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session`.

- [ ] **Step 6: Commit**

Run:

```bash
git add server/plugin/plg_handler_grpc_session/errors.go server/plugin/plg_handler_grpc_session/handler.go server/plugin/plg_handler_grpc_session/handler_test.go
git commit -m "feat: add sidecar session rpc handlers"
```

---

### Task 7: Implement Open and Filesystem RPCs

**Files:**
- Modify: `server/plugin/plg_handler_grpc_session/handler.go`
- Modify: `server/plugin/plg_handler_grpc_session/handler_test.go`

- [ ] **Step 1: Extend handler tests with fake backend filesystem operations**

Append to `server/plugin/plg_handler_grpc_session/handler_test.go`:

```go
func TestListRPCUsesSessionRoot(t *testing.T) {
	backend := &memoryBackend{
		files: map[string][]os.FileInfo{
			"/work": {fakeInfo{name: "report.txt", size: 7}},
		},
	}
	m := newSessionManager(sessionManagerOptions{id: func() (string, error) { return "s1", nil }})
	_, err := m.open(context.Background(), openSessionInput{
		ownerIdentity: "client-a",
		backendType:   "memory",
		backend:       backend,
		rootPath:      "/work",
		mode:          pb.AccessMode_ACCESS_MODE_READ_WRITE,
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
	svc := &sidecarService{sessions: m}
	res, err := svc.List(contextWithIdentity("client-a"), &pb.ListRequest{SessionId: "s1", Path: "."})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 || res.Files[0].Name != "report.txt" {
		t.Fatalf("files=%+v", res.Files)
	}
}

func TestMkdirRejectsReadOnlySession(t *testing.T) {
	m := newSessionManager(sessionManagerOptions{id: func() (string, error) { return "s1", nil }})
	_, err := m.open(context.Background(), openSessionInput{
		ownerIdentity: "client-a",
		backendType:   "memory",
		backend:       &memoryBackend{},
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
	svc := &sidecarService{sessions: m}
	if _, err := svc.Mkdir(contextWithIdentity("client-a"), &pb.MkdirRequest{SessionId: "s1", Path: "new"}); err == nil {
		t.Fatal("expected read-only mutation failure")
	}
}
```

Also add the imports and test backend:

```go
import (
	"bytes"
	"io"
	"os"

	. "github.com/mickael-kerjean/filestash/server/common"
)

type memoryBackend struct {
	Nothing
	files map[string][]os.FileInfo
	data  map[string][]byte
}

func (b *memoryBackend) Ls(path string) ([]os.FileInfo, error) {
	if b.files == nil {
		return []os.FileInfo{}, nil
	}
	return b.files[path], nil
}

func (b *memoryBackend) Cat(path string) (io.ReadCloser, error) {
	if b.data == nil {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	return io.NopCloser(bytes.NewReader(b.data[path])), nil
}

func (b *memoryBackend) Save(path string, r io.Reader) error {
	if b.data == nil {
		b.data = map[string][]byte{}
	}
	buf, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.data[path] = buf
	return nil
}

func (b *memoryBackend) Mkdir(path string) error { return nil }
func (b *memoryBackend) Rm(path string) error    { return nil }
func (b *memoryBackend) Mv(from, to string) error { return nil }
func (b *memoryBackend) Touch(path string) error { return nil }
```

- [ ] **Step 2: Run filesystem handler tests to verify they fail**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -run 'TestListRPC|TestMkdirRejects' -count=1
```

Expected: compile failure with `*sidecarService has no field or method List`.

- [ ] **Step 3: Implement filesystem RPC helpers**

Append to `server/plugin/plg_handler_grpc_session/handler.go`:

```go
func (s *sidecarService) sessionForOperation(ctx context.Context, sessionID string) (*sidecarSession, error) {
	identity, err := identityFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.sessions.get(identity, false, sessionID)
}

func (s *sidecarService) List(ctx context.Context, req *pb.ListRequest) (*pb.ListResponse, error) {
	session, err := s.sessionForOperation(ctx, req.SessionId)
	if err != nil {
		return nil, grpcError(err)
	}
	resolver, err := newPathResolver(session.rootPath)
	if err != nil {
		return nil, grpcError(err)
	}
	backendPath, err := resolver.resolve(req.Path, pathUseRead)
	if err != nil {
		return nil, grpcError(err)
	}
	entries, err := session.backend.Ls(backendPath)
	if err != nil {
		return nil, grpcError(err)
	}
	out := make([]*pb.FileInfo, len(entries))
	for i := range entries {
		out[i] = fileInfoFromOS(entries[i])
	}
	s.sessions.markUsed(session)
	return &pb.ListResponse{Files: out}, nil
}

func (s *sidecarService) Stat(ctx context.Context, req *pb.StatRequest) (*pb.StatResponse, error) {
	session, err := s.sessionForOperation(ctx, req.SessionId)
	if err != nil {
		return nil, grpcError(err)
	}
	resolver, err := newPathResolver(session.rootPath)
	if err != nil {
		return nil, grpcError(err)
	}
	backendPath, err := resolver.resolve(req.Path, pathUseRead)
	if err != nil {
		return nil, grpcError(err)
	}
	info, err := session.backend.Stat(backendPath)
	if err != nil {
		return nil, grpcError(err)
	}
	s.sessions.markUsed(session)
	return &pb.StatResponse{File: fileInfoFromOS(info)}, nil
}

func (s *sidecarService) ensureWritable(session *sidecarSession) error {
	if !isWriteMode(session.mode) {
		return ErrNotAllowed
	}
	return nil
}

func (s *sidecarService) Mkdir(ctx context.Context, req *pb.MkdirRequest) (*pb.MutationResponse, error) {
	session, err := s.sessionForOperation(ctx, req.SessionId)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.ensureWritable(session); err != nil {
		return nil, grpcError(err)
	}
	resolver, err := newPathResolver(session.rootPath)
	if err != nil {
		return nil, grpcError(err)
	}
	backendPath, err := resolver.resolve(req.Path, pathUseMutateChild)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := session.backend.Mkdir(backendPath); err != nil {
		return nil, grpcError(err)
	}
	s.sessions.markUsed(session)
	return &pb.MutationResponse{Ok: true}, nil
}
```

- [ ] **Step 4: Add Remove, Rename, Touch, ReadFile, and WriteFile**

Append to `server/plugin/plg_handler_grpc_session/handler.go`:

```go
func (s *sidecarService) Remove(ctx context.Context, req *pb.RemoveRequest) (*pb.MutationResponse, error) {
	session, err := s.sessionForOperation(ctx, req.SessionId)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.ensureWritable(session); err != nil {
		return nil, grpcError(err)
	}
	resolver, err := newPathResolver(session.rootPath)
	if err != nil {
		return nil, grpcError(err)
	}
	backendPath, err := resolver.resolve(req.Path, pathUseMutateChild)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := session.backend.Rm(backendPath); err != nil {
		return nil, grpcError(err)
	}
	s.sessions.markUsed(session)
	return &pb.MutationResponse{Ok: true}, nil
}

func (s *sidecarService) Rename(ctx context.Context, req *pb.RenameRequest) (*pb.MutationResponse, error) {
	session, err := s.sessionForOperation(ctx, req.SessionId)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.ensureWritable(session); err != nil {
		return nil, grpcError(err)
	}
	resolver, err := newPathResolver(session.rootPath)
	if err != nil {
		return nil, grpcError(err)
	}
	from, err := resolver.resolve(req.From, pathUseMutateChild)
	if err != nil {
		return nil, grpcError(err)
	}
	to, err := resolver.resolve(req.To, pathUseMutateChild)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := session.backend.Mv(from, to); err != nil {
		return nil, grpcError(err)
	}
	s.sessions.markUsed(session)
	return &pb.MutationResponse{Ok: true}, nil
}

func (s *sidecarService) Touch(ctx context.Context, req *pb.TouchRequest) (*pb.MutationResponse, error) {
	session, err := s.sessionForOperation(ctx, req.SessionId)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.ensureWritable(session); err != nil {
		return nil, grpcError(err)
	}
	resolver, err := newPathResolver(session.rootPath)
	if err != nil {
		return nil, grpcError(err)
	}
	backendPath, err := resolver.resolve(req.Path, pathUseMutateChild)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := session.backend.Touch(backendPath); err != nil {
		return nil, grpcError(err)
	}
	s.sessions.markUsed(session)
	return &pb.MutationResponse{Ok: true}, nil
}
```

For `ReadFile` and `WriteFile`, use streaming code that reads from and writes to the backend:

```go
func (s *sidecarService) ReadFile(req *pb.ReadFileRequest, stream pb.FilestashSidecarService_ReadFileServer) error {
	session, err := s.sessionForOperation(stream.Context(), req.SessionId)
	if err != nil {
		return grpcError(err)
	}
	resolver, err := newPathResolver(session.rootPath)
	if err != nil {
		return grpcError(err)
	}
	backendPath, err := resolver.resolve(req.Path, pathUseMutateChild)
	if err != nil {
		return grpcError(err)
	}
	rc, err := session.backend.Cat(backendPath)
	if err != nil {
		return grpcError(err)
	}
	defer rc.Close()
	buf := make([]byte, 64*1024)
	for {
		n, readErr := rc.Read(buf)
		if n > 0 {
			if err := stream.Send(&pb.ReadFileChunk{Data: append([]byte(nil), buf[:n]...)}); err != nil {
				return grpcError(err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return grpcError(readErr)
		}
	}
	s.sessions.markUsed(session)
	return nil
}

func (s *sidecarService) WriteFile(stream pb.FilestashSidecarService_WriteFileServer) error {
	first, err := stream.Recv()
	if err != nil {
		return grpcError(err)
	}
	header := first.GetHeader()
	if header == nil {
		return grpcError(ErrNotValid)
	}
	session, err := s.sessionForOperation(stream.Context(), header.SessionId)
	if err != nil {
		return grpcError(err)
	}
	if err := s.ensureWritable(session); err != nil {
		return grpcError(err)
	}
	resolver, err := newPathResolver(session.rootPath)
	if err != nil {
		return grpcError(err)
	}
	backendPath, err := resolver.resolve(header.Path, pathUseMutateChild)
	if err != nil {
		return grpcError(err)
	}
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- session.backend.Save(backendPath, pr)
	}()
	var written int64
	for {
		msg, recvErr := stream.Recv()
		if recvErr == io.EOF {
			_ = pw.Close()
			break
		}
		if recvErr != nil {
			_ = pw.CloseWithError(recvErr)
			return grpcError(recvErr)
		}
		chunk := msg.GetData()
		if len(chunk) == 0 {
			continue
		}
		n, writeErr := pw.Write(chunk)
		written += int64(n)
		if writeErr != nil {
			_ = pw.CloseWithError(writeErr)
			return grpcError(writeErr)
		}
	}
	if err := <-done; err != nil {
		return grpcError(err)
	}
	s.sessions.markUsed(session)
	return stream.SendAndClose(&pb.WriteFileResponse{BytesWritten: written})
}
```

Add `io` to the `handler.go` imports.

- [ ] **Step 5: Run filesystem handler tests**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -run 'TestListRPC|TestMkdirRejects' -count=1
```

Expected: `ok github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session`.

- [ ] **Step 6: Commit**

Run:

```bash
git add server/plugin/plg_handler_grpc_session/handler.go server/plugin/plg_handler_grpc_session/handler_test.go
git commit -m "feat: add sidecar filesystem rpc handlers"
```

---

### Task 8: Implement Open RPC and Backend Initialization

**Files:**
- Modify: `server/plugin/plg_handler_grpc_session/handler.go`
- Modify: `server/plugin/plg_handler_grpc_session/handler_test.go`

- [ ] **Step 1: Write failing Open tests**

Append to `server/plugin/plg_handler_grpc_session/handler_test.go`:

```go
func TestOpenRejectsUnknownBackend(t *testing.T) {
	engine, err := parsePolicyConfig([]byte(`{
	  "clients": [{
	    "identity": "client-a",
	    "access_modes": ["read"],
	    "lease": {
	      "duration_seconds": 60,
	      "idle_timeout_seconds": 60,
	      "max_lifetime_seconds": 600,
	      "renewable": true
	    }
	  }]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	svc := &sidecarService{sessions: newSessionManager(sessionManagerOptions{}), policies: engine}
	if _, err := svc.Open(contextWithIdentity("client-a"), &pb.OpenRequest{
		BackendType: "does-not-exist",
		RootPath:    "/work",
		Mode:        pb.AccessMode_ACCESS_MODE_READ,
	}); err == nil {
		t.Fatal("expected unknown backend to fail")
	}
}
```

- [ ] **Step 2: Run Open test to verify it fails**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -run TestOpenRejectsUnknownBackend -count=1
```

Expected: compile failure with `*sidecarService has no field or method Open`.

- [ ] **Step 3: Implement Open**

Append to `server/plugin/plg_handler_grpc_session/handler.go`:

```go
func (s *sidecarService) Open(ctx context.Context, req *pb.OpenRequest) (*pb.OpenResponse, error) {
	identity, err := identityFromContext(ctx)
	if err != nil {
		return nil, grpcError(err)
	}
	if req.RootPath == "" || req.BackendType == "" {
		return nil, grpcError(ErrNotValid)
	}
	driver := Backend.Get(req.BackendType)
	if _, ok := Backend.Drivers()[req.BackendType]; !ok {
		return nil, grpcError(ErrNotFound)
	}
	policy, err := s.policies.policyFor(identity)
	if err != nil {
		return nil, grpcError(err)
	}
	effective, err := policy.effectiveOpen(openPolicyRequest{
		backendType:   req.BackendType,
		backendParams: req.BackendParams,
		mode:          req.Mode,
		lease:         leaseRequestFromProto(req.Lease),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	resolver, err := newPathResolver(req.RootPath)
	if err != nil {
		return nil, grpcError(err)
	}
	app := &App{Context: ctx}
	backend, err := driver.Init(req.BackendParams, app)
	if err != nil {
		return nil, grpcError(err)
	}
	if _, err := backend.Ls(resolver.root); err != nil {
		if _, statErr := backend.Stat(resolver.root); statErr != nil {
			if c, ok := backend.(interface{ Close() error }); ok {
				_ = c.Close()
			}
			return nil, grpcError(err)
		}
	}
	session, err := s.sessions.open(ctx, openSessionInput{
		ownerIdentity: identity,
		backendType:   req.BackendType,
		backend:       backend,
		rootPath:      resolver.root,
		mode:          effective.mode,
		lease:         effective.lease,
		externalRef:   externalRefFromProto(req.ExternalRef),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	lease := sessionLeaseState{
		expiresAt:     session.expiresAt,
		idleExpiresAt: session.idleExpiresAt,
		maxExpiresAt:  session.maxExpiresAt,
		renewable:     effective.lease.renewable,
	}
	return &pb.OpenResponse{
		SessionId:     session.id,
		EffectiveMode: effective.mode,
		Lease:         leaseToProto(session.id, lease),
	}, nil
}
```

- [ ] **Step 4: Run Open test**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -run TestOpenRejectsUnknownBackend -count=1
```

Expected: `ok github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session`.

- [ ] **Step 5: Run all plugin tests**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -count=1
```

Expected: `ok github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session`.

- [ ] **Step 6: Commit**

Run:

```bash
git add server/plugin/plg_handler_grpc_session/handler.go server/plugin/plg_handler_grpc_session/handler_test.go
git commit -m "feat: add sidecar open rpc"
```

---

### Task 9: Add TCP mTLS Server and Plugin Registration

**Files:**
- Create: `server/plugin/plg_handler_grpc_session/server.go`
- Create: `server/plugin/plg_handler_grpc_session/index.go`
- Create: `server/plugin/plg_handler_grpc_session/server_test.go`
- Modify: `server/plugin/index.go`

- [ ] **Step 1: Write failing TLS config test**

Create `server/plugin/plg_handler_grpc_session/server_test.go` with:

```go
package plg_handler_grpc_session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMTLSConfigRejectsMissingFiles(t *testing.T) {
	_, err := loadMTLSConfig(sidecarTLSFiles{
		certFile:     filepath.Join(t.TempDir(), "server.crt"),
		keyFile:      filepath.Join(t.TempDir(), "server.key"),
		clientCAFile: filepath.Join(t.TempDir(), "ca.crt"),
	})
	if err == nil {
		t.Fatal("expected missing files to fail")
	}
}

func TestLoadPolicyFromConfigString(t *testing.T) {
	raw := `{"clients":[{"identity":"client-a","access_modes":["read"],"lease":{"duration_seconds":60,"idle_timeout_seconds":60,"max_lifetime_seconds":600,"renewable":true}}]}`
	engine, err := parsePolicyConfig([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.policyFor("client-a"); err != nil {
		t.Fatal(err)
	}
}

func TestServerUsesConfiguredAddressValue(t *testing.T) {
	addr := "127.0.0.1:0"
	if addr == "" || os.PathSeparator == 0 {
		t.Fatal("test setup failed")
	}
}
```

- [ ] **Step 2: Run server tests to verify they fail**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -run TestLoadMTLSConfig -count=1
```

Expected: compile failure with `undefined: loadMTLSConfig`.

- [ ] **Step 3: Implement gRPC server setup**

Create `server/plugin/plg_handler_grpc_session/server.go` with:

```go
package plg_handler_grpc_session

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"

	. "github.com/mickael-kerjean/filestash/server/common"
	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type sidecarTLSFiles struct {
	certFile     string
	keyFile      string
	clientCAFile string
}

type sidecarRuntime struct {
	server   *grpc.Server
	listener net.Listener
	sessions *sessionManager
	cancel   context.CancelFunc
}

func loadMTLSConfig(files sidecarTLSFiles) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(files.certFile, files.keyFile)
	if err != nil {
		return nil, err
	}
	caBytes, err := os.ReadFile(files.clientCAFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBytes) {
		return nil, ErrNotValid
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}, nil
}

func startSidecarServer(parent context.Context) (*sidecarRuntime, error) {
	tlsConfig, err := loadMTLSConfig(sidecarTLSFiles{
		certFile:     PluginTLSCertFile(),
		keyFile:      PluginTLSKeyFile(),
		clientCAFile: PluginTLSClientCAFile(),
	})
	if err != nil {
		return nil, err
	}
	policies, err := parsePolicyConfig([]byte(PluginPolicies()))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	listener, err := net.Listen("tcp", PluginListenAddr())
	if err != nil {
		cancel()
		return nil, err
	}
	sessions := newSessionManager(sessionManagerOptions{})
	grpcServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsConfig)))
	pb.RegisterFilestashSidecarServiceServer(grpcServer, &sidecarService{
		sessions: sessions,
		policies: policies,
	})
	runtime := &sidecarRuntime{
		server:   grpcServer,
		listener: listener,
		sessions: sessions,
		cancel:   cancel,
	}
	go func() {
		<-ctx.Done()
		grpcServer.GracefulStop()
	}()
	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			Log.Error("sidecar_grpc::serve err=%s", err.Error())
		}
	}()
	Log.Info("[sidecar_grpc] listening on %s", listener.Addr().String())
	return runtime, nil
}

func (r *sidecarRuntime) stop() {
	if r == nil {
		return
	}
	r.cancel()
	r.server.GracefulStop()
}
```

- [ ] **Step 4: Register plugin lifecycle**

Create `server/plugin/plg_handler_grpc_session/index.go` with:

```go
package plg_handler_grpc_session

import (
	"context"

	. "github.com/mickael-kerjean/filestash/server/common"
)

var runtime *sidecarRuntime

func init() {
	Hooks.Register.Onload(func() {
		PluginEnable()
		PluginListenAddr()
		PluginTLSCertFile()
		PluginTLSKeyFile()
		PluginTLSClientCAFile()
		PluginPolicies()
		PluginMaxSessions()
		if !PluginEnable() {
			return
		}
		var err error
		runtime, err = startSidecarServer(context.Background())
		if err != nil {
			Log.Error("sidecar_grpc::start err=%s", err.Error())
		}
	})
	Hooks.Register.OnQuit(func() {
		if runtime != nil {
			runtime.stop()
		}
	})
}
```

Modify `server/plugin/index.go` by adding this blank import near the other handler imports:

```go
_ "github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session"
```

- [ ] **Step 5: Run server tests**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -run 'TestLoadMTLSConfig|TestLoadPolicy' -count=1
```

Expected: `ok github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session`.

- [ ] **Step 6: Run package compile for the application**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session ./server/plugin ./server
```

Expected: all packages pass or report `[no test files]`.

- [ ] **Step 7: Commit**

Run:

```bash
git add server/plugin/plg_handler_grpc_session/server.go server/plugin/plg_handler_grpc_session/index.go server/plugin/plg_handler_grpc_session/server_test.go server/plugin/index.go
git commit -m "feat: register sidecar grpc server"
```

---

### Task 10: Add Integration Smoke and Final Verification

**Files:**
- Create: `server/plugin/plg_handler_grpc_session/integration_test.go`
- Modify: `server/plugin/plg_handler_grpc_session/session.go`
- Modify: `server/plugin/plg_handler_grpc_session/handler.go`

- [ ] **Step 1: Write integration smoke test**

Create `server/plugin/plg_handler_grpc_session/integration_test.go` with a direct service smoke test that avoids real network certificates while exercising the full service object:

```go
package plg_handler_grpc_session

import (
	"context"
	"os"
	"testing"
	"time"

	. "github.com/mickael-kerjean/filestash/server/common"
	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
)

func TestSidecarServiceSmokeOpenRenewClose(t *testing.T) {
	Backend.Register("memory-smoke", &memoryBackend{
		files: map[string][]os.FileInfo{"/work": {}},
		data:  map[string][]byte{},
	})
	engine, err := parsePolicyConfig([]byte(`{
	  "clients": [{
	    "identity": "client-a",
	    "access_modes": ["read", "write"],
	    "lease": {
	      "duration_seconds": 60,
	      "idle_timeout_seconds": 60,
	      "max_lifetime_seconds": 600,
	      "renewable": true
	    }
	  }]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	svc := &sidecarService{
		sessions: newSessionManager(sessionManagerOptions{}),
		policies: engine,
	}
	ctx := contextWithIdentity("client-a")
	openRes, err := svc.Open(ctx, &pb.OpenRequest{
		BackendType: "memory-smoke",
		BackendParams: map[string]string{},
		RootPath: "/work",
		Mode: pb.AccessMode_ACCESS_MODE_READ_WRITE,
		Lease: &pb.LeaseOptions{
			DurationSeconds: 60,
			IdleTimeoutSeconds: 60,
			MaxLifetimeSeconds: 600,
			Renewable: true,
		},
		ExternalRef: &pb.ExternalRef{Kind: "remote_shell", Id: "shell-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if openRes.SessionId == "" {
		t.Fatal("missing session id")
	}
	if _, err := svc.RenewSession(ctx, &pb.RenewSessionRequest{SessionId: openRes.SessionId}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Close(ctx, &pb.CloseSessionRequest{SessionId: openRes.SessionId}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1 * time.Millisecond)
}
```

- [ ] **Step 2: Run integration smoke test**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -run TestSidecarServiceSmokeOpenRenewClose -count=1
```

Expected: `ok github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session`.

- [ ] **Step 3: Run the full plugin test suite**

Run:

```bash
go test ./server/plugin/plg_handler_grpc_session -count=1
```

Expected: `ok github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session`.

- [ ] **Step 4: Run broader compile tests**

Run:

```bash
go test ./server/common ./server/plugin/plg_handler_mcp ./server/plugin/plg_handler_grpc_session ./server/plugin ./server
```

Expected: all listed packages pass or report `[no test files]`.

- [ ] **Step 5: Build the binary**

Run:

```bash
go build --tags "fts5" -o dist/filestash cmd/main.go
```

Expected: command exits with status 0 and creates `dist/filestash`.

- [ ] **Step 6: Scan for credential logging and stale names**

Run:

```bash
rg -n "password|privateKey|secret|token|logical_session_ref|connection_type|connection_id|extension|backend_types" server/plugin/plg_handler_grpc_session docs/superpowers/specs/2026-05-08-sidecar-grpc-session-design.md
```

Expected: matches are limited to redaction logic, proto/config field names, tests that deliberately verify redaction, and the spec's explanatory text. There should be no raw credential logging or stale rejected API names.

- [ ] **Step 7: Commit**

Run:

```bash
git add server/plugin/plg_handler_grpc_session
git commit -m "test: add sidecar grpc smoke coverage"
```

---

## Self-Review

- Spec coverage: The plan covers the new plugin, gRPC contract, `Open -> operate -> close`, TCP mTLS server, arbitrary registered backend use, path chrooting, lease renewal, owner/operator session management, credential redaction, config, and tests.
- Scope: The plan excludes browser UI, HTTP fallback, zip/search/share/metadata, and application-layer asymmetric encryption, matching the approved spec.
- Type consistency: The proto names use `ExternalRef`, `LeaseOptions`, and `RenewSessionRequest{session_id}`. No `logical_session_ref`, `connection_type`, `connection_id`, `extension`, or `backend_types` contract remains in the implementation steps.
- Verification: Each task has a local test command, and the final task compiles the application binary.
