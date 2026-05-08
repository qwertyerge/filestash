package plg_handler_grpc_session

import (
	"context"
	"errors"
	"io"
	"net/url"
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
	maxSessions    int
	maxStreamBytes int64
	defaultLease   effectiveLease
}

type sessionCaller struct {
	identity string
	operator bool
}

type sessionSnapshot struct {
	id             string
	ownerIdentity  string
	backendType    string
	rootPath       string
	redactedTarget string
	mode           pb.AccessMode
	state          pb.SessionState
	externalRef    externalRef
	openedAt       time.Time
	lastUsedAt     time.Time
	lease          sessionLeaseState
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
		maxSessions:    PluginMaxSessions(),
		maxStreamBytes: PluginMaxStreamBytes(),
		defaultLease:   defaultLeaseFromConfig(),
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

func (s *sidecarService) Open(ctx context.Context, req *pb.OpenRequest) (*pb.OpenResponse, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if s.policies == nil {
		return nil, status.Error(codes.FailedPrecondition, "sidecar service requires policy engine")
	}
	caller, policy, err := s.openCallerPolicy(ctx)
	if err != nil {
		return nil, err
	}

	backendType := strings.TrimSpace(req.GetBackendType())
	if backendType == "" {
		return nil, status.Error(codes.InvalidArgument, "backend_type is required")
	}
	root, err := normalizedRootPath(req.GetRootPath())
	if err != nil {
		return nil, grpcError(err)
	}
	driver, ok := Backend.Drivers()[backendType]
	if !ok || driver == nil {
		return nil, grpcError(ErrNotFound)
	}

	params := copyStringMap(req.GetBackendParams())
	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	var backend IBackend
	cleanup := func() {
		sessionCancel()
		if backend != nil {
			closeBackend(backend)
		}
	}

	effective, err := policy.effectiveOpen(openPolicyRequest{
		backendType:   backendType,
		backendParams: params,
		mode:          req.GetMode(),
		lease:         leaseRequestFromProto(req.GetLease()),
		defaultLease:  s.defaultLease,
	})
	if err != nil {
		cleanup()
		return nil, grpcError(err)
	}
	reservation, err := s.reserveOpenAdmission(caller.identity, effective.maxSessions)
	if err != nil {
		cleanup()
		return nil, err
	}

	backendParams := copyStringMap(params)
	backend, err = initAndVerifyBackend(ctx, driver, backendParams, sessionCtx, root)
	if err != nil {
		reservation.release()
		cleanup()
		return nil, grpcError(err)
	}
	if backend == nil {
		reservation.release()
		cleanup()
		return nil, status.Error(codes.Internal, "backend init returned nil backend")
	}

	session, err := s.sessionManager.open(context.Background(), openSessionInput{
		ownerIdentity:  caller.identity,
		backendType:    backendType,
		backend:        backend,
		rootPath:       root,
		redactedTarget: redactedTargetFromBackendParams(params),
		mode:           effective.mode,
		externalRef:    externalRefFromProto(req.GetExternalRef()),
		lease:          effective.lease,
		maxStreamBytes: effectiveMaxStreamBytes(s.maxStreamBytes, effective.maxStreamBytes),
		ctx:            sessionCtx,
		cancel:         sessionCancel,
		commitCtx:      ctx,
		reservation:    reservation,
	})
	if err != nil {
		reservation.release()
		cleanup()
		return nil, grpcError(err)
	}
	snapshot := snapshotSession(session, s.sessionManager.now())

	return &pb.OpenResponse{
		SessionId:     snapshot.id,
		EffectiveMode: snapshot.mode,
		Lease:         leaseToProto(snapshot.id, snapshot.lease),
	}, nil
}

var beforeOpenSessionInsert = func(context.Context) {}

func (s *sidecarService) openCallerPolicy(ctx context.Context) (sessionCaller, *clientPolicy, error) {
	identity, err := identityFromContext(ctx)
	if err != nil {
		return sessionCaller{}, nil, err
	}
	policy, err := s.policies.policyFor(identity)
	if err != nil {
		return sessionCaller{}, nil, grpcError(err)
	}
	return sessionCaller{
		identity: identity,
		operator: policy.Role == "operator",
	}, policy, nil
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
	session, release, err := s.activeSession(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	defer release()
	resolved, err := resolveSessionPath(session, req.GetPath(), pathUseRead)
	if err != nil {
		return nil, grpcError(err)
	}
	backend, err := session.backendHandle()
	if err != nil {
		return nil, grpcError(err)
	}

	entries, err := backend.Ls(resolved)
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
	session, release, err := s.activeSession(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	defer release()
	resolved, err := resolveSessionPath(session, req.GetPath(), pathUseRead)
	if err != nil {
		return nil, grpcError(err)
	}
	backend, err := session.backendHandle()
	if err != nil {
		return nil, grpcError(err)
	}

	info, err := backend.Stat(resolved)
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
	session, release, err := s.activeSession(stream.Context(), req.GetSessionId())
	if err != nil {
		return err
	}
	defer release()
	resolved, err := resolveSessionPath(session, req.GetPath(), pathUseRead)
	if err != nil {
		return grpcError(err)
	}
	backend, err := session.backendHandle()
	if err != nil {
		return grpcError(err)
	}

	reader, err := backend.Cat(resolved)
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
	remaining, limitedByMax := readFileLimit(req.GetLimit(), s.readFileMaxBytes(session))
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
					if limitedByMax {
						if err == io.EOF {
							break
						}
						if err != nil {
							return grpcError(err)
						}
						hasMore, moreErr := readerHasMore(reader)
						if moreErr != nil {
							return grpcError(moreErr)
						}
						if hasMore {
							return status.Errorf(codes.ResourceExhausted, "read stream exceeds max size %d", s.readFileMaxBytes(session))
						}
					}
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

	session, release, err := s.activeSession(stream.Context(), header.GetSessionId())
	if err != nil {
		return err
	}
	defer release()
	if !isWriteMode(session.mode) {
		return status.Error(codes.FailedPrecondition, "session is read-only")
	}
	resolved, err := resolveSessionPath(session, header.GetPath(), pathUseMutateChild)
	if err != nil {
		return grpcError(err)
	}
	backend, err := session.backendHandle()
	if err != nil {
		return grpcError(err)
	}
	if !header.GetOverwrite() {
		info, err := backend.Stat(resolved)
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				return grpcError(err)
			}
		} else if info != nil {
			return grpcError(ErrConflict)
		}
	}

	return s.writeFileStaged(stream, session, backend, resolved, header.GetExpectedSize(), s.writeFileMaxBytes(session))
}

func (s *sidecarService) writeFileStaged(stream pb.FilestashSidecarService_WriteFileServer, session *sidecarSession, backend IBackend, resolved string, expected int64, maxBytes int64) error {
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
	if err := backend.Save(resolved, staging); err != nil {
		return grpcError(err)
	}

	res := &pb.WriteFileResponse{BytesWritten: written}
	if err := stream.SendAndClose(res); err != nil {
		return err
	}
	s.sessionManager.markUsed(session)
	return nil
}

func (s *sidecarService) readFileMaxBytes(session *sidecarSession) int64 {
	return s.sessionMaxStreamBytes(session)
}

func (s *sidecarService) writeFileMaxBytes(session *sidecarSession) int64 {
	return s.sessionMaxStreamBytes(session)
}

func (s *sidecarService) sessionMaxStreamBytes(session *sidecarSession) int64 {
	if session != nil && session.maxStreamBytes > 0 {
		return session.maxStreamBytes
	}
	if s.maxStreamBytes > 0 {
		return s.maxStreamBytes
	}
	return 0
}

func readFileLimit(userLimit, maxBytes int64) (int64, bool) {
	if maxBytes <= 0 {
		return userLimit, false
	}
	if userLimit <= 0 || maxBytes <= userLimit {
		return maxBytes, true
	}
	return userLimit, false
}

func readerHasMore(reader io.Reader) (bool, error) {
	var one [1]byte
	n, err := reader.Read(one[:])
	if n > 0 {
		return true, nil
	}
	if err == io.EOF {
		return false, nil
	}
	return false, err
}

func effectiveMaxStreamBytes(global, policy int64) int64 {
	switch {
	case global > 0 && policy > 0:
		if global < policy {
			return global
		}
		return policy
	case policy > 0:
		return policy
	case global > 0:
		return global
	default:
		return 0
	}
}

func defaultLeaseFromConfig() effectiveLease {
	return effectiveLease{
		duration:    configSeconds(PluginDefaultLeaseSeconds()),
		idleTimeout: configSeconds(PluginDefaultIdleTimeoutSeconds()),
		maxLifetime: configSeconds(PluginDefaultMaxLifetimeSeconds()),
	}
}

func configSeconds(v int) time.Duration {
	if v <= 0 {
		return 0
	}
	return time.Duration(v) * time.Second
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
	session, release, resolved, err := s.mutationTarget(ctx, req.GetSessionId(), req.GetPath())
	if err != nil {
		return nil, err
	}
	defer release()
	backend, err := session.backendHandle()
	if err != nil {
		return nil, grpcError(err)
	}
	if err := backend.Mkdir(resolved); err != nil {
		return nil, grpcError(err)
	}
	s.sessionManager.markUsed(session)
	return &pb.MutationResponse{Ok: true}, nil
}

func (s *sidecarService) Remove(ctx context.Context, req *pb.RemoveRequest) (*pb.MutationResponse, error) {
	session, release, resolved, err := s.mutationTarget(ctx, req.GetSessionId(), req.GetPath())
	if err != nil {
		return nil, err
	}
	defer release()
	backend, err := session.backendHandle()
	if err != nil {
		return nil, grpcError(err)
	}
	if err := backend.Rm(resolved); err != nil {
		return nil, grpcError(err)
	}
	s.sessionManager.markUsed(session)
	return &pb.MutationResponse{Ok: true}, nil
}

func (s *sidecarService) Rename(ctx context.Context, req *pb.RenameRequest) (*pb.MutationResponse, error) {
	session, release, err := s.activeWritableSession(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	defer release()
	from, err := resolveSessionPath(session, req.GetFrom(), pathUseMutateChild)
	if err != nil {
		return nil, grpcError(err)
	}
	to, err := resolveSessionPath(session, req.GetTo(), pathUseMutateChild)
	if err != nil {
		return nil, grpcError(err)
	}
	backend, err := session.backendHandle()
	if err != nil {
		return nil, grpcError(err)
	}
	if err := backend.Mv(from, to); err != nil {
		return nil, grpcError(err)
	}
	s.sessionManager.markUsed(session)
	return &pb.MutationResponse{Ok: true}, nil
}

func (s *sidecarService) Touch(ctx context.Context, req *pb.TouchRequest) (*pb.MutationResponse, error) {
	session, release, resolved, err := s.mutationTarget(ctx, req.GetSessionId(), req.GetPath())
	if err != nil {
		return nil, err
	}
	defer release()
	backend, err := session.backendHandle()
	if err != nil {
		return nil, grpcError(err)
	}
	if err := backend.Touch(resolved); err != nil {
		return nil, grpcError(err)
	}
	s.sessionManager.markUsed(session)
	return &pb.MutationResponse{Ok: true}, nil
}

func (s *sidecarService) activeSession(ctx context.Context, sessionID string) (*sidecarSession, func(), error) {
	if err := s.ready(); err != nil {
		return nil, nil, err
	}
	caller, err := s.callerFromContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, nil, err
	}
	session, release, err := s.sessionManager.acquire(caller.identity, caller.operator, sessionID)
	if err != nil {
		return nil, nil, grpcError(err)
	}
	return session, release, nil
}

func (s *sidecarService) activeWritableSession(ctx context.Context, sessionID string) (*sidecarSession, func(), error) {
	session, release, err := s.activeSession(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	if !isWriteMode(session.mode) {
		release()
		return nil, nil, status.Error(codes.FailedPrecondition, "session is read-only")
	}
	return session, release, nil
}

func (s *sidecarService) mutationTarget(ctx context.Context, sessionID, inputPath string) (*sidecarSession, func(), string, error) {
	session, release, err := s.activeWritableSession(ctx, sessionID)
	if err != nil {
		return nil, nil, "", err
	}
	resolved, err := resolveSessionPath(session, inputPath, pathUseMutateChild)
	if err != nil {
		release()
		return nil, nil, "", grpcError(err)
	}
	return session, release, resolved, nil
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
		if !includeClosed && snapshot.state != pb.SessionState_SESSION_STATE_ACTIVE {
			continue
		}
		out = append(out, snapshot)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].id < out[j].id
	})
	return out
}

func (s *sidecarService) reserveOpenAdmission(identity string, policyMaxSessions int) (*openReservation, error) {
	return s.sessionManager.reserveOpen(identity, s.maxSessions, policyMaxSessions)
}

func normalizedRootPath(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", ErrNotValid
	}
	resolver, err := newPathResolver(root)
	if err != nil {
		return "", err
	}
	return resolver.root, nil
}

func verifyBackendRoot(backend IBackend, root string) error {
	info, statErr := backend.Stat(root)
	if statErr == nil && info != nil {
		return nil
	}
	if _, lsErr := backend.Ls(root); lsErr == nil {
		return nil
	} else if statErr != nil {
		return statErr
	} else {
		return lsErr
	}
}

type backendOpenResult struct {
	backend IBackend
	err     error
}

func initAndVerifyBackend(ctx context.Context, driver IBackend, params map[string]string, sessionCtx context.Context, root string) (IBackend, error) {
	result := make(chan backendOpenResult, 1)
	go func() {
		backend, err := driver.Init(params, &App{Context: sessionCtx})
		if err == nil {
			if backend == nil {
				err = status.Error(codes.Internal, "backend init returned nil backend")
			} else if verifyErr := verifyBackendRoot(backend, root); verifyErr != nil {
				err = grpcError(verifyErr)
			}
		} else {
			err = grpcError(err)
		}
		result <- backendOpenResult{backend: backend, err: err}
	}()

	select {
	case out := <-result:
		if out.err != nil && out.backend != nil {
			closeBackend(out.backend)
			out.backend = nil
		}
		return out.backend, out.err
	case <-ctx.Done():
		go func() {
			out := <-result
			if out.backend != nil {
				closeBackend(out.backend)
			}
		}()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
}

func closeBackend(backend IBackend) {
	if closer, ok := backend.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
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

func redactedTarget(in sessionSnapshot) string {
	return in.redactedTarget
}

func redactedTargetFromBackendParams(params map[string]string) string {
	redacted := redactBackendParams(params)
	for _, key := range []string{"hostname", "host", "endpoint", "url"} {
		target := strings.TrimSpace(redacted[key])
		if target == "" || target == "[REDACTED]" {
			continue
		}
		if safe := safeTarget(target); safe != "" {
			return safe
		}
	}
	return ""
}

func safeTarget(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		return trimHost(u.Host)
	}
	if before, _, ok := strings.Cut(target, "?"); ok {
		target = before
	}
	if before, _, ok := strings.Cut(target, "#"); ok {
		target = before
	}
	if _, after, ok := strings.Cut(target, "@"); ok {
		target = after
	}
	return trimHost(target)
}

func snapshotSession(s *sidecarSession, now time.Time) sessionSnapshot {
	_, expired := s.stateAt(now)
	if expired {
		s.finalize()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return sessionSnapshot{
		id:             s.id,
		ownerIdentity:  s.ownerIdentity,
		backendType:    s.backendType,
		rootPath:       s.rootPath,
		redactedTarget: s.redactedTarget,
		mode:           s.mode,
		state:          s.state,
		externalRef:    s.externalRef,
		openedAt:       s.openedAt,
		lastUsedAt:     s.lastUsedAt,
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
