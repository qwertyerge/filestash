package plg_handler_grpc_session

import (
	"context"
	"sort"
	"strings"
	"time"

	. "github.com/mickael-kerjean/filestash/server/common"
	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type sidecarService struct {
	pb.UnimplementedFilestashSidecarServiceServer

	sessionManager *sessionManager
	policies       *policyEngine
}

type sessionCaller struct {
	identity string
	operator bool
}

type sessionSnapshot struct {
	id            string
	ownerIdentity string
	backendType   string
	rootPath      string
	mode          pb.AccessMode
	state         pb.SessionState
	externalRef   externalRef
	openedAt      time.Time
	lastUsedAt    time.Time
	lease         sessionLeaseState
}

func newSidecarService(sessions *sessionManager, policies *policyEngine) (*sidecarService, error) {
	if sessions == nil {
		return nil, status.Error(codes.FailedPrecondition, "sidecar service requires session manager")
	}
	if policies == nil {
		return nil, status.Error(codes.FailedPrecondition, "sidecar service requires policy engine")
	}
	return &sidecarService{
		sessionManager: sessions,
		policies:       policies,
	}, nil
}

func (s *sidecarService) ready() error {
	if s == nil || s.sessionManager == nil {
		return status.Error(codes.FailedPrecondition, "sidecar service is not ready")
	}
	return nil
}

func identityFromContext(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok || p == nil {
		return "", status.Error(codes.Unauthenticated, ErrNotAuthorized.Error())
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", status.Error(codes.Unauthenticated, ErrNotAuthorized.Error())
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return "", status.Error(codes.Unauthenticated, ErrNotAuthorized.Error())
	}
	identity := strings.TrimSpace(identityFromCertificate(tlsInfo.State.PeerCertificates[0]))
	if identity == "" {
		return "", status.Error(codes.Unauthenticated, ErrNotAuthorized.Error())
	}
	return identity, nil
}

func (s *sidecarService) callerFromContext(ctx context.Context) (sessionCaller, error) {
	identity, err := identityFromContext(ctx)
	if err != nil {
		return sessionCaller{}, err
	}
	if s.policies == nil {
		return sessionCaller{identity: identity}, nil
	}
	policy, err := s.policies.policyFor(identity)
	if err != nil {
		return sessionCaller{}, grpcError(err)
	}
	return sessionCaller{
		identity: identity,
		operator: policy.Role == "operator",
	}, nil
}

func (s *sidecarService) RenewSession(ctx context.Context, req *pb.RenewSessionRequest) (*pb.SessionLease, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	caller, err := s.callerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateSessionID(req.GetSessionId()); err != nil {
		return nil, err
	}
	lease, err := s.sessionManager.renew(caller.identity, caller.operator, req.GetSessionId())
	if err != nil {
		return nil, grpcError(err)
	}
	return leaseToProto(req.GetSessionId(), lease), nil
}

func (s *sidecarService) Close(ctx context.Context, req *pb.CloseSessionRequest) (*pb.CloseSessionResponse, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	caller, err := s.callerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateSessionID(req.GetSessionId()); err != nil {
		return nil, err
	}
	if err := s.sessionManager.close(caller.identity, caller.operator, req.GetSessionId()); err != nil {
		return nil, grpcError(err)
	}
	return &pb.CloseSessionResponse{
		SessionId: req.GetSessionId(),
		State:     pb.SessionState_SESSION_STATE_CLOSED,
	}, nil
}

func (s *sidecarService) GetSession(ctx context.Context, req *pb.GetSessionRequest) (*pb.SessionInfo, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	caller, err := s.callerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateSessionID(req.GetSessionId()); err != nil {
		return nil, err
	}
	session, err := s.sessionManager.lookup(caller.identity, caller.operator, req.GetSessionId())
	if err != nil {
		return nil, grpcError(err)
	}
	return sessionInfoToProto(snapshotSession(session, s.sessionManager.now())), nil
}

func (s *sidecarService) ListSessions(ctx context.Context, req *pb.ListSessionsRequest) (*pb.ListSessionsResponse, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	caller, err := s.callerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	sessions := s.snapshotSessions(caller, req.GetIncludeClosed())
	out := &pb.ListSessionsResponse{Sessions: make([]*pb.SessionInfo, 0, len(sessions))}
	for _, session := range sessions {
		out.Sessions = append(out.Sessions, sessionInfoToProto(session))
	}
	return out, nil
}

func (s *sidecarService) ForceClose(ctx context.Context, req *pb.ForceCloseRequest) (*pb.CloseSessionResponse, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	caller, err := s.callerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateSessionID(req.GetSessionId()); err != nil {
		return nil, err
	}
	if !caller.operator {
		return nil, grpcError(ErrPermissionDenied)
	}
	if err := s.sessionManager.close(caller.identity, true, req.GetSessionId()); err != nil {
		return nil, grpcError(err)
	}
	return &pb.CloseSessionResponse{
		SessionId: req.GetSessionId(),
		State:     pb.SessionState_SESSION_STATE_CLOSED,
	}, nil
}

func (s *sidecarService) snapshotSessions(caller sessionCaller, includeClosed bool) []sessionSnapshot {
	now := s.sessionManager.now()
	s.sessionManager.mu.Lock()
	sessions := make([]*sidecarSession, 0, len(s.sessionManager.sessions))
	for _, session := range s.sessionManager.sessions {
		if !caller.operator && session.ownerIdentity != caller.identity {
			continue
		}
		sessions = append(sessions, session)
	}
	s.sessionManager.mu.Unlock()

	out := make([]sessionSnapshot, 0, len(sessions))
	for _, session := range sessions {
		snapshot := snapshotSession(session, now)
		if !includeClosed && snapshot.state == pb.SessionState_SESSION_STATE_CLOSED {
			continue
		}
		out = append(out, snapshot)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].id < out[j].id
	})
	return out
}

func validateSessionID(id string) error {
	if strings.TrimSpace(id) == "" {
		return status.Error(codes.InvalidArgument, "session_id is required")
	}
	return nil
}

func leaseToProto(sessionID string, lease sessionLeaseState) *pb.SessionLease {
	return &pb.SessionLease{
		SessionId:     sessionID,
		ExpiresAt:     timestampFromTime(lease.expiresAt),
		IdleExpiresAt: timestampFromTime(lease.idleExpiresAt),
		MaxExpiresAt:  timestampFromTime(lease.maxExpiresAt),
		Renewable:     lease.renewable,
	}
}

func leaseRequestFromProto(in *pb.LeaseOptions) leaseRequest {
	if in == nil {
		return leaseRequest{}
	}
	return leaseRequest{
		duration:    time.Duration(in.GetDurationSeconds()) * time.Second,
		idleTimeout: time.Duration(in.GetIdleTimeoutSeconds()) * time.Second,
		maxLifetime: time.Duration(in.GetMaxLifetimeSeconds()) * time.Second,
		renewable:   in.GetRenewable(),
	}
}

func externalRefFromProto(in *pb.ExternalRef) externalRef {
	if in == nil {
		return externalRef{}
	}
	return externalRef{
		kind: in.GetKind(),
		id:   in.GetId(),
	}
}

func externalRefToProto(in externalRef) *pb.ExternalRef {
	if in.kind == "" && in.id == "" {
		return nil
	}
	return &pb.ExternalRef{
		Kind: in.kind,
		Id:   in.id,
	}
}

func sessionInfoToProto(in sessionSnapshot) *pb.SessionInfo {
	return &pb.SessionInfo{
		SessionId:      in.id,
		OwnerIdentity:  in.ownerIdentity,
		BackendType:    in.backendType,
		RedactedTarget: redactedTarget(in),
		RootPath:       in.rootPath,
		Mode:           in.mode,
		State:          in.state,
		ExternalRef:    externalRefToProto(in.externalRef),
		OpenedAt:       timestampFromTime(in.openedAt),
		LastUsedAt:     timestampFromTime(in.lastUsedAt),
		Lease:          leaseToProto(in.id, in.lease),
	}
}

func redactedTarget(sessionSnapshot) string {
	return ""
}

func snapshotSession(s *sidecarSession, now time.Time) sessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == pb.SessionState_SESSION_STATE_ACTIVE &&
		(deadlineReached(now, s.expiresAt) ||
			deadlineReached(now, s.idleExpiresAt) ||
			deadlineReached(now, s.maxExpiresAt)) {
		s.state = pb.SessionState_SESSION_STATE_EXPIRED
	}
	return sessionSnapshot{
		id:            s.id,
		ownerIdentity: s.ownerIdentity,
		backendType:   s.backendType,
		rootPath:      s.rootPath,
		mode:          s.mode,
		state:         s.state,
		externalRef:   s.externalRef,
		openedAt:      s.openedAt,
		lastUsedAt:    s.lastUsedAt,
		lease: sessionLeaseState{
			expiresAt:     s.expiresAt,
			idleExpiresAt: s.idleExpiresAt,
			maxExpiresAt:  s.maxExpiresAt,
			renewable:     s.lease.renewable,
		},
	}
}

func timestampFromTime(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
