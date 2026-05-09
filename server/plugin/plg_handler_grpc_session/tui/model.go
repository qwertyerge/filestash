package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	tea "charm.land/bubbletea/v2"
	sidecarclient "github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/client"
	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
	"google.golang.org/grpc/status"
)

type AppOptions struct {
	Client     sidecarclient.Sidecar
	Connection sidecarclient.Options
	Context    context.Context
}

type PromptKind string

const (
	PromptNone        PromptKind = ""
	PromptParam       PromptKind = "param"
	PromptRoot        PromptKind = "root"
	PromptJump        PromptKind = "jump"
	PromptMkdir       PromptKind = "mkdir"
	PromptTouch       PromptKind = "touch"
	PromptRename      PromptKind = "rename"
	PromptRemove      PromptKind = "remove"
	PromptWriteLocal  PromptKind = "write_local"
	PromptWriteRemote PromptKind = "write_remote"
	PromptOverwrite   PromptKind = "overwrite"
	PromptForceID     PromptKind = "force_id"
	PromptForceReason PromptKind = "force_reason"
)

type Prompt struct {
	Kind   PromptKind
	Label  string
	Value  string
	Key    string
	Target string
}

type Model struct {
	State      *State
	Client     sidecarclient.Sidecar
	Connection sidecarclient.Options
	Entries    []*pb.FileInfo
	Sessions   []*pb.SessionInfo
	Cursor     int
	Prompt     Prompt
	Pager      string

	paramFields        []Field
	paramIndex         int
	selectedBackend    int
	pendingWriteLocal  string
	pendingWriteRemote string
	pendingForceID     string
}

type openSuccessMsg struct {
	Response *pb.OpenResponse
}

type closeSuccessMsg struct {
	SessionID string
	Quit      bool
}

type listSuccessMsg struct {
	Entries []*pb.FileInfo
}

type mutationSuccessMsg struct {
	Action string
}

type statSuccessMsg struct {
	File *pb.FileInfo
}

type readSuccessMsg struct {
	Path string
	Data []byte
}

type writeSuccessMsg struct {
	Path  string
	Bytes int64
}

type renewSuccessMsg struct {
	Lease *pb.SessionLease
}

type sessionSuccessMsg struct {
	Session *pb.SessionInfo
}

type sessionsSuccessMsg struct {
	Sessions []*pb.SessionInfo
}

type forceCloseSuccessMsg struct {
	SessionID string
}

type errorMsg struct {
	Action string
	Err    error
}

func NewModel(opts AppOptions) Model {
	state := NewState()
	return Model{
		State:      state,
		Client:     opts.Client,
		Connection: opts.Connection,
	}
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m *Model) Shutdown(ctx context.Context) {
	if m == nil || m.Client == nil || m.State == nil || m.State.SessionID == "" {
		return
	}
	sessionID := m.State.SessionID
	if err := m.Client.Close(ctx, sessionID); err != nil {
		m.State.Status = formatError("close", err)
		return
	}
	m.State.SessionID = ""
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case openSuccessMsg:
		return m.handleOpenSuccess(msg)
	case closeSuccessMsg:
		m.State.SessionID = ""
		m.State.Status = "session closed"
		if msg.Quit {
			return m, tea.Quit
		}
		return m, nil
	case listSuccessMsg:
		m.Entries = msg.Entries
		m.Cursor = clampCursor(m.Cursor, len(m.Entries))
		m.State.Phase = PhaseBrowser
		m.State.Status = fmt.Sprintf("%d entries", len(m.Entries))
		return m, nil
	case mutationSuccessMsg:
		m.State.Status = msg.Action
		return m, m.listCmd()
	case statSuccessMsg:
		if msg.File != nil {
			m.State.Status = fmt.Sprintf("%s %s %d bytes", msg.File.GetType(), msg.File.GetName(), msg.File.GetSize())
		}
		return m, nil
	case readSuccessMsg:
		m.Pager = string(msg.Data)
		m.State.Status = fmt.Sprintf("read %s", msg.Path)
		return m, nil
	case writeSuccessMsg:
		m.State.Status = fmt.Sprintf("wrote %d bytes to %s", msg.Bytes, msg.Path)
		m.clearWrite()
		return m, m.listCmd()
	case renewSuccessMsg:
		m.State.Status = "session renewed"
		return m, nil
	case sessionSuccessMsg:
		if msg.Session != nil {
			m.State.Status = fmt.Sprintf("%s %s", msg.Session.GetSessionId(), msg.Session.GetState())
		}
		return m, nil
	case sessionsSuccessMsg:
		m.Sessions = msg.Sessions
		m.State.Status = fmt.Sprintf("%d sessions", len(msg.Sessions))
		return m, nil
	case forceCloseSuccessMsg:
		m.State.Status = fmt.Sprintf("force closed %s", msg.SessionID)
		return m, nil
	case errorMsg:
		if msg.Action == "write" && isUploadConflict(msg.Err) {
			m.State.HandleUploadError(msg.Err)
			m.Prompt = Prompt{Kind: PromptOverwrite, Label: "overwrite? y/N", Target: m.pendingWriteRemote}
			return m, nil
		}
		m.State.Status = formatError(msg.Action, msg.Err)
		return m, nil
	default:
		return m, nil
	}
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.Prompt.Kind != PromptNone {
		return m.handlePromptKey(msg)
	}
	key := msg.String()
	switch m.State.Phase {
	case PhaseBackendSelect:
		return m.handleBackendSelectKey(key)
	case PhaseParams:
		return m.handlePromptKey(msg)
	case PhaseBrowser, PhaseModal:
		return m.handleBrowserKey(key)
	default:
		return m, nil
	}
}

func (m Model) handleBackendSelectKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.selectedBackend > 0 {
			m.selectedBackend--
		}
	case "down", "j":
		if m.selectedBackend < len(DefaultBackendTypes)-1 {
			m.selectedBackend++
		}
	case "w":
		m.toggleMode()
	case "enter":
		backend := DefaultBackendTypes[m.selectedBackend]
		m.State.SelectBackend(backend)
		m.paramFields = BackendFields(backend)
		m.paramIndex = 0
		return m.nextParamPrompt()
	}
	return m, nil
}

func (m Model) handleBrowserKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q", "ctrl+c":
		if m.State.SessionID == "" {
			return m, tea.Quit
		}
		return m, m.closeCmd(true)
	case "c":
		if m.State.SessionID == "" {
			m.State.Status = "no active session"
			return m, nil
		}
		return m, m.closeCmd(false)
	case "up", "k":
		if m.Cursor > 0 {
			m.Cursor--
		}
	case "down", "j":
		if m.Cursor < len(m.Entries)-1 {
			m.Cursor++
		}
	case "enter":
		entry := m.selectedEntry()
		if entry == nil {
			return m, nil
		}
		if entry.GetType() == "directory" {
			m.State.CurrentPath = normalizeAbsolutePath(path.Join(m.State.CurrentPath, entry.GetName()))
			m.Cursor = 0
			return m, m.listCmd()
		}
		return m, m.readCmd(m.selectedRemotePath(), 0, 0)
	case "backspace", "h":
		m.State.GoParent()
		m.Cursor = 0
		return m, m.listCmd()
	case "r":
		return m, m.listCmd()
	case "/":
		m.Prompt = Prompt{Kind: PromptJump, Label: "path", Value: m.State.CurrentPath}
	case "i":
		if remote := m.selectedRemotePath(); remote != "" {
			return m, m.statCmd(remote)
		}
	case "v":
		if remote := m.selectedRemotePath(); remote != "" {
			return m, m.readCmd(remote, 0, 0)
		}
	case "n":
		if !m.ensureMutate(ActionMkdir) {
			return m, nil
		}
		m.Prompt = Prompt{Kind: PromptMkdir, Label: "directory name"}
	case "t":
		if !m.ensureMutate(ActionTouch) {
			return m, nil
		}
		m.Prompt = Prompt{Kind: PromptTouch, Label: "file name"}
	case "R":
		if !m.ensureMutate(ActionRename) {
			return m, nil
		}
		if remote := m.selectedRemotePath(); remote != "" {
			m.Prompt = Prompt{Kind: PromptRename, Label: "new name", Target: remote}
		}
	case "d":
		if !m.ensureMutate(ActionRemove) {
			return m, nil
		}
		if remote := m.selectedRemotePath(); remote != "" {
			m.Prompt = Prompt{Kind: PromptRemove, Label: "remove? y/N", Target: remote}
		}
	case "u":
		if !m.ensureMutate(ActionWrite) {
			return m, nil
		}
		m.Prompt = Prompt{Kind: PromptWriteLocal, Label: "local source path"}
	case "e":
		return m, m.renewCmd()
	case "s":
		return m, m.getSessionCmd()
	case "S":
		return m, m.listSessionsCmd()
	case "F":
		m.Prompt = Prompt{Kind: PromptForceID, Label: "session id"}
	case "w":
		m.State.Status = "access mode is fixed after open"
	case "esc":
		m.Pager = ""
		m.State.Phase = PhaseBrowser
		m.State.Modal = Modal{}
	}
	return m, nil
}

func (m Model) handlePromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.Prompt = Prompt{}
		m.State.Phase = PhaseBrowser
		return m, nil
	case "backspace":
		m.Prompt.Value = trimLastRune(m.Prompt.Value)
		return m, nil
	case "enter":
		return m.submitPrompt()
	default:
		if text := msg.Key().Text; text != "" {
			m.Prompt.Value += text
		}
		return m, nil
	}
}

func (m Model) submitPrompt() (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(m.Prompt.Value)
	switch m.Prompt.Kind {
	case PromptParam:
		if value != "" {
			m.State.SetParam(m.Prompt.Key, value)
		}
		m.paramIndex++
		return m.nextParamPrompt()
	case PromptRoot:
		if value != "" {
			m.State.CurrentPath = normalizeAbsolutePath(value)
		}
		m.Prompt = Prompt{}
		return m, m.openCmd()
	case PromptJump:
		if value != "" {
			m.State.CurrentPath = normalizeAbsolutePath(value)
			m.Cursor = 0
		}
		m.Prompt = Prompt{}
		return m, m.listCmd()
	case PromptMkdir:
		m.Prompt = Prompt{}
		if value == "" {
			return m, nil
		}
		return m, m.mkdirCmd(joinRemote(m.State.CurrentPath, value))
	case PromptTouch:
		m.Prompt = Prompt{}
		if value == "" {
			return m, nil
		}
		return m, m.touchCmd(joinRemote(m.State.CurrentPath, value))
	case PromptRename:
		from := m.Prompt.Target
		m.Prompt = Prompt{}
		if value == "" {
			return m, nil
		}
		return m, m.renameCmd(from, joinRemote(m.State.CurrentPath, value))
	case PromptRemove:
		target := m.Prompt.Target
		m.Prompt = Prompt{}
		if strings.EqualFold(value, "y") || strings.EqualFold(value, "yes") {
			return m, m.removeCmd(target)
		}
	case PromptWriteLocal:
		m.pendingWriteLocal = value
		m.Prompt = Prompt{Kind: PromptWriteRemote, Label: "remote destination", Value: path.Base(value)}
	case PromptWriteRemote:
		m.pendingWriteRemote = joinRemote(m.State.CurrentPath, value)
		m.Prompt = Prompt{}
		return m, m.writeCmd(false)
	case PromptOverwrite:
		m.Prompt = Prompt{}
		if strings.EqualFold(value, "y") || strings.EqualFold(value, "yes") {
			return m, m.writeCmd(true)
		}
		m.clearWrite()
	case PromptForceID:
		m.pendingForceID = value
		m.Prompt = Prompt{Kind: PromptForceReason, Label: "reason"}
	case PromptForceReason:
		sessionID := m.pendingForceID
		m.pendingForceID = ""
		m.Prompt = Prompt{}
		if sessionID != "" {
			return m, m.forceCloseCmd(sessionID, value)
		}
	}
	return m, nil
}

func (m Model) nextParamPrompt() (tea.Model, tea.Cmd) {
	for m.paramIndex < len(m.paramFields) {
		field := m.paramFields[m.paramIndex]
		m.Prompt = Prompt{Kind: PromptParam, Label: field.Name, Key: field.Name}
		return m, nil
	}
	m.Prompt = Prompt{Kind: PromptRoot, Label: "root path", Value: m.State.CurrentPath}
	return m, nil
}

func (m Model) handleOpenSuccess(msg openSuccessMsg) (tea.Model, tea.Cmd) {
	resp := msg.Response
	m.State.SessionID = resp.GetSessionId()
	m.State.EffectiveMode = resp.GetEffectiveMode()
	if m.State.EffectiveMode == pb.AccessMode_ACCESS_MODE_UNSPECIFIED {
		m.State.EffectiveMode = m.State.Mode
	}
	m.State.Phase = PhaseBrowser
	m.State.Status = "session opened"
	return m, m.listCmd()
}

func (m Model) openCmd() tea.Cmd {
	client := m.Client
	req := m.State.BuildOpenRequest()
	return func() tea.Msg {
		resp, err := client.Open(context.Background(), req)
		if err != nil {
			return errorMsg{Action: "open", Err: err}
		}
		return openSuccessMsg{Response: resp}
	}
}

func (m Model) closeCmd(quit bool) tea.Cmd {
	client := m.Client
	sessionID := m.State.SessionID
	return func() tea.Msg {
		if err := client.Close(context.Background(), sessionID); err != nil {
			return errorMsg{Action: "close", Err: err}
		}
		return closeSuccessMsg{SessionID: sessionID, Quit: quit}
	}
}

func (m Model) listCmd() tea.Cmd {
	client := m.Client
	sessionID := m.State.SessionID
	remote := remotePath(m.State.CurrentPath)
	return func() tea.Msg {
		entries, err := client.List(context.Background(), sessionID, remote)
		if err != nil {
			return errorMsg{Action: "list", Err: err}
		}
		return listSuccessMsg{Entries: entries}
	}
}

func (m Model) statCmd(remote string) tea.Cmd {
	client := m.Client
	sessionID := m.State.SessionID
	return func() tea.Msg {
		file, err := client.Stat(context.Background(), sessionID, remote)
		if err != nil {
			return errorMsg{Action: "stat", Err: err}
		}
		return statSuccessMsg{File: file}
	}
}

func (m Model) readCmd(remote string, offset, limit int64) tea.Cmd {
	client := m.Client
	sessionID := m.State.SessionID
	return func() tea.Msg {
		data, err := client.ReadFile(context.Background(), sessionID, remote, offset, limit)
		if err != nil {
			return errorMsg{Action: "read", Err: err}
		}
		return readSuccessMsg{Path: remote, Data: data}
	}
}

func (m Model) mkdirCmd(remote string) tea.Cmd {
	client := m.Client
	sessionID := m.State.SessionID
	return func() tea.Msg {
		if err := client.Mkdir(context.Background(), sessionID, remote); err != nil {
			return errorMsg{Action: "mkdir", Err: err}
		}
		return mutationSuccessMsg{Action: "created " + remote}
	}
}

func (m Model) touchCmd(remote string) tea.Cmd {
	client := m.Client
	sessionID := m.State.SessionID
	return func() tea.Msg {
		if err := client.Touch(context.Background(), sessionID, remote); err != nil {
			return errorMsg{Action: "touch", Err: err}
		}
		return mutationSuccessMsg{Action: "touched " + remote}
	}
}

func (m Model) removeCmd(remote string) tea.Cmd {
	client := m.Client
	sessionID := m.State.SessionID
	return func() tea.Msg {
		if err := client.Remove(context.Background(), sessionID, remote); err != nil {
			return errorMsg{Action: "remove", Err: err}
		}
		return mutationSuccessMsg{Action: "removed " + remote}
	}
}

func (m Model) renameCmd(from, to string) tea.Cmd {
	client := m.Client
	sessionID := m.State.SessionID
	return func() tea.Msg {
		if err := client.Rename(context.Background(), sessionID, from, to); err != nil {
			return errorMsg{Action: "rename", Err: err}
		}
		return mutationSuccessMsg{Action: "renamed " + from}
	}
}

func (m Model) writeCmd(overwrite bool) tea.Cmd {
	client := m.Client
	sessionID := m.State.SessionID
	local := m.pendingWriteLocal
	remote := m.pendingWriteRemote
	return func() tea.Msg {
		file, err := os.Open(local)
		if err != nil {
			return errorMsg{Action: "write", Err: err}
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return errorMsg{Action: "write", Err: err}
		}
		written, err := client.WriteFile(context.Background(), sessionID, remote, file, info.Size(), overwrite)
		if err != nil {
			return errorMsg{Action: "write", Err: err}
		}
		return writeSuccessMsg{Path: remote, Bytes: written}
	}
}

func (m Model) renewCmd() tea.Cmd {
	client := m.Client
	sessionID := m.State.SessionID
	return func() tea.Msg {
		lease, err := client.Renew(context.Background(), sessionID)
		if err != nil {
			return errorMsg{Action: "renew", Err: err}
		}
		return renewSuccessMsg{Lease: lease}
	}
}

func (m Model) getSessionCmd() tea.Cmd {
	client := m.Client
	sessionID := m.State.SessionID
	return func() tea.Msg {
		session, err := client.GetSession(context.Background(), sessionID)
		if err != nil {
			return errorMsg{Action: "session", Err: err}
		}
		return sessionSuccessMsg{Session: session}
	}
}

func (m Model) listSessionsCmd() tea.Cmd {
	client := m.Client
	return func() tea.Msg {
		sessions, err := client.ListSessions(context.Background(), true)
		if err != nil {
			return errorMsg{Action: "sessions", Err: err}
		}
		return sessionsSuccessMsg{Sessions: sessions}
	}
}

func (m Model) forceCloseCmd(sessionID, reason string) tea.Cmd {
	client := m.Client
	return func() tea.Msg {
		if err := client.ForceClose(context.Background(), sessionID, reason); err != nil {
			return errorMsg{Action: "force close", Err: err}
		}
		return forceCloseSuccessMsg{SessionID: sessionID}
	}
}

func (m *Model) toggleMode() {
	if m.State.Mode == pb.AccessMode_ACCESS_MODE_READ {
		m.State.Mode = pb.AccessMode_ACCESS_MODE_READ_WRITE
	} else {
		m.State.Mode = pb.AccessMode_ACCESS_MODE_READ
	}
}

func (m Model) ensureMutate(action Action) bool {
	if !m.State.CanMutate(action) {
		m.State.Status = "read-only session"
		return false
	}
	return true
}

func (m Model) selectedEntry() *pb.FileInfo {
	if m.Cursor < 0 || m.Cursor >= len(m.Entries) {
		return nil
	}
	return m.Entries[m.Cursor]
}

func (m Model) selectedRemotePath() string {
	entry := m.selectedEntry()
	if entry == nil {
		return ""
	}
	return joinRemote(m.State.CurrentPath, entry.GetName())
}

func (m *Model) clearWrite() {
	m.pendingWriteLocal = ""
	m.pendingWriteRemote = ""
}

func clampCursor(cursor, length int) int {
	if length <= 0 {
		return 0
	}
	if cursor < 0 {
		return 0
	}
	if cursor >= length {
		return length - 1
	}
	return cursor
}

func remotePath(absPath string) string {
	clean := normalizeAbsolutePath(absPath)
	if clean == "/" {
		return ""
	}
	return strings.TrimPrefix(clean, "/")
}

func joinRemote(absPath, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return remotePath(absPath)
	}
	base := remotePath(absPath)
	if base == "" {
		return path.Clean(name)
	}
	return path.Clean(path.Join(base, name))
}

func trimLastRune(input string) string {
	if input == "" {
		return ""
	}
	runes := []rune(input)
	return string(runes[:len(runes)-1])
}

func formatError(action string, err error) string {
	if err == nil {
		return action
	}
	if st, ok := status.FromError(err); ok {
		return fmt.Sprintf("%s: %s: %s", action, st.Code(), st.Message())
	}
	if err == io.EOF {
		return action + ": unexpected eof"
	}
	return action + ": " + err.Error()
}
