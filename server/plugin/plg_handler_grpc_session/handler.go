package plg_handler_grpc_session

import (
	"context"
	"io"
	"os"
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

func (s *sidecarService) List(ctx context.Context, req *pb.ListRequest) (*pb.ListResponse, error) {
	session, err := s.activeSession(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	resolved, err := resolveSessionPath(session, req.GetPath(), pathUseRead)
	if err != nil {
		return nil, grpcError(err)
	}

	entries, err := session.backend.Ls(resolved)
	if err != nil {
		return nil, grpcError(err)
	}
	out := &pb.ListResponse{Files: make([]*pb.FileInfo, 0, len(entries))}
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		out.Files = append(out.Files, fileInfoFromOS(entry))
	}
	s.sessionManager.markUsed(session)
	return out, nil
}

func (s *sidecarService) Stat(ctx context.Context, req *pb.StatRequest) (*pb.StatResponse, error) {
	session, err := s.activeSession(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	resolved, err := resolveSessionPath(session, req.GetPath(), pathUseRead)
	if err != nil {
		return nil, grpcError(err)
	}

	info, err := session.backend.Stat(resolved)
	if err != nil {
		return nil, grpcError(err)
	}
	if info == nil {
		return nil, status.Error(codes.Internal, "backend returned nil file info")
	}
	s.sessionManager.markUsed(session)
	return &pb.StatResponse{File: fileInfoFromOS(info)}, nil
}

func (s *sidecarService) ReadFile(req *pb.ReadFileRequest, stream pb.FilestashSidecarService_ReadFileServer) error {
	if req.GetOffset() < 0 || req.GetLimit() < 0 {
		return status.Error(codes.InvalidArgument, "offset and limit must be non-negative")
	}
	session, err := s.activeSession(stream.Context(), req.GetSessionId())
	if err != nil {
		return err
	}
	resolved, err := resolveSessionPath(session, req.GetPath(), pathUseRead)
	if err != nil {
		return grpcError(err)
	}

	reader, err := session.backend.Cat(resolved)
	if err != nil {
		return grpcError(err)
	}
	if reader == nil {
		return status.Error(codes.Internal, "backend returned nil reader")
	}
	defer reader.Close()

	if offset := req.GetOffset(); offset > 0 {
		if _, err := io.CopyN(io.Discard, reader, offset); err != nil && err != io.EOF {
			return grpcError(err)
		}
	}

	buf := make([]byte, 32*1024)
	remaining := req.GetLimit()
	for {
		readBuf := buf
		if remaining > 0 && int64(len(readBuf)) > remaining {
			readBuf = readBuf[:remaining]
		}
		n, err := reader.Read(readBuf)
		if n > 0 {
			if sendErr := stream.Send(&pb.ReadFileChunk{Data: append([]byte(nil), readBuf[:n]...)}); sendErr != nil {
				return sendErr
			}
			if remaining > 0 {
				remaining -= int64(n)
				if remaining == 0 {
					break
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return grpcError(err)
		}
	}
	s.sessionManager.markUsed(session)
	return nil
}

func (s *sidecarService) WriteFile(stream pb.FilestashSidecarService_WriteFileServer) error {
	first, err := stream.Recv()
	if err == io.EOF {
		return status.Error(codes.InvalidArgument, "write file header is required")
	}
	if err != nil {
		return err
	}
	header := first.GetHeader()
	if header == nil {
		return status.Error(codes.InvalidArgument, "first write file message must be a header")
	}
	if header.GetExpectedSize() < 0 {
		return status.Error(codes.InvalidArgument, "expected_size must be non-negative")
	}

	session, err := s.activeSession(stream.Context(), header.GetSessionId())
	if err != nil {
		return err
	}
	if !isWriteMode(session.mode) {
		return status.Error(codes.FailedPrecondition, "session is read-only")
	}
	resolved, err := resolveSessionPath(session, header.GetPath(), pathUseMutateChild)
	if err != nil {
		return grpcError(err)
	}

	return s.writeFileStaged(stream, session, resolved, header.GetExpectedSize(), writeFileMaxBytes())
}

func (s *sidecarService) writeFileStaged(stream pb.FilestashSidecarService_WriteFileServer, session *sidecarSession, resolved string, expected int64, maxBytes int64) error {
	staging, err := os.CreateTemp("", "filestash-sidecar-write-*")
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	defer os.Remove(staging.Name())
	defer staging.Close()

	var written int64
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		data, err := parseWriteFileDataMessage(req)
		if err != nil {
			return err
		}
		written += int64(len(data))
		if maxBytes > 0 && written > maxBytes {
			return status.Errorf(codes.ResourceExhausted, "write stream exceeds max size %d", maxBytes)
		}
		if expected > 0 && written > expected {
			return status.Errorf(codes.FailedPrecondition, "bytes written %d exceeds expected size %d", written, expected)
		}
		if _, err := staging.Write(data); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
	}
	if expected > 0 && written != expected {
		return status.Errorf(codes.FailedPrecondition, "bytes written %d does not match expected size %d", written, expected)
	}
	if _, err := staging.Seek(0, io.SeekStart); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	if err := session.backend.Save(resolved, staging); err != nil {
		return grpcError(err)
	}

	res := &pb.WriteFileResponse{BytesWritten: written}
	if err := stream.SendAndClose(res); err != nil {
		return err
	}
	s.sessionManager.markUsed(session)
	return nil
}

func writeFileMaxBytes() int64 {
	max := PluginMaxStreamBytes()
	if max > 0 {
		return max
	}
	return 1 << 30
}

func parseWriteFileDataMessage(req *pb.WriteFileRequest) ([]byte, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "write file data message is required")
	}
	switch payload := req.GetPayload().(type) {
	case *pb.WriteFileRequest_Data:
		return payload.Data, nil
	case *pb.WriteFileRequest_Header:
		return nil, status.Error(codes.InvalidArgument, "write file header may only be sent once")
	default:
		return nil, status.Error(codes.InvalidArgument, "write file data payload is required")
	}
}

func (s *sidecarService) Mkdir(ctx context.Context, req *pb.MkdirRequest) (*pb.MutationResponse, error) {
	session, resolved, err := s.mutationTarget(ctx, req.GetSessionId(), req.GetPath())
	if err != nil {
		return nil, err
	}
	if err := session.backend.Mkdir(resolved); err != nil {
		return nil, grpcError(err)
	}
	s.sessionManager.markUsed(session)
	return &pb.MutationResponse{Ok: true}, nil
}

func (s *sidecarService) Remove(ctx context.Context, req *pb.RemoveRequest) (*pb.MutationResponse, error) {
	session, resolved, err := s.mutationTarget(ctx, req.GetSessionId(), req.GetPath())
	if err != nil {
		return nil, err
	}
	if err := session.backend.Rm(resolved); err != nil {
		return nil, grpcError(err)
	}
	s.sessionManager.markUsed(session)
	return &pb.MutationResponse{Ok: true}, nil
}

func (s *sidecarService) Rename(ctx context.Context, req *pb.RenameRequest) (*pb.MutationResponse, error) {
	session, err := s.activeWritableSession(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	from, err := resolveSessionPath(session, req.GetFrom(), pathUseMutateChild)
	if err != nil {
		return nil, grpcError(err)
	}
	to, err := resolveSessionPath(session, req.GetTo(), pathUseMutateChild)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := session.backend.Mv(from, to); err != nil {
		return nil, grpcError(err)
	}
	s.sessionManager.markUsed(session)
	return &pb.MutationResponse{Ok: true}, nil
}

func (s *sidecarService) Touch(ctx context.Context, req *pb.TouchRequest) (*pb.MutationResponse, error) {
	session, resolved, err := s.mutationTarget(ctx, req.GetSessionId(), req.GetPath())
	if err != nil {
		return nil, err
	}
	if err := session.backend.Touch(resolved); err != nil {
		return nil, grpcError(err)
	}
	s.sessionManager.markUsed(session)
	return &pb.MutationResponse{Ok: true}, nil
}

func (s *sidecarService) activeSession(ctx context.Context, sessionID string) (*sidecarSession, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	caller, err := s.callerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	session, err := s.sessionManager.get(caller.identity, caller.operator, sessionID)
	if err != nil {
		return nil, grpcError(err)
	}
	return session, nil
}

func (s *sidecarService) activeWritableSession(ctx context.Context, sessionID string) (*sidecarSession, error) {
	session, err := s.activeSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !isWriteMode(session.mode) {
		return nil, status.Error(codes.FailedPrecondition, "session is read-only")
	}
	return session, nil
}

func (s *sidecarService) mutationTarget(ctx context.Context, sessionID, inputPath string) (*sidecarSession, string, error) {
	session, err := s.activeWritableSession(ctx, sessionID)
	if err != nil {
		return nil, "", err
	}
	resolved, err := resolveSessionPath(session, inputPath, pathUseMutateChild)
	if err != nil {
		return nil, "", grpcError(err)
	}
	return session, resolved, nil
}

func resolveSessionPath(session *sidecarSession, input string, usage pathUse) (string, error) {
	resolver, err := newPathResolver(session.rootPath)
	if err != nil {
		return "", err
	}
	return resolver.resolve(input, usage)
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
