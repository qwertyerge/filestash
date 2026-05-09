package tui

import (
	"testing"

	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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

	request := state.BuildOpenRequest()
	if request.GetBackendType() != "sftp" {
		t.Fatalf("backend=%q", request.GetBackendType())
	}
	if got := request.GetBackendParams()["hostname"]; got != "127.0.0.1" {
		t.Fatalf("hostname=%q", got)
	}
	if got := request.GetBackendParams()["password"]; got != "secret" {
		t.Fatalf("password=%q", got)
	}
	if request.GetRootPath() != "/" {
		t.Fatalf("root=%q", request.GetRootPath())
	}
	if request.GetMode() != pb.AccessMode_ACCESS_MODE_READ_WRITE {
		t.Fatalf("mode=%s", request.GetMode())
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
	if state.CanMutate(ActionMkdir) {
		t.Fatal("read-only session allowed mkdir")
	}
	if state.CanMutate(ActionTouch) {
		t.Fatal("read-only session allowed touch")
	}
	if state.CanMutate(ActionRename) {
		t.Fatal("read-only session allowed rename")
	}
	if state.CanMutate(ActionWrite) {
		t.Fatal("read-only session allowed write")
	}
}

func TestUploadConflictEntersOverwriteConfirmation(t *testing.T) {
	state := NewState()
	state.HandleUploadError(status.Error(codes.AlreadyExists, "already exists"))

	if state.Phase != PhaseModal || state.Modal.Kind != ModalOverwriteConfirm {
		t.Fatalf("phase=%s modal=%+v", state.Phase, state.Modal)
	}
}
