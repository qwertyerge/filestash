# Sidecar gRPC TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `filestash-sidecar tui`, a Bubble Tea terminal client that connects to sidecar gRPC with mTLS, opens a backend session, and performs all sidecar filesystem operations interactively.

**Architecture:** Keep sidecar service code separate from terminal UI code. Add a client package for config discovery, mTLS dial, and RPC wrappers; add a TUI package for backend forms and Bubble Tea state; wire `cmd/sidecar` to dispatch `serve` and `tui`.

**Tech Stack:** Go 1.26, gRPC, `server/plugin/plg_handler_grpc_session/pb`, `charm.land/bubbletea/v2`, Charmbracelet Bubbles/Lip Gloss if needed, TDD with focused package tests.

---

## File Structure

- Create `server/plugin/plg_handler_grpc_session/client/config.go`: TUI connection options, convention-path config discovery, relative path resolution.
- Create `server/plugin/plg_handler_grpc_session/client/config_test.go`: config discovery and path resolution tests.
- Create `server/plugin/plg_handler_grpc_session/client/dial.go`: mTLS TLS config and gRPC dial helper.
- Create `server/plugin/plg_handler_grpc_session/client/dial_test.go`: certificate loading and TLS server name tests.
- Create `server/plugin/plg_handler_grpc_session/client/rpc.go`: `SidecarClient` interface and wrapper methods for Open, session management, filesystem RPCs, read/write streams.
- Create `server/plugin/plg_handler_grpc_session/client/rpc_test.go`: fake generated client tests for read/write stream behavior.
- Create `server/plugin/plg_handler_grpc_session/tui/backend.go`: backend field schema, masking rules, initial backend list.
- Create `server/plugin/plg_handler_grpc_session/tui/backend_test.go`: backend schema and masking tests.
- Create `server/plugin/plg_handler_grpc_session/tui/state.go`: UI state model independent from Bubble Tea rendering.
- Create `server/plugin/plg_handler_grpc_session/tui/state_test.go`: wizard transitions, file navigation, read-only mutation blocking, conflict confirmation.
- Create `server/plugin/plg_handler_grpc_session/tui/model.go`: Bubble Tea model, messages, update loop, command dispatch.
- Create `server/plugin/plg_handler_grpc_session/tui/view.go`: rendered full-screen views and modal/pager text.
- Create `server/plugin/plg_handler_grpc_session/tui/run.go`: `Run(ctx, Options)` entry point used by `cmd/sidecar`.
- Create `server/plugin/plg_handler_grpc_session/tui/model_test.go`: close-on-quit and command selection tests using fake client.
- Modify `cmd/sidecar/main.go`: command dispatch for default serve, `serve`, and `tui`; flag parsing.
- Modify `packaging/sidecar-grpc/README.md`: document `bin/filestash-sidecar tui`.
- Modify `packaging/sidecar-grpc/scripts/package.sh`: no structural change expected, but re-run to include new binary.

## Task 1: Client Config Discovery

**Files:**
- Create: `server/plugin/plg_handler_grpc_session/client/config.go`
- Test: `server/plugin/plg_handler_grpc_session/client/config_test.go`

- [ ] **Step 1: Write failing tests for package-root discovery**

Add tests that create a temp package directory with `conf/config.json` and cert files, then call `ResolveOptions`.

Expected test shape:

```go
func TestResolveOptionsFindsPackageRootConfig(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "conf", "config.json"), `{
	  "features": {"sidecar_grpc": {
	    "listen_addr": "127.0.0.1:9443",
	    "tls": {"client_ca_file": "conf/certs/client-ca.crt"}
	  }}
	}`)
	writeFile(t, filepath.Join(root, "conf", "certs", "client.crt"), "cert")
	writeFile(t, filepath.Join(root, "conf", "certs", "client.key"), "key")
	writeFile(t, filepath.Join(root, "conf", "certs", "client-ca.crt"), "ca")

	got, err := ResolveOptions(Options{WorkDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != "127.0.0.1:9443" {
		t.Fatalf("addr=%q", got.Addr)
	}
	if got.ConfigFile != filepath.Join(root, "conf", "config.json") {
		t.Fatalf("config=%q", got.ConfigFile)
	}
	if got.ClientCertFile != filepath.Join(root, "conf", "certs", "client.crt") {
		t.Fatalf("cert=%q", got.ClientCertFile)
	}
}
```

- [ ] **Step 2: Run RED**

Run:

```sh
go test ./server/plugin/plg_handler_grpc_session/client -run TestResolveOptionsFindsPackageRootConfig -count=1
```

Expected: fail because the `client` package does not exist.

- [ ] **Step 3: Implement minimal config discovery**

Define:

```go
type Options struct {
    Addr           string
    ConfigFile     string
    ClientCertFile string
    ClientKeyFile  string
    CAFile         string
    ServerName     string
    WorkDir        string
}

func ResolveOptions(in Options) (Options, error)
```

Use `tidwall/gjson` to read nested config. If `WorkDir` is empty, use `os.Getwd()`. Probe `conf/config.json`, `data/state/config/config.json`, `../conf/config.json`, and `FILESTASH_PATH/state/config/config.json`. Default address to `127.0.0.1:9443`.

- [ ] **Step 4: Run GREEN**

Run:

```sh
go test ./server/plugin/plg_handler_grpc_session/client -run TestResolveOptionsFindsPackageRootConfig -count=1
```

Expected: pass.

- [ ] **Step 5: Add RED tests for bin directory, explicit overrides, and relative paths**

Add tests:

```go
func TestResolveOptionsFindsConfigFromBinDirectory(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "conf", "config.json"), `{"features":{"sidecar_grpc":{"listen_addr":"127.0.0.1:9444"}}}`)

	got, err := ResolveOptions(Options{WorkDir: bin})
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigFile != filepath.Join(root, "conf", "config.json") {
		t.Fatalf("config=%q", got.ConfigFile)
	}
	if got.Addr != "127.0.0.1:9444" {
		t.Fatalf("addr=%q", got.Addr)
	}
}

func TestResolveOptionsExplicitFlagsOverrideConfig(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "conf", "config.json"), `{"features":{"sidecar_grpc":{"listen_addr":"127.0.0.1:9444"}}}`)

	got, err := ResolveOptions(Options{
		WorkDir:        root,
		Addr:           "127.0.0.1:9555",
		ClientCertFile: "/tmp/client.crt",
		ClientKeyFile:  "/tmp/client.key",
		CAFile:         "/tmp/ca.crt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != "127.0.0.1:9555" || got.CAFile != "/tmp/ca.crt" {
		t.Fatalf("overrides not preserved: %+v", got)
	}
}

func TestResolveOptionsUsesFallbackAddressWithoutConfig(t *testing.T) {
	got, err := ResolveOptions(Options{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != "127.0.0.1:9443" {
		t.Fatalf("addr=%q", got.Addr)
	}
}
```

The bin-directory test should set `WorkDir` to `<root>/bin` and expect `../conf/config.json`.

- [ ] **Step 6: Run RED/GREEN for added tests**

Run:

```sh
go test ./server/plugin/plg_handler_grpc_session/client -run TestResolveOptions -count=1
```

Expected before implementation: at least one failure. Implement missing precedence and path resolution. Expected after implementation: pass.

- [ ] **Step 7: Commit**

```sh
git add server/plugin/plg_handler_grpc_session/client/config.go server/plugin/plg_handler_grpc_session/client/config_test.go
git commit -m "feat: discover sidecar tui connection config"
```

## Task 2: Client Dial and RPC Wrapper

**Files:**
- Create: `server/plugin/plg_handler_grpc_session/client/dial.go`
- Create: `server/plugin/plg_handler_grpc_session/client/rpc.go`
- Test: `server/plugin/plg_handler_grpc_session/client/dial_test.go`
- Test: `server/plugin/plg_handler_grpc_session/client/rpc_test.go`

- [ ] **Step 1: Write failing TLS config test**

Generate temporary CA/server/client certs using existing test helper patterns from `server/plugin/plg_handler_grpc_session/server_test.go` or local helper code. Test:

```go
func TestTLSConfigLoadsClientCertificateAndCA(t *testing.T) {
    opts := Options{ClientCertFile: clientCert, ClientKeyFile: clientKey, CAFile: caCert, ServerName: "localhost"}
    cfg, err := TLSConfig(opts)
    if err != nil {
        t.Fatal(err)
    }
    if len(cfg.Certificates) != 1 {
        t.Fatalf("certificates=%d", len(cfg.Certificates))
    }
    if cfg.RootCAs == nil || len(cfg.RootCAs.Subjects()) == 0 {
        t.Fatal("missing root CA")
    }
    if cfg.ServerName != "localhost" {
        t.Fatalf("server name=%q", cfg.ServerName)
    }
}
```

- [ ] **Step 2: Run RED**

```sh
go test ./server/plugin/plg_handler_grpc_session/client -run TestTLSConfigLoadsClientCertificateAndCA -count=1
```

Expected: fail because `TLSConfig` does not exist.

- [ ] **Step 3: Implement `TLSConfig` and `Dial`**

Define:

```go
func TLSConfig(opts Options) (*tls.Config, error)
func Dial(ctx context.Context, opts Options) (*grpc.ClientConn, error)
```

`TLSConfig` loads client cert/key and CA, sets `MinVersion: tls.VersionTLS12`, and honors `ServerName`.

- [ ] **Step 4: Run GREEN**

```sh
go test ./server/plugin/plg_handler_grpc_session/client -run TestTLSConfigLoadsClientCertificateAndCA -count=1
```

- [ ] **Step 5: Write failing RPC wrapper tests**

Use fake stream/client types to test:

```go
func TestReadFileCollectsChunks(t *testing.T) {
	fake := &fakeGeneratedClient{readChunks: [][]byte{[]byte("he"), []byte("llo")}}
	client := NewFromGenerated(fake)

	got, err := client.ReadFile(context.Background(), "s1", "note.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("read=%q", got)
	}
}

func TestWriteFileSendsHeaderAndChunks(t *testing.T) {
	fake := &fakeGeneratedClient{}
	client := NewFromGenerated(fake)

	written, err := client.WriteFile(context.Background(), "s1", "out.txt", strings.NewReader("hello"), 5, false)
	if err != nil {
		t.Fatal(err)
	}
	if written != 5 {
		t.Fatalf("written=%d", written)
	}
	if fake.writeHeader.GetSessionId() != "s1" || fake.writeHeader.GetPath() != "out.txt" || fake.writeHeader.GetOverwrite() {
		t.Fatalf("header=%+v", fake.writeHeader)
	}
	if got := string(bytes.Join(fake.writeChunks, nil)); got != "hello" {
		t.Fatalf("chunks=%q", got)
	}
}

func TestWriteFileReturnsConflictForCallerControlledRetry(t *testing.T) {
	fake := &fakeGeneratedClient{writeErr: status.Error(codes.AlreadyExists, "exists")}
	client := NewFromGenerated(fake)

	_, err := client.WriteFile(context.Background(), "s1", "out.txt", strings.NewReader("hello"), 5, false)
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("err=%v", err)
	}
}
```

The wrapper should not hide conflict retry behavior; it should return the gRPC error so the TUI can ask for overwrite confirmation.

- [ ] **Step 6: Implement wrapper**

Define `type Sidecar interface` with methods:

```go
Open(ctx context.Context, req *pb.OpenRequest) (*pb.OpenResponse, error)
Close(ctx context.Context, sessionID string) error
Renew(ctx context.Context, sessionID string) (*pb.SessionLease, error)
GetSession(ctx context.Context, sessionID string) (*pb.SessionInfo, error)
ListSessions(ctx context.Context, includeClosed bool) ([]*pb.SessionInfo, error)
ForceClose(ctx context.Context, sessionID, reason string) error
List(ctx context.Context, sessionID, path string) ([]*pb.FileInfo, error)
Stat(ctx context.Context, sessionID, path string) (*pb.FileInfo, error)
ReadFile(ctx context.Context, sessionID, path string, offset, limit int64) ([]byte, error)
WriteFile(ctx context.Context, sessionID, remotePath string, r io.Reader, expectedSize int64, overwrite bool) (int64, error)
Mkdir(ctx context.Context, sessionID, path string) error
Remove(ctx context.Context, sessionID, path string) error
Rename(ctx context.Context, sessionID, from, to string) error
Touch(ctx context.Context, sessionID, path string) error
```

- [ ] **Step 7: Run tests and commit**

```sh
go test ./server/plugin/plg_handler_grpc_session/client -count=1
git add server/plugin/plg_handler_grpc_session/client
git commit -m "feat: add sidecar tui client wrapper"
```

## Task 3: Backend Form Schema and TUI State Machine

**Files:**
- Create: `server/plugin/plg_handler_grpc_session/tui/backend.go`
- Create: `server/plugin/plg_handler_grpc_session/tui/state.go`
- Test: `server/plugin/plg_handler_grpc_session/tui/backend_test.go`
- Test: `server/plugin/plg_handler_grpc_session/tui/state_test.go`

- [ ] **Step 1: Write failing backend schema tests**

Tests:

```go
func TestBackendFieldsForKnownBackends(t *testing.T) {
    fields := BackendFields("sftp")
    want := []string{"hostname", "port", "username", "password"}
    if got := fieldNames(fields); !reflect.DeepEqual(got, want) {
        t.Fatalf("fields=%v", got)
    }
}

func TestSensitiveFieldsAreMasked(t *testing.T) {
    if !IsSensitiveField("secret_access_key") || !IsSensitiveField("password") {
        t.Fatal("expected sensitive fields to be masked")
    }
}
```

- [ ] **Step 2: Run RED**

```sh
go test ./server/plugin/plg_handler_grpc_session/tui -run 'TestBackendFields|TestSensitiveFields' -count=1
```

Expected: fail because package does not exist.

- [ ] **Step 3: Implement backend schema**

Define `Field`, `BackendFields`, `DefaultBackendTypes`, and `IsSensitiveField`.

- [ ] **Step 4: Run GREEN**

```sh
go test ./server/plugin/plg_handler_grpc_session/tui -run 'TestBackendFields|TestSensitiveFields' -count=1
```

- [ ] **Step 5: Write failing state-machine tests**

Tests:

```go
func TestWizardMovesFromBackendToParamsToOpen(t *testing.T) {
	state := NewState()
	if state.Phase != PhaseBackendSelect {
		t.Fatalf("phase=%s", state.Phase)
	}
	state.SelectBackend("sftp")
	if state.Phase != PhaseParams {
		t.Fatalf("phase=%s", state.Phase)
	}
	state.SetParam("hostname", "127.0.0.1")
	state.SetParam("username", "me")
	state.SetParam("password", "secret")
	req := state.BuildOpenRequest()
	if req.GetBackendType() != "sftp" || req.GetBackendParams()["hostname"] != "127.0.0.1" {
		t.Fatalf("request=%+v", req)
	}
}

func TestBrowserParentPathNavigation(t *testing.T) {
	state := NewState()
	state.CurrentPath = "/a/b"
	state.GoParent()
	if state.CurrentPath != "/a" {
		t.Fatalf("path=%q", state.CurrentPath)
	}
	state.GoParent()
	state.GoParent()
	if state.CurrentPath != "/" {
		t.Fatalf("path=%q", state.CurrentPath)
	}
}

func TestReadOnlyModeBlocksMutatingActions(t *testing.T) {
	state := NewState()
	state.EffectiveMode = pb.AccessMode_ACCESS_MODE_READ
	if state.CanMutate(ActionRemove) {
		t.Fatal("read-only session allowed remove")
	}
}

func TestUploadConflictEntersOverwriteConfirmation(t *testing.T) {
	state := NewState()
	state.HandleUploadError(status.Error(codes.AlreadyExists, "exists"))
	if state.Phase != PhaseModal || state.Modal.Kind != ModalOverwriteConfirm {
		t.Fatalf("phase=%s modal=%+v", state.Phase, state.Modal)
	}
}
```

The state package should expose plain methods that do not require a terminal, such as `SelectBackend`, `SetParam`, `BuildOpenRequest`, `ParentPath`, `CanMutate`, and `HandleUploadError`.

- [ ] **Step 6: Implement minimal state**

Define:

```go
type Phase string
const (
    PhaseBackendSelect Phase = "backend_select"
    PhaseParams        Phase = "params"
    PhaseBrowser       Phase = "browser"
    PhaseModal         Phase = "modal"
)

type State struct {
    Phase         Phase
    BackendType   string
    Params        map[string]string
    CurrentPath   string
    EffectiveMode pb.AccessMode
    Modal         Modal
}
```

Keep this independent from Bubble Tea messages.

- [ ] **Step 7: Run tests and commit**

```sh
go test ./server/plugin/plg_handler_grpc_session/tui -run 'TestBackend|TestSensitive|TestWizard|TestBrowser|TestReadOnly|TestUploadConflict' -count=1
git add server/plugin/plg_handler_grpc_session/tui/backend.go server/plugin/plg_handler_grpc_session/tui/backend_test.go server/plugin/plg_handler_grpc_session/tui/state.go server/plugin/plg_handler_grpc_session/tui/state_test.go
git commit -m "feat: add sidecar tui state model"
```

## Task 4: Bubble Tea Model and Views

**Files:**
- Create: `server/plugin/plg_handler_grpc_session/tui/model.go`
- Create: `server/plugin/plg_handler_grpc_session/tui/view.go`
- Create: `server/plugin/plg_handler_grpc_session/tui/run.go`
- Test: `server/plugin/plg_handler_grpc_session/tui/model_test.go`
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add Bubble Tea dependencies**

Run:

```sh
go get charm.land/bubbletea/v2@latest
go get github.com/charmbracelet/bubbles@latest
go get github.com/charmbracelet/lipgloss@latest
```

If `charm.land/bubbletea/v2` and Bubbles major versions conflict, prefer Bubble Tea's current documented import path for the direct model and use only compatible Bubbles components, or avoid Bubbles and keep simple custom list rendering.

- [ ] **Step 2: Write failing model tests**

Use a fake sidecar client and call `Update` directly:

```go
func TestQuitWithActiveSessionRequestsClose(t *testing.T) {
	fake := &fakeSidecar{}
	model := NewModel(AppOptions{Client: fake})
	model.State.SessionID = "s1"

	updated, cmd := model.Update(keyMsg("q"))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected close command")
	}
	runCmd(t, cmd)
	if fake.closedSession != "s1" {
		t.Fatalf("closed=%q", fake.closedSession)
	}
}

func TestReadOnlyMutationKeyDoesNotDispatchRPC(t *testing.T) {
	fake := &fakeSidecar{}
	model := NewModel(AppOptions{Client: fake})
	model.State.EffectiveMode = pb.AccessMode_ACCESS_MODE_READ

	updated, cmd := model.Update(keyMsg("d"))
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("unexpected RPC command")
	}
	if fake.removeCalls != 0 || !strings.Contains(model.State.Status, "read-only") {
		t.Fatalf("removeCalls=%d status=%q", fake.removeCalls, model.State.Status)
	}
}

func TestOpenSuccessLoadsRootDirectory(t *testing.T) {
	fake := &fakeSidecar{openSessionID: "s1", listEntries: []*pb.FileInfo{{Name: "file.txt", Type: "file"}}}
	model := NewModel(AppOptions{Client: fake})

	msg := openSuccessMsg{Response: &pb.OpenResponse{SessionId: "s1", EffectiveMode: pb.AccessMode_ACCESS_MODE_READ_WRITE}}
	updated, cmd := model.Update(msg)
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected list command after open")
	}
	runCmd(t, cmd)
	if model.State.SessionID != "s1" {
		t.Fatalf("session=%q", model.State.SessionID)
	}
}
```

- [ ] **Step 3: Run RED**

```sh
CGO_ENABLED=0 go test ./server/plugin/plg_handler_grpc_session/tui -run 'TestQuit|TestReadOnlyMutation|TestOpenSuccess' -count=1
```

Expected: fail because Bubble Tea model does not exist.

- [ ] **Step 4: Implement Bubble Tea model**

Implement:

```go
type AppOptions struct {
    Client sidecarclient.Sidecar
    Connection sidecarclient.Options
}

func NewModel(opts AppOptions) Model
func Run(ctx context.Context, opts AppOptions) error
```

Use `tea.Cmd` for RPC calls. Keep all RPC side effects inside commands; keep update logic deterministic.

- [ ] **Step 5: Implement views**

Render text-only full-screen views:

- connect/open wizard
- backend params
- file browser
- modal/pager
- session list

Use stable dimensions and avoid relying on terminal width for correctness.

- [ ] **Step 6: Run GREEN and commit**

```sh
CGO_ENABLED=0 go test ./server/plugin/plg_handler_grpc_session/tui -count=1
git add go.mod go.sum server/plugin/plg_handler_grpc_session/tui
git commit -m "feat: add bubble tea sidecar tui"
```

## Task 5: Command Dispatch and Packaging

**Files:**
- Modify: `cmd/sidecar/main.go`
- Modify: `packaging/sidecar-grpc/README.md`
- Test: `cmd/sidecar/main_test.go`

- [ ] **Step 1: Write failing command parsing tests**

Extract parsing into a testable function:

```go
func TestParseSidecarCommandDefaultsToServe(t *testing.T) {
	cmd, opts, err := parseSidecarCommand(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cmd != commandServe || opts.Addr != "" {
		t.Fatalf("cmd=%v opts=%+v", cmd, opts)
	}
}

func TestParseSidecarCommandAcceptsTUIFlags(t *testing.T) {
	cmd, opts, err := parseSidecarCommand([]string{
		"tui",
		"--addr", "127.0.0.1:9444",
		"--config", "/tmp/config.json",
		"--cert", "/tmp/client.crt",
		"--key", "/tmp/client.key",
		"--ca", "/tmp/ca.crt",
		"--server-name", "localhost",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cmd != commandTUI || opts.Addr != "127.0.0.1:9444" || opts.ServerName != "localhost" {
		t.Fatalf("cmd=%v opts=%+v", cmd, opts)
	}
}
```

- [ ] **Step 2: Run RED**

```sh
CGO_ENABLED=0 go test ./cmd/sidecar -run TestParseSidecarCommand -count=1
```

Expected: fail because parse function does not exist.

- [ ] **Step 3: Implement command dispatch**

Behavior:

- `filestash-sidecar` -> serve
- `filestash-sidecar serve` -> serve
- `filestash-sidecar tui --addr 127.0.0.1:9443 --config conf/config.json --cert conf/certs/client.crt --key conf/certs/client.key --ca conf/certs/client-ca.crt --server-name localhost` -> run TUI

On TUI startup:

```go
resolved, err := sidecarclient.ResolveOptions(parsedOptions)
conn, err := sidecarclient.Dial(ctx, resolved)
app := tui.NewModel(tui.AppOptions{Client: sidecarclient.New(conn), Connection: resolved})
return tui.Run(ctx, app)
```

- [ ] **Step 4: Update package README**

Document:

```sh
./scripts/gen-etls-certs.sh
./bin/start.sh
./bin/filestash-sidecar tui
```

Also document flags and convention paths.

- [ ] **Step 5: Run tests and rebuild package**

```sh
CGO_ENABLED=0 go test ./cmd/sidecar ./server/plugin/plg_handler_grpc_session/client ./server/plugin/plg_handler_grpc_session/tui ./server/plugin/plg_handler_grpc_session -count=1
./packaging/sidecar-grpc/scripts/package.sh
```

- [ ] **Step 6: Commit**

```sh
git add cmd/sidecar/main.go cmd/sidecar/main_test.go packaging/sidecar-grpc/README.md
git commit -m "feat: wire sidecar tui command"
```

## Task 6: End-to-End Verification and Final Review

**Files:**
- No planned source edits unless verification finds bugs.

- [ ] **Step 1: Run full focused verification**

```sh
CGO_ENABLED=0 go test ./cmd/sidecar ./server/plugin/plg_handler_grpc_session/client ./server/plugin/plg_handler_grpc_session/tui ./server/plugin/plg_handler_grpc_session -count=1
CGO_ENABLED=0 go vet ./cmd/sidecar ./server/plugin/plg_handler_grpc_session/client ./server/plugin/plg_handler_grpc_session/tui
```

- [ ] **Step 2: Package and smoke**

```sh
./packaging/sidecar-grpc/scripts/package.sh
cd dist/filestash-sidecar-grpc
./scripts/gen-etls-certs.sh
./bin/start.sh
```

Start smoke in a subprocess, wait for `[sidecar_grpc] listening`, then terminate.

- [ ] **Step 3: TUI command smoke without taking over the terminal**

Run a non-interactive parse/startup test through `go test`. Do not run a full-screen TUI in CI unless Bubble Tea supports a test renderer path used by the tests.

- [ ] **Step 4: Final code review**

Dispatch a final review agent scoped to:

- config discovery security and path precedence
- mTLS client correctness
- no credential leaks in UI/status strings
- read-only mutation blocking
- close-on-quit lifecycle
- package output

- [ ] **Step 5: Final commit if fixes were needed**

Only commit if Step 4 requires changes:

```sh
git add <changed files>
git commit -m "fix: harden sidecar tui"
```

## Self-Review

- Spec coverage: every spec section maps to a task. Entry point and packaging are Task 5; config discovery is Task 1; mTLS dial and RPC wrappers are Task 2; backend forms and state machine are Task 3; Bubble Tea UI and file operations are Task 4; verification is Task 6.
- Placeholder scan: no incomplete marker text remains. Code snippets define expected APIs and test shapes.
- Type consistency: `client.Options`, `client.Sidecar`, `tui.AppOptions`, `tui.NewModel`, and `tui.Run` are used consistently across tasks.
