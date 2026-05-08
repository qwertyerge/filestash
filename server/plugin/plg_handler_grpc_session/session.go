package plg_handler_grpc_session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	. "github.com/mickael-kerjean/filestash/server/common"
	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

type externalRef struct {
	kind string
	id   string
}

type sidecarSession struct {
	id             string
	ownerIdentity  string
	backendType    string
	backend        IBackend
	rootPath       string
	redactedTarget string
	mode           pb.AccessMode
	externalRef    externalRef
	openedAt       time.Time
	lastUsedAt     time.Time
	expiresAt      time.Time
	idleExpiresAt  time.Time
	maxExpiresAt   time.Time
	lease          effectiveLease
	maxStreamBytes int64
	state          pb.SessionState
	ctx            context.Context
	cancel         context.CancelFunc
	opsMu          sync.Mutex
	opsCond        *sync.Cond
	inFlightOps    int
	closeOnce      sync.Once
	mu             sync.Mutex
}

type openSessionInput struct {
	ownerIdentity  string
	backendType    string
	backend        IBackend
	rootPath       string
	redactedTarget string
	mode           pb.AccessMode
	externalRef    externalRef
	lease          effectiveLease
	maxStreamBytes int64
	ctx            context.Context
	cancel         context.CancelFunc
	commitCtx      context.Context
	reservation    *openReservation
}

type sessionManagerOptions struct {
	now func() time.Time
	id  func() (string, error)
}

type sessionManager struct {
	mu           sync.Mutex
	sessions     map[string]*sidecarSession
	pendingOwner map[string]int
	pendingTotal int
	shuttingDown bool
	now          func() time.Time
	id           func() (string, error)
}

type openReservation struct {
	manager       *sessionManager
	ownerIdentity string
	released      bool
}

func newSessionManager(opts sessionManagerOptions) *sessionManager {
	now := opts.now
	if now == nil {
		now = time.Now
	}
	id := opts.id
	if id == nil {
		id = randomSessionID
	}

	return &sessionManager{
		sessions:     make(map[string]*sidecarSession),
		pendingOwner: make(map[string]int),
		now:          now,
		id:           id,
	}
}

func randomSessionID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (m *sessionManager) open(parent context.Context, in openSessionInput) (*sidecarSession, error) {
	id, err := m.id()
	if err != nil {
		return nil, err
	}
	if id == "" || in.ownerIdentity == "" || in.backend == nil {
		return nil, ErrNotValid
	}

	now := m.now()
	ctx := in.ctx
	cancel := in.cancel
	if ctx == nil || cancel == nil {
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel = context.WithCancel(parent)
	}
	lease := newSessionLeaseState(now, in.lease)
	s := &sidecarSession{
		id:             id,
		ownerIdentity:  in.ownerIdentity,
		backendType:    in.backendType,
		backend:        in.backend,
		rootPath:       in.rootPath,
		redactedTarget: in.redactedTarget,
		mode:           in.mode,
		externalRef:    in.externalRef,
		openedAt:       now,
		lastUsedAt:     now,
		expiresAt:      lease.expiresAt,
		idleExpiresAt:  lease.idleExpiresAt,
		maxExpiresAt:   lease.maxExpiresAt,
		lease:          in.lease,
		maxStreamBytes: in.maxStreamBytes,
		state:          pb.SessionState_SESSION_STATE_ACTIVE,
		ctx:            ctx,
		cancel:         cancel,
	}
	s.opsCond = sync.NewCond(&s.opsMu)

	m.mu.Lock()
	defer m.mu.Unlock()
	beforeOpenSessionInsert(in.commitCtx)
	if m.shuttingDown {
		if in.reservation != nil {
			in.reservation.releaseLocked()
		}
		cancel()
		return nil, ErrShuttingDown
	}
	if in.commitCtx != nil && in.commitCtx.Err() != nil {
		if in.reservation != nil {
			in.reservation.releaseLocked()
		}
		cancel()
		return nil, status.FromContextError(in.commitCtx.Err()).Err()
	}
	if _, ok := m.sessions[id]; ok {
		if in.reservation != nil {
			in.reservation.releaseLocked()
		}
		cancel()
		return nil, ErrConflict
	}
	m.sessions[id] = s
	if in.reservation != nil {
		in.reservation.releaseLocked()
	}
	return s, nil
}

func (m *sessionManager) get(caller string, operator bool, id string) (*sidecarSession, error) {
	s, release, err := m.acquire(caller, operator, id)
	if err != nil {
		return nil, err
	}
	release()
	return s, nil
}

func (m *sessionManager) acquire(caller string, operator bool, id string) (*sidecarSession, func(), error) {
	s, err := m.lookup(caller, operator, id)
	if err != nil {
		return nil, nil, err
	}

	now := m.now()
	s.mu.Lock()
	if s.state == pb.SessionState_SESSION_STATE_CLOSED {
		s.mu.Unlock()
		return nil, nil, ErrSessionInactive
	}
	if deadlineReached(now, s.expiresAt) ||
		deadlineReached(now, s.idleExpiresAt) ||
		deadlineReached(now, s.maxExpiresAt) {
		s.state = pb.SessionState_SESSION_STATE_EXPIRED
		s.mu.Unlock()
		s.finalize()
		return nil, nil, ErrTimeout
	}
	if s.state != pb.SessionState_SESSION_STATE_ACTIVE {
		s.mu.Unlock()
		return nil, nil, ErrSessionInactive
	}
	release := s.beginOperation()
	s.mu.Unlock()

	return s, release, nil
}

func (m *sessionManager) renew(caller string, operator bool, id string) (sessionLeaseState, error) {
	s, err := m.lookup(caller, operator, id)
	if err != nil {
		return sessionLeaseState{}, err
	}

	now := m.now()
	s.mu.Lock()
	if s.state == pb.SessionState_SESSION_STATE_CLOSED {
		s.mu.Unlock()
		return sessionLeaseState{}, ErrSessionInactive
	}
	if deadlineReached(now, s.expiresAt) ||
		deadlineReached(now, s.idleExpiresAt) ||
		deadlineReached(now, s.maxExpiresAt) {
		s.state = pb.SessionState_SESSION_STATE_EXPIRED
		s.mu.Unlock()
		s.finalize()
		return sessionLeaseState{}, ErrTimeout
	}
	if s.state != pb.SessionState_SESSION_STATE_ACTIVE {
		s.mu.Unlock()
		return sessionLeaseState{}, ErrSessionInactive
	}
	if !s.lease.renewable {
		s.mu.Unlock()
		return sessionLeaseState{}, ErrSessionInactive
	}

	expiresAt := addDuration(now, s.lease.duration)
	expiresAt = minDeadline(expiresAt, s.maxExpiresAt)
	s.lastUsedAt = now
	s.expiresAt = expiresAt
	s.idleExpiresAt = addDuration(now, s.lease.idleTimeout)
	lease := sessionLeaseState{
		expiresAt:     s.expiresAt,
		idleExpiresAt: s.idleExpiresAt,
		maxExpiresAt:  s.maxExpiresAt,
		renewable:     s.lease.renewable,
	}
	s.mu.Unlock()
	return lease, nil
}

func (m *sessionManager) markUsed(s *sidecarSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := m.now()
	s.lastUsedAt = now
	s.idleExpiresAt = addDuration(now, s.lease.idleTimeout)
}

func (m *sessionManager) activeCount(ownerIdentity string, now time.Time) int {
	m.mu.Lock()
	sessions := make([]*sidecarSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.Unlock()

	count := 0
	for _, session := range sessions {
		snapshot := snapshotSession(session, now)
		if snapshot.state != pb.SessionState_SESSION_STATE_ACTIVE {
			continue
		}
		if ownerIdentity != "" && snapshot.ownerIdentity != ownerIdentity {
			continue
		}
		count++
	}
	return count
}

func (m *sessionManager) reserveOpen(ownerIdentity string, globalMax, ownerMax int) (*openReservation, error) {
	now := m.now()
	m.mu.Lock()
	if m.shuttingDown {
		m.mu.Unlock()
		return nil, ErrShuttingDown
	}
	activeGlobal := 0
	activeOwner := 0
	expired := []*sidecarSession{}
	for _, session := range m.sessions {
		state, didExpire := session.stateAt(now)
		if didExpire {
			expired = append(expired, session)
		}
		if state != pb.SessionState_SESSION_STATE_ACTIVE {
			continue
		}
		activeGlobal++
		if session.ownerIdentity == ownerIdentity {
			activeOwner++
		}
	}
	pendingGlobal := m.pendingTotal
	pendingOwner := m.pendingOwner[ownerIdentity]
	if globalMax > 0 && activeGlobal+pendingGlobal >= globalMax {
		m.mu.Unlock()
		finalizeExpiredSessions(expired)
		return nil, status.Error(codes.ResourceExhausted, "maximum active sessions reached")
	}
	if ownerMax > 0 && activeOwner+pendingOwner >= ownerMax {
		m.mu.Unlock()
		finalizeExpiredSessions(expired)
		return nil, status.Error(codes.ResourceExhausted, "maximum active sessions reached for identity")
	}
	m.pendingTotal++
	m.pendingOwner[ownerIdentity]++
	reservation := &openReservation{
		manager:       m,
		ownerIdentity: ownerIdentity,
	}
	m.mu.Unlock()
	finalizeExpiredSessions(expired)
	return reservation, nil
}

func (r *openReservation) release() {
	if r == nil || r.manager == nil {
		return
	}
	r.manager.mu.Lock()
	defer r.manager.mu.Unlock()
	r.releaseLocked()
}

func (r *openReservation) releaseLocked() {
	if r == nil || r.released {
		return
	}
	r.released = true
	m := r.manager
	m.pendingTotal--
	m.pendingOwner[r.ownerIdentity]--
	if m.pendingOwner[r.ownerIdentity] <= 0 {
		delete(m.pendingOwner, r.ownerIdentity)
	}
}

func (m *sessionManager) close(caller string, operator bool, id string) error {
	s, err := m.lookup(caller, operator, id)
	if err != nil {
		return err
	}

	s.mu.Lock()
	if s.state == pb.SessionState_SESSION_STATE_CLOSED {
		s.mu.Unlock()
		return nil
	}
	s.state = pb.SessionState_SESSION_STATE_CLOSED
	s.mu.Unlock()

	s.finalize()
	return nil
}

func (m *sessionManager) lookup(caller string, operator bool, id string) (*sidecarSession, error) {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return nil, ErrNotFound
	}
	if !operator && s.ownerIdentity != caller {
		return nil, ErrNotAllowed
	}
	return s, nil
}

func (m *sessionManager) scanExpiredSessions() int {
	now := m.now()
	m.mu.Lock()
	expired := make([]*sidecarSession, 0)
	for _, session := range m.sessions {
		if _, didExpire := session.stateAt(now); didExpire {
			expired = append(expired, session)
		}
	}
	m.mu.Unlock()

	finalizeExpiredSessions(expired)
	return len(expired)
}

func ensureActive(s *sidecarSession) error {
	return ensureActiveAt(s, time.Now())
}

func ensureActiveAt(s *sidecarSession, now time.Time) error {
	s.mu.Lock()
	if s.state == pb.SessionState_SESSION_STATE_CLOSED {
		s.mu.Unlock()
		return ErrSessionInactive
	}
	if deadlineReached(now, s.expiresAt) ||
		deadlineReached(now, s.idleExpiresAt) ||
		deadlineReached(now, s.maxExpiresAt) {
		s.state = pb.SessionState_SESSION_STATE_EXPIRED
		s.mu.Unlock()
		s.finalize()
		return ErrTimeout
	}
	if s.state != pb.SessionState_SESSION_STATE_ACTIVE {
		s.mu.Unlock()
		return ErrSessionInactive
	}
	s.mu.Unlock()
	return nil
}

func (s *sidecarSession) stateAt(now time.Time) (pb.SessionState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == pb.SessionState_SESSION_STATE_ACTIVE &&
		(deadlineReached(now, s.expiresAt) ||
			deadlineReached(now, s.idleExpiresAt) ||
			deadlineReached(now, s.maxExpiresAt)) {
		s.state = pb.SessionState_SESSION_STATE_EXPIRED
		return s.state, true
	}
	return s.state, false
}

func (s *sidecarSession) finalize() {
	if s.cancel != nil {
		s.cancel()
	}
	s.waitForOps()
	s.closeBackendOnce()
}

func (s *sidecarSession) beginOperation() func() {
	s.opsMu.Lock()
	s.inFlightOps++
	s.opsMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.opsMu.Lock()
			if s.inFlightOps > 0 {
				s.inFlightOps--
				if s.inFlightOps == 0 && s.opsCond != nil {
					s.opsCond.Broadcast()
				}
			}
			s.opsMu.Unlock()
		})
	}
}

func (s *sidecarSession) waitForOps() {
	s.opsMu.Lock()
	defer s.opsMu.Unlock()
	for s.inFlightOps > 0 {
		s.opsCond.Wait()
	}
}

func (s *sidecarSession) waitForOpsUntil(deadline time.Time) bool {
	for {
		s.opsMu.Lock()
		inFlight := s.inFlightOps
		s.opsMu.Unlock()
		if inFlight == 0 {
			return true
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return false
		}

		sleep := 5 * time.Millisecond
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return false
			}
			if remaining < sleep {
				sleep = remaining
			}
		}
		time.Sleep(sleep)
	}
}

func (s *sidecarSession) closeBackendOnce() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		backend := s.backend
		s.backend = nil
		s.mu.Unlock()
		closeBackend(backend)
	})
}

func (s *sidecarSession) backendHandle() (IBackend, error) {
	s.mu.Lock()
	backend := s.backend
	s.mu.Unlock()
	if backend == nil {
		return nil, ErrSessionInactive
	}
	return backend, nil
}

func finalizeExpiredSessions(sessions []*sidecarSession) {
	for _, session := range sessions {
		session.finalize()
	}
}

func newSessionLeaseState(now time.Time, lease effectiveLease) sessionLeaseState {
	expiresAt := addDuration(now, lease.duration)
	maxExpiresAt := addDuration(now, lease.maxLifetime)
	expiresAt = minDeadline(expiresAt, maxExpiresAt)
	return sessionLeaseState{
		expiresAt:     expiresAt,
		idleExpiresAt: addDuration(now, lease.idleTimeout),
		maxExpiresAt:  maxExpiresAt,
		renewable:     lease.renewable,
	}
}

func addDuration(t time.Time, d time.Duration) time.Time {
	if d <= 0 {
		return time.Time{}
	}
	return t.Add(d)
}

func minDeadline(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	if b.IsZero() || a.Before(b) {
		return a
	}
	return b
}

func deadlineReached(now, deadline time.Time) bool {
	return !deadline.IsZero() && !now.Before(deadline)
}
