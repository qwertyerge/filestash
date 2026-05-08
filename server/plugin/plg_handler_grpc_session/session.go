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

type externalRef struct {
	kind string
	id   string
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
	leaseOptions  effectiveLease
	lease         sessionLeaseState
	state         pb.SessionState
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
}

type openSessionInput struct {
	ownerIdentity string
	backendType   string
	backend       IBackend
	rootPath      string
	mode          pb.AccessMode
	externalRef   externalRef
	lease         effectiveLease
}

type sessionManagerOptions struct {
	now func() time.Time
	id  func() (string, error)
}

type sessionManager struct {
	mu       sync.Mutex
	sessions map[string]*sidecarSession
	now      func() time.Time
	id       func() (string, error)
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
		sessions: make(map[string]*sidecarSession),
		now:      now,
		id:       id,
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
	if parent == nil {
		parent = context.Background()
	}
	id, err := m.id()
	if err != nil {
		return nil, err
	}
	if id == "" || in.ownerIdentity == "" || in.backend == nil {
		return nil, ErrNotValid
	}

	now := m.now()
	ctx, cancel := context.WithCancel(parent)
	lease := newSessionLeaseState(now, in.lease)
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
		expiresAt:     lease.expiresAt,
		idleExpiresAt: lease.idleExpiresAt,
		maxExpiresAt:  lease.maxExpiresAt,
		leaseOptions:  in.lease,
		lease:         lease,
		state:         pb.SessionState_SESSION_STATE_ACTIVE,
		ctx:           ctx,
		cancel:        cancel,
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[id]; ok {
		cancel()
		return nil, ErrConflict
	}
	m.sessions[id] = s
	return s, nil
}

func (m *sessionManager) get(caller string, operator bool, id string) (*sidecarSession, error) {
	s, err := m.lookup(caller, operator, id)
	if err != nil {
		return nil, err
	}
	if err := ensureActiveAt(s, m.now()); err != nil {
		return nil, err
	}
	return s, nil
}

func (m *sessionManager) renew(caller string, operator bool, id string) (sessionLeaseState, error) {
	s, err := m.lookup(caller, operator, id)
	if err != nil {
		return sessionLeaseState{}, err
	}

	now := m.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == pb.SessionState_SESSION_STATE_CLOSED {
		return sessionLeaseState{}, ErrNotAllowed
	}
	if deadlineReached(now, s.expiresAt) || deadlineReached(now, s.maxExpiresAt) {
		s.state = pb.SessionState_SESSION_STATE_EXPIRED
		return sessionLeaseState{}, ErrTimeout
	}
	if s.state != pb.SessionState_SESSION_STATE_ACTIVE {
		return sessionLeaseState{}, ErrNotAllowed
	}
	if !s.leaseOptions.renewable {
		return sessionLeaseState{}, ErrNotAllowed
	}

	expiresAt := addDuration(now, s.leaseOptions.duration)
	expiresAt = minDeadline(expiresAt, s.maxExpiresAt)
	s.lastUsedAt = now
	s.expiresAt = expiresAt
	s.idleExpiresAt = addDuration(now, s.leaseOptions.idleTimeout)
	s.lease = sessionLeaseState{
		expiresAt:     s.expiresAt,
		idleExpiresAt: s.idleExpiresAt,
		maxExpiresAt:  s.maxExpiresAt,
		renewable:     s.leaseOptions.renewable,
	}
	return s.lease, nil
}

func markUsed(s *sidecarSession) {
	markUsedAt(s, time.Now())
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
	cancel := s.cancel
	backend := s.backend
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if closer, ok := backend.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func ensureActive(s *sidecarSession) error {
	return ensureActiveAt(s, time.Now())
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

func ensureActiveAt(s *sidecarSession, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == pb.SessionState_SESSION_STATE_CLOSED {
		return ErrNotAllowed
	}
	if deadlineReached(now, s.expiresAt) ||
		deadlineReached(now, s.idleExpiresAt) ||
		deadlineReached(now, s.maxExpiresAt) {
		s.state = pb.SessionState_SESSION_STATE_EXPIRED
		return ErrTimeout
	}
	if s.state != pb.SessionState_SESSION_STATE_ACTIVE {
		return ErrNotAllowed
	}
	return nil
}

func markUsedAt(s *sidecarSession, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastUsedAt = now
	s.idleExpiresAt = addDuration(now, s.leaseOptions.idleTimeout)
	s.lease.idleExpiresAt = s.idleExpiresAt
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
