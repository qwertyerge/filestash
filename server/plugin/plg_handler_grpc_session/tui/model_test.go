package tui

import (
	"context"
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	sidecarclient "github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/client"
	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
)

func TestQuitWithActiveSessionRequestsClose(t *testing.T) {
	fake := &fakeSidecar{}
	model := NewModel(AppOptions{Client: fake})
	model.State.SessionID = "s1"
	model.State.Phase = PhaseBrowser

	updated, cmd := model.Update(keyMsg("q"))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected close command")
	}

	msg := cmd()
	if fake.closedSession != "s1" {
		t.Fatalf("closed=%q", fake.closedSession)
	}

	updated, cmd = model.Update(msg)
	model = updated.(Model)
	if model.State.SessionID != "" {
		t.Fatalf("session=%q", model.State.SessionID)
	}
	if cmd == nil {
		t.Fatal("expected quit command after close")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("quit command returned %T", cmd())
	}
}

func TestReadOnlyMutationKeyDoesNotDispatchRPC(t *testing.T) {
	fake := &fakeSidecar{}
	model := NewModel(AppOptions{Client: fake})
	model.State.Phase = PhaseBrowser
	model.State.SessionID = "s1"
	model.State.EffectiveMode = pb.AccessMode_ACCESS_MODE_READ
	model.Entries = []*pb.FileInfo{{Name: "old.txt", Type: "file"}}

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
	fake := &fakeSidecar{listEntries: []*pb.FileInfo{{Name: "file.txt", Type: "file"}}}
	model := NewModel(AppOptions{Client: fake})

	updated, cmd := model.Update(openSuccessMsg{
		Response: &pb.OpenResponse{
			SessionId:     "s1",
			EffectiveMode: pb.AccessMode_ACCESS_MODE_READ_WRITE,
		},
	})
	model = updated.(Model)
	if model.State.SessionID != "s1" {
		t.Fatalf("session=%q", model.State.SessionID)
	}
	if cmd == nil {
		t.Fatal("expected list command after open")
	}

	msg := cmd()
	if fake.listSession != "s1" || fake.listPath != "" {
		t.Fatalf("list session=%q path=%q", fake.listSession, fake.listPath)
	}

	updated, _ = model.Update(msg)
	model = updated.(Model)
	if len(model.Entries) != 1 || model.Entries[0].GetName() != "file.txt" {
		t.Fatalf("entries=%v", model.Entries)
	}
}

func TestMkdirPromptDispatchesMutationAndRefresh(t *testing.T) {
	fake := &fakeSidecar{}
	model := NewModel(AppOptions{Client: fake})
	model.State.Phase = PhaseBrowser
	model.State.SessionID = "s1"
	model.State.EffectiveMode = pb.AccessMode_ACCESS_MODE_READ_WRITE

	updated, cmd := model.Update(keyMsg("n"))
	model = updated.(Model)
	if cmd != nil || model.Prompt.Kind != PromptMkdir {
		t.Fatalf("cmd=%v prompt=%+v", cmd, model.Prompt)
	}

	model = updateWithKeys(t, model, "docs")
	updated, cmd = model.Update(keyMsg("enter"))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected mkdir command")
	}

	msg := cmd()
	if fake.mkdirPath != "docs" {
		t.Fatalf("mkdir=%q", fake.mkdirPath)
	}

	updated, cmd = model.Update(msg)
	model = updated.(Model)
	if !strings.Contains(model.State.Status, "created") {
		t.Fatalf("status=%q", model.State.Status)
	}
	if cmd == nil {
		t.Fatal("expected refresh command after mkdir")
	}
}

func updateWithKeys(t *testing.T, model Model, keys string) Model {
	t.Helper()
	for _, r := range keys {
		updated, cmd := model.Update(keyMsg(string(r)))
		if cmd != nil {
			t.Fatalf("unexpected command for key %q", r)
		}
		model = updated.(Model)
	}
	return model
}

func keyMsg(key string) tea.KeyPressMsg {
	switch key {
	case "enter":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	case "esc":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc})
	case "up":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyUp})
	case "down":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyDown})
	case "backspace":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace})
	default:
		runes := []rune(key)
		if len(runes) == 0 {
			return tea.KeyPressMsg(tea.Key{})
		}
		return tea.KeyPressMsg(tea.Key{Text: key, Code: runes[0]})
	}
}

var _ sidecarclient.Sidecar = (*fakeSidecar)(nil)

type fakeSidecar struct {
	openReq       *pb.OpenRequest
	closedSession string
	removeCalls   int
	mkdirPath     string
	listSession   string
	listPath      string
	listEntries   []*pb.FileInfo
}

func (f *fakeSidecar) Open(ctx context.Context, req *pb.OpenRequest) (*pb.OpenResponse, error) {
	f.openReq = req
	return &pb.OpenResponse{SessionId: "s1", EffectiveMode: req.GetMode()}, nil
}

func (f *fakeSidecar) Close(ctx context.Context, sessionID string) error {
	f.closedSession = sessionID
	return nil
}

func (f *fakeSidecar) Renew(ctx context.Context, sessionID string) (*pb.SessionLease, error) {
	return &pb.SessionLease{SessionId: sessionID}, nil
}

func (f *fakeSidecar) GetSession(ctx context.Context, sessionID string) (*pb.SessionInfo, error) {
	return &pb.SessionInfo{SessionId: sessionID}, nil
}

func (f *fakeSidecar) ListSessions(ctx context.Context, includeClosed bool) ([]*pb.SessionInfo, error) {
	return nil, nil
}

func (f *fakeSidecar) ForceClose(ctx context.Context, sessionID, reason string) error {
	return nil
}

func (f *fakeSidecar) List(ctx context.Context, sessionID, path string) ([]*pb.FileInfo, error) {
	f.listSession = sessionID
	f.listPath = path
	return f.listEntries, nil
}

func (f *fakeSidecar) Stat(ctx context.Context, sessionID, path string) (*pb.FileInfo, error) {
	return &pb.FileInfo{Name: path}, nil
}

func (f *fakeSidecar) ReadFile(ctx context.Context, sessionID, path string, offset, limit int64) ([]byte, error) {
	return []byte("content"), nil
}

func (f *fakeSidecar) WriteFile(ctx context.Context, sessionID, path string, r io.Reader, expectedSize int64, overwrite bool) (int64, error) {
	return 0, nil
}

func (f *fakeSidecar) Mkdir(ctx context.Context, sessionID, path string) error {
	f.mkdirPath = path
	return nil
}

func (f *fakeSidecar) Remove(ctx context.Context, sessionID, path string) error {
	f.removeCalls++
	return nil
}

func (f *fakeSidecar) Rename(ctx context.Context, sessionID, from, to string) error {
	return nil
}

func (f *fakeSidecar) Touch(ctx context.Context, sessionID, path string) error {
	return nil
}
