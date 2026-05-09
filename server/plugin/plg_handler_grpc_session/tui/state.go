package tui

import (
	"path"
	"strings"

	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Phase string

const (
	PhaseBackendSelect Phase = "backend_select"
	PhaseParams        Phase = "params"
	PhaseBrowser       Phase = "browser"
	PhaseModal         Phase = "modal"
)

type ModalKind string

const (
	ModalOverwriteConfirm ModalKind = "overwrite_confirm"
)

type Modal struct {
	Kind    ModalKind
	Message string
}

type Action int

const (
	ActionUnknown Action = iota
	ActionRemove
	ActionMkdir
	ActionTouch
	ActionRename
	ActionWrite
)

type State struct {
	Phase         Phase
	BackendType   string
	Params        map[string]string
	CurrentPath   string
	Mode          pb.AccessMode
	EffectiveMode pb.AccessMode
	Lease         *pb.LeaseOptions
	ExternalRef   *pb.ExternalRef
	Modal         Modal
	Status        string
}

func NewState() *State {
	return &State{
		Phase:         PhaseBackendSelect,
		CurrentPath:   "/",
		Params:        make(map[string]string),
		Mode:          pb.AccessMode_ACCESS_MODE_READ_WRITE,
		EffectiveMode: pb.AccessMode_ACCESS_MODE_READ_WRITE,
	}
}

func (s *State) SelectBackend(backend string) {
	s.BackendType = strings.ToLower(strings.TrimSpace(backend))
	s.Params = make(map[string]string)
	s.Phase = PhaseParams
}

func (s *State) SetParam(key, value string) {
	if s.Params == nil {
		s.Params = make(map[string]string)
	}
	s.Params[key] = value
}

func (s *State) BuildOpenRequest() *pb.OpenRequest {
	params := make(map[string]string, len(s.Params))
	for key, value := range s.Params {
		params[key] = value
	}

	rootPath := normalizeAbsolutePath(s.CurrentPath)

	request := &pb.OpenRequest{
		BackendType:   s.BackendType,
		BackendParams: params,
		RootPath:      rootPath,
		Mode:          s.Mode,
	}

	if request.Mode == pb.AccessMode_ACCESS_MODE_UNSPECIFIED {
		request.Mode = pb.AccessMode_ACCESS_MODE_READ_WRITE
	}
	if s.Lease != nil {
		request.Lease = s.Lease
	}
	if s.ExternalRef != nil {
		request.ExternalRef = s.ExternalRef
	}

	return request
}

func (s *State) GoParent() {
	clean := normalizeAbsolutePath(s.CurrentPath)
	if clean == "." || clean == "/" {
		s.CurrentPath = "/"
		return
	}

	parent := path.Dir(clean)
	if parent == "." || parent == "/" {
		s.CurrentPath = "/"
		return
	}

	s.CurrentPath = parent
}

func normalizeAbsolutePath(input string) string {
	clean := strings.TrimSpace(input)
	if clean == "" {
		return "/"
	}
	if !path.IsAbs(clean) {
		clean = "/" + clean
	}
	clean = path.Clean(clean)
	if clean == "." {
		return "/"
	}
	return clean
}

func (s *State) CanMutate(action Action) bool {
	if s.EffectiveMode != pb.AccessMode_ACCESS_MODE_READ_WRITE {
		return false
	}
	switch action {
	case ActionRemove, ActionMkdir, ActionTouch, ActionRename, ActionWrite:
		return true
	default:
		return false
	}
}

func (s *State) HandleUploadError(err error) {
	if err == nil {
		return
	}

	if isUploadConflict(err) {
		s.Phase = PhaseModal
		s.Modal = Modal{
			Kind:    ModalOverwriteConfirm,
			Message: err.Error(),
		}
	}
}

func isUploadConflict(err error) bool {
	st, ok := status.FromError(err)
	if ok && st.Code() == codes.AlreadyExists {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "already exists")
}
