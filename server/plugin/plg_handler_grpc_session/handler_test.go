package plg_handler_grpc_session

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/mickael-kerjean/filestash/server/common"
	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func TestRenewSessionRPCRequiresOwner(t *testing.T) {
	svc := newTestSidecarService(t, nil, []openSessionInput{testOpenInput("client-a", "s1")})

	lease, err := svc.RenewSession(identityContext("client-a"), &pb.RenewSessionRequest{SessionId: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if lease.GetSessionId() != "s1" {
		t.Fatalf("session_id=%q", lease.GetSessionId())
	}
	if !lease.GetRenewable() {
		t.Fatal("expected renewable lease")
	}

	_, err = svc.RenewSession(identityContext("client-b"), &pb.RenewSessionRequest{SessionId: "s1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
}

func TestCloseSessionRPCIsIdempotent(t *testing.T) {
	backend := &closeTrackingBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{testOpenInputWithBackend("client-a", "s1", backend)})

	for i := 0; i < 2; i++ {
		res, err := svc.Close(identityContext("client-a"), &pb.CloseSessionRequest{SessionId: "s1"})
		if err != nil {
			t.Fatal(err)
		}
		if res.GetSessionId() != "s1" || res.GetState() != pb.SessionState_SESSION_STATE_CLOSED {
			t.Fatalf("unexpected close response: %+v", res)
		}
	}
	if backend.closed != 1 {
		t.Fatalf("closed=%d", backend.closed)
	}
}

func TestIdentityFromContextMissingOrBlankTLSIdentity(t *testing.T) {
	if _, err := identityFromContext(context.Background()); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing peer code=%s err=%v", status.Code(err), err)
	}

	noCertCtx := peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{}},
	})
	if _, err := identityFromContext(noCertCtx); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing cert code=%s err=%v", status.Code(err), err)
	}

	blankCtx := peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}},
	})
	if _, err := identityFromContext(blankCtx); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("blank identity code=%s err=%v", status.Code(err), err)
	}
}

func TestForceCloseOperatorCanCloseAnotherOwnersSession(t *testing.T) {
	policies := testPolicyEngine(t, map[string]string{
		"client-a": "",
		"operator": "operator",
	})
	svc := newTestSidecarService(t, policies, []openSessionInput{testOpenInput("client-a", "s1")})

	res, err := svc.ForceClose(identityContext("operator"), &pb.ForceCloseRequest{SessionId: "s1", Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetSessionId() != "s1" || res.GetState() != pb.SessionState_SESSION_STATE_CLOSED {
		t.Fatalf("unexpected force close response: %+v", res)
	}
}

func TestForceCloseNonOperatorIsPermissionDenied(t *testing.T) {
	policies := testPolicyEngine(t, map[string]string{
		"client-a": "",
		"client-b": "",
	})
	svc := newTestSidecarService(t, policies, []openSessionInput{testOpenInput("client-a", "s1")})

	_, err := svc.ForceClose(identityContext("client-b"), &pb.ForceCloseRequest{SessionId: "s1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
}

func TestSidecarServiceRejectsBlankSessionID(t *testing.T) {
	policies := testPolicyEngine(t, map[string]string{"operator": "operator"})
	svc := newTestSidecarService(t, policies, nil)
	ctx := identityContext("operator")

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "renew",
			call: func() error {
				_, err := svc.RenewSession(ctx, &pb.RenewSessionRequest{})
				return err
			},
		},
		{
			name: "close",
			call: func() error {
				_, err := svc.Close(ctx, &pb.CloseSessionRequest{})
				return err
			},
		},
		{
			name: "get",
			call: func() error {
				_, err := svc.GetSession(ctx, &pb.GetSessionRequest{})
				return err
			},
		},
		{
			name: "force close",
			call: func() error {
				_, err := svc.ForceClose(ctx, &pb.ForceCloseRequest{})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("code=%s err=%v", status.Code(err), err)
			}
		})
	}
}

func TestGetSessionReturnsRedactedMetadataAndRejectsNonOwner(t *testing.T) {
	svc := newTestSidecarService(t, nil, []openSessionInput{testOpenInput("client-a", "s1")})

	info, err := svc.GetSession(identityContext("client-a"), &pb.GetSessionRequest{SessionId: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if info.GetSessionId() != "s1" ||
		info.GetOwnerIdentity() != "client-a" ||
		info.GetBackendType() != "sftp" ||
		info.GetRootPath() != "/work" ||
		info.GetMode() != pb.AccessMode_ACCESS_MODE_READ_WRITE ||
		info.GetState() != pb.SessionState_SESSION_STATE_ACTIVE {
		t.Fatalf("unexpected session info: %+v", info)
	}
	if info.GetExternalRef().GetKind() != "job" || info.GetExternalRef().GetId() != "job-s1" {
		t.Fatalf("unexpected external ref: %+v", info.GetExternalRef())
	}
	if info.GetRedactedTarget() != "" {
		t.Fatalf("redacted target=%q", info.GetRedactedTarget())
	}
	if info.GetOpenedAt() == nil || info.GetLastUsedAt() == nil || info.GetLease() == nil {
		t.Fatalf("missing timestamps or lease: %+v", info)
	}

	_, err = svc.GetSession(identityContext("client-b"), &pb.GetSessionRequest{SessionId: "s1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
}

func TestListSessionsFiltersOwnershipAndClosedSessions(t *testing.T) {
	policies := testPolicyEngine(t, map[string]string{
		"client-a": "",
		"client-b": "",
		"operator": "operator",
	})
	svc := newTestSidecarService(t, policies, []openSessionInput{
		testOpenInput("client-a", "s1"),
		testOpenInput("client-b", "s2"),
		testOpenInput("client-a", "s3"),
	})
	if _, err := svc.Close(identityContext("client-a"), &pb.CloseSessionRequest{SessionId: "s3"}); err != nil {
		t.Fatal(err)
	}

	clientList, err := svc.ListSessions(identityContext("client-a"), &pb.ListSessionsRequest{IncludeClosed: false})
	if err != nil {
		t.Fatal(err)
	}
	assertSessionIDs(t, clientList.GetSessions(), []string{"s1"})

	clientWithClosed, err := svc.ListSessions(identityContext("client-a"), &pb.ListSessionsRequest{IncludeClosed: true})
	if err != nil {
		t.Fatal(err)
	}
	assertSessionIDs(t, clientWithClosed.GetSessions(), []string{"s1", "s3"})

	operatorList, err := svc.ListSessions(identityContext("operator"), &pb.ListSessionsRequest{IncludeClosed: false})
	if err != nil {
		t.Fatal(err)
	}
	assertSessionIDs(t, operatorList.GetSessions(), []string{"s1", "s2"})
}

func TestGrpcErrorMapsConflictToAlreadyExists(t *testing.T) {
	err := grpcError(ErrConflict)
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if status.Code(grpcError(ErrFilesystemError)) != codes.Unavailable {
		t.Fatalf("filesystem code=%s", status.Code(grpcError(ErrFilesystemError)))
	}
	if grpcError(nil) != nil {
		t.Fatal("nil error should map to nil")
	}
	if status.Code(grpcError(errors.New("boom"))) != codes.Internal {
		t.Fatalf("default code=%s", status.Code(grpcError(errors.New("boom"))))
	}
}

func TestSidecarServiceConstructorRequiresProductionDependencies(t *testing.T) {
	sessions := newSessionManager(sessionManagerOptions{})
	policies := testPolicyEngine(t, map[string]string{"operator": "operator"})

	if _, err := newSidecarService(sessions, policies); err != nil {
		t.Fatal(err)
	}
	if _, err := newSidecarService(nil, policies); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("nil sessions code=%s err=%v", status.Code(err), err)
	}
	if _, err := newSidecarService(sessions, nil); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("nil policies code=%s err=%v", status.Code(err), err)
	}
}

func TestSidecarServiceRPCsRequireSessionManager(t *testing.T) {
	svc := &sidecarService{}
	ctx := identityContext("client-a")

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "renew",
			call: func() error {
				_, err := svc.RenewSession(ctx, &pb.RenewSessionRequest{SessionId: "s1"})
				return err
			},
		},
		{
			name: "close",
			call: func() error {
				_, err := svc.Close(ctx, &pb.CloseSessionRequest{SessionId: "s1"})
				return err
			},
		},
		{
			name: "get",
			call: func() error {
				_, err := svc.GetSession(ctx, &pb.GetSessionRequest{SessionId: "s1"})
				return err
			},
		},
		{
			name: "list",
			call: func() error {
				_, err := svc.ListSessions(ctx, &pb.ListSessionsRequest{})
				return err
			},
		},
		{
			name: "force close",
			call: func() error {
				_, err := svc.ForceClose(ctx, &pb.ForceCloseRequest{SessionId: "s1"})
				return err
			},
		},
		{
			name: "list files",
			call: func() error {
				_, err := svc.List(ctx, &pb.ListRequest{SessionId: "s1"})
				return err
			},
		},
		{
			name: "stat",
			call: func() error {
				_, err := svc.Stat(ctx, &pb.StatRequest{SessionId: "s1"})
				return err
			},
		},
		{
			name: "read file",
			call: func() error {
				return svc.ReadFile(&pb.ReadFileRequest{SessionId: "s1"}, newTestReadFileStream(ctx))
			},
		},
		{
			name: "write file",
			call: func() error {
				return svc.WriteFile(newTestWriteFileStream(ctx, []*pb.WriteFileRequest{
					writeFileHeader("s1", "note.txt", 0),
				}))
			},
		},
		{
			name: "mkdir",
			call: func() error {
				_, err := svc.Mkdir(ctx, &pb.MkdirRequest{SessionId: "s1"})
				return err
			},
		},
		{
			name: "remove",
			call: func() error {
				_, err := svc.Remove(ctx, &pb.RemoveRequest{SessionId: "s1"})
				return err
			},
		},
		{
			name: "rename",
			call: func() error {
				_, err := svc.Rename(ctx, &pb.RenameRequest{SessionId: "s1", From: "a", To: "b"})
				return err
			},
		},
		{
			name: "touch",
			call: func() error {
				_, err := svc.Touch(ctx, &pb.TouchRequest{SessionId: "s1"})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("code=%s err=%v", status.Code(err), err)
			}
		})
	}
}

func TestListRPCUsesSessionRootAndRelativePathResolution(t *testing.T) {
	backend := &filesystemRPCBackend{
		lsEntries: []os.FileInfo{
			fakeInfo{name: "report.txt", size: 11, mode: 0o644},
			fakeInfo{name: "docs", isDir: true, mode: os.ModeDir | 0o755},
		},
	}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})

	res, err := svc.List(identityContext("client-a"), &pb.ListRequest{SessionId: "s1", Path: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	if backend.lsPath != "/work/docs" {
		t.Fatalf("ls path=%q", backend.lsPath)
	}
	if len(res.GetFiles()) != 2 {
		t.Fatalf("file count=%d files=%+v", len(res.GetFiles()), res.GetFiles())
	}
	if res.GetFiles()[0].GetName() != "report.txt" || res.GetFiles()[1].GetType() != "directory" {
		t.Fatalf("unexpected files: %+v", res.GetFiles())
	}
}

func TestListRPCRootPathsUseSessionRoot(t *testing.T) {
	for _, inputPath := range []string{"", "."} {
		t.Run("path "+inputPath, func(t *testing.T) {
			backend := &filesystemRPCBackend{}
			svc := newTestSidecarService(t, nil, []openSessionInput{
				testOpenInputWithBackend("client-a", "s1", backend),
			})

			if _, err := svc.List(identityContext("client-a"), &pb.ListRequest{SessionId: "s1", Path: inputPath}); err != nil {
				t.Fatal(err)
			}
			if backend.lsPath != "/work" {
				t.Fatalf("ls path=%q", backend.lsPath)
			}
		})
	}
}

func TestStatRPCReturnsFileInfo(t *testing.T) {
	mtime := time.Unix(1_700_000_123, 456_000_000)
	backend := &filesystemRPCBackend{
		statInfo: fakeInfo{name: "report.txt", size: 99, mod: mtime, mode: 0o600},
	}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})

	res, err := svc.Stat(identityContext("client-a"), &pb.StatRequest{SessionId: "s1", Path: "docs/report.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if backend.statPath != "/work/docs/report.txt" {
		t.Fatalf("stat path=%q", backend.statPath)
	}
	if res.GetFile().GetName() != "report.txt" ||
		res.GetFile().GetType() != "file" ||
		res.GetFile().GetSize() != 99 ||
		res.GetFile().GetModTimeUnixMs() != unixMillis(mtime) {
		t.Fatalf("unexpected file info: %+v", res.GetFile())
	}
}

func TestStatRPCNilFileInfoReturnsGrpcError(t *testing.T) {
	backend := &filesystemRPCBackend{statNil: true}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})

	_, err := svc.Stat(identityContext("client-a"), &pb.StatRequest{SessionId: "s1", Path: "missing"})
	if status.Code(err) == codes.OK {
		t.Fatal("expected gRPC error")
	}
}

func TestMkdirRejectsReadOnlySession(t *testing.T) {
	backend := &filesystemRPCBackend{}
	input := testOpenInputWithBackend("client-a", "s1", backend)
	input.mode = pb.AccessMode_ACCESS_MODE_READ
	svc := newTestSidecarService(t, nil, []openSessionInput{input})

	_, err := svc.Mkdir(identityContext("client-a"), &pb.MkdirRequest{SessionId: "s1", Path: "docs"})
	if status.Code(err) != codes.PermissionDenied && status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.mkdirPath != "" {
		t.Fatalf("mkdir was called with path=%q", backend.mkdirPath)
	}
}

func TestRemoveRejectsMutatingRootPath(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})

	_, err := svc.Remove(identityContext("client-a"), &pb.RemoveRequest{SessionId: "s1", Path: "."})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.rmPath != "" {
		t.Fatalf("rm was called with path=%q", backend.rmPath)
	}
}

func TestRenameRejectsTraversalDestination(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})

	_, err := svc.Rename(identityContext("client-a"), &pb.RenameRequest{SessionId: "s1", From: "docs/report.txt", To: "../escape.txt"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.mvFrom != "" || backend.mvTo != "" {
		t.Fatalf("mv was called from=%q to=%q", backend.mvFrom, backend.mvTo)
	}
}

func TestReadFileStreamsDataWithOffsetLimit(t *testing.T) {
	backend := &filesystemRPCBackend{catData: "0123456789"}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestReadFileStream(identityContext("client-a"))

	err := svc.ReadFile(&pb.ReadFileRequest{SessionId: "s1", Path: "data.bin", Offset: 3, Limit: 4}, stream)
	if err != nil {
		t.Fatal(err)
	}
	if backend.catPath != "/work/data.bin" {
		t.Fatalf("cat path=%q", backend.catPath)
	}
	if !backend.catClosed {
		t.Fatal("reader was not closed")
	}
	if got := stream.data(); got != "3456" {
		t.Fatalf("streamed data=%q", got)
	}
}

func TestReadFileNilReaderReturnsGrpcError(t *testing.T) {
	backend := &filesystemRPCBackend{catNil: true}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestReadFileStream(identityContext("client-a"))

	err := svc.ReadFile(&pb.ReadFileRequest{SessionId: "s1", Path: "data.bin"}, stream)
	if status.Code(err) == codes.OK {
		t.Fatal("expected gRPC error")
	}
}

func TestWriteFileWritesChunksAndReportsBytesWritten(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", int64(len("hello sidecar"))),
		writeFileData("hello "),
		writeFileData("sidecar"),
	})

	if err := svc.WriteFile(stream); err != nil {
		t.Fatal(err)
	}
	if backend.savePath != "/work/out.txt" {
		t.Fatalf("save path=%q", backend.savePath)
	}
	if got := backend.savedData; got != "hello sidecar" {
		t.Fatalf("saved data=%q", got)
	}
	if stream.closedResponse == nil || stream.closedResponse.GetBytesWritten() != int64(len("hello sidecar")) {
		t.Fatalf("unexpected response: %+v", stream.closedResponse)
	}
}

func TestWriteFileNegativeExpectedSizeRejectsInvalidArgument(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", -1),
		writeFileData("hello"),
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestWriteFileExpectedSizeTooSmallRejectsBeforeSave(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", 3),
		writeFileData("hello"),
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestWriteFileExpectedSizeTooLargeRejectsBeforeSave(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", 8),
		writeFileData("hello"),
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestWriteFileSaveEarlyFailurePreservesBackendError(t *testing.T) {
	backend := &filesystemRPCBackend{saveErr: ErrFilesystemError}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", 0),
		writeFileData(strings.Repeat("x", 128*1024)),
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
}

func TestWriteFileSecondHeaderRejectsInvalidArgument(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", 0),
		writeFileHeader("s1", "other.txt", 0),
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestWriteFileSecondHeaderAfterDataRejectsBeforeSave(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", 0),
		writeFileData("hello"),
		writeFileHeader("s1", "other.txt", 0),
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestWriteFileUnsetPayloadAfterHeaderRejectsInvalidArgument(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", 0),
		{},
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestWriteFileUnsetPayloadAfterDataRejectsBeforeSave(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", 0),
		writeFileData("hello"),
		{},
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestWriteFileRejectsWhenStreamLimitExceededBeforeSave(t *testing.T) {
	original := PluginMaxStreamBytes
	PluginMaxStreamBytes = func() int64 { return 4 }
	t.Cleanup(func() { PluginMaxStreamBytes = original })

	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileHeader("s1", "out.txt", 0),
		writeFileData("hello"),
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestWriteFileMissingHeaderRejectsInvalidArgument(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	stream := newTestWriteFileStream(identityContext("client-a"), []*pb.WriteFileRequest{
		writeFileData("hello"),
	})

	err := svc.WriteFile(stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.savePath != "" {
		t.Fatalf("save was called with path=%q", backend.savePath)
	}
}

func TestFilesystemOperationRequiresOwner(t *testing.T) {
	backend := &filesystemRPCBackend{}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})

	_, err := svc.List(identityContext("client-b"), &pb.ListRequest{SessionId: "s1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code=%s err=%v", status.Code(err), err)
	}
	if backend.lsPath != "" {
		t.Fatalf("ls was called with path=%q", backend.lsPath)
	}
}

func TestFilesystemOperationMarksSessionUsedOnSuccess(t *testing.T) {
	backend := &filesystemRPCBackend{statInfo: fakeInfo{name: "report.txt"}}
	svc := newTestSidecarService(t, nil, []openSessionInput{
		testOpenInputWithBackend("client-a", "s1", backend),
	})
	before := svc.sessionManager.sessions["s1"].lastUsedAt
	later := before.Add(2 * time.Minute)
	svc.sessionManager.now = func() time.Time { return later }

	if _, err := svc.Stat(identityContext("client-a"), &pb.StatRequest{SessionId: "s1", Path: "report.txt"}); err != nil {
		t.Fatal(err)
	}
	session := svc.sessionManager.sessions["s1"]
	if !session.lastUsedAt.Equal(later) {
		t.Fatalf("last_used_at=%s want=%s", session.lastUsedAt, later)
	}
	if want := later.Add(5 * time.Minute); !session.idleExpiresAt.Equal(want) {
		t.Fatalf("idle_expires_at=%s want=%s", session.idleExpiresAt, want)
	}
}

func newTestSidecarService(t *testing.T, policies *policyEngine, inputs []openSessionInput) *sidecarService {
	t.Helper()
	now := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	ids := make([]string, 0, len(inputs))
	for _, in := range inputs {
		ids = append(ids, in.externalRef.id[len("job-"):])
	}
	nextID := 0
	m := newSessionManager(sessionManagerOptions{
		now: func() time.Time { return now },
		id: func() (string, error) {
			id := ids[nextID]
			nextID++
			return id, nil
		},
	})
	for _, in := range inputs {
		if _, err := m.open(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	return &sidecarService{sessionManager: m, policies: policies}
}

func testOpenInput(owner, id string) openSessionInput {
	return testOpenInputWithBackend(owner, id, &closeTrackingBackend{})
}

func testOpenInputWithBackend(owner, id string, backend IBackend) openSessionInput {
	return openSessionInput{
		ownerIdentity: owner,
		backendType:   "sftp",
		backend:       backend,
		rootPath:      "/work",
		mode:          pb.AccessMode_ACCESS_MODE_READ_WRITE,
		externalRef:   externalRef{kind: "job", id: "job-" + id},
		lease: effectiveLease{
			duration:    15 * time.Minute,
			idleTimeout: 5 * time.Minute,
			maxLifetime: time.Hour,
			renewable:   true,
		},
	}
}

type filesystemRPCBackend struct {
	Nothing

	lsPath    string
	lsEntries []os.FileInfo

	statPath string
	statInfo os.FileInfo
	statNil  bool

	catPath   string
	catData   string
	catNil    bool
	catClosed bool

	mkdirPath string
	rmPath    string
	mvFrom    string
	mvTo      string
	touchPath string
	savePath  string
	savedData string
	saveErr   error
}

func (b *filesystemRPCBackend) Ls(path string) ([]os.FileInfo, error) {
	b.lsPath = path
	return b.lsEntries, nil
}

func (b *filesystemRPCBackend) Stat(path string) (os.FileInfo, error) {
	b.statPath = path
	if b.statNil {
		return nil, nil
	}
	return b.statInfo, nil
}

func (b *filesystemRPCBackend) Cat(path string) (io.ReadCloser, error) {
	b.catPath = path
	if b.catNil {
		return nil, nil
	}
	return &closeTrackingReadCloser{
		reader: strings.NewReader(b.catData),
		close:  func() { b.catClosed = true },
	}, nil
}

func (b *filesystemRPCBackend) Mkdir(path string) error {
	b.mkdirPath = path
	return nil
}

func (b *filesystemRPCBackend) Rm(path string) error {
	b.rmPath = path
	return nil
}

func (b *filesystemRPCBackend) Mv(from string, to string) error {
	b.mvFrom = from
	b.mvTo = to
	return nil
}

func (b *filesystemRPCBackend) Touch(path string) error {
	b.touchPath = path
	return nil
}

func (b *filesystemRPCBackend) Save(path string, file io.Reader) error {
	b.savePath = path
	if b.saveErr != nil {
		return b.saveErr
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	b.savedData = string(data)
	return nil
}

type closeTrackingReadCloser struct {
	reader io.Reader
	close  func()
}

func (r *closeTrackingReadCloser) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r *closeTrackingReadCloser) Close() error {
	if r.close != nil {
		r.close()
	}
	return nil
}

type testReadFileStream struct {
	ctx    context.Context
	chunks [][]byte
}

func newTestReadFileStream(ctx context.Context) *testReadFileStream {
	return &testReadFileStream{ctx: ctx}
}

func (s *testReadFileStream) Send(chunk *pb.ReadFileChunk) error {
	s.chunks = append(s.chunks, append([]byte(nil), chunk.GetData()...))
	return nil
}

func (s *testReadFileStream) data() string {
	var out []byte
	for _, chunk := range s.chunks {
		out = append(out, chunk...)
	}
	return string(out)
}

func (s *testReadFileStream) SetHeader(metadata.MD) error {
	return nil
}

func (s *testReadFileStream) SendHeader(metadata.MD) error {
	return nil
}

func (s *testReadFileStream) SetTrailer(metadata.MD) {}

func (s *testReadFileStream) Context() context.Context {
	return s.ctx
}

func (s *testReadFileStream) SendMsg(any) error {
	return nil
}

func (s *testReadFileStream) RecvMsg(any) error {
	return io.EOF
}

type testWriteFileStream struct {
	ctx            context.Context
	requests       []*pb.WriteFileRequest
	closedResponse *pb.WriteFileResponse
}

func newTestWriteFileStream(ctx context.Context, requests []*pb.WriteFileRequest) *testWriteFileStream {
	return &testWriteFileStream{ctx: ctx, requests: requests}
}

func (s *testWriteFileStream) Recv() (*pb.WriteFileRequest, error) {
	if len(s.requests) == 0 {
		return nil, io.EOF
	}
	req := s.requests[0]
	s.requests = s.requests[1:]
	return req, nil
}

func (s *testWriteFileStream) SendAndClose(res *pb.WriteFileResponse) error {
	s.closedResponse = res
	return nil
}

func (s *testWriteFileStream) SetHeader(metadata.MD) error {
	return nil
}

func (s *testWriteFileStream) SendHeader(metadata.MD) error {
	return nil
}

func (s *testWriteFileStream) SetTrailer(metadata.MD) {}

func (s *testWriteFileStream) Context() context.Context {
	return s.ctx
}

func (s *testWriteFileStream) SendMsg(any) error {
	return nil
}

func (s *testWriteFileStream) RecvMsg(any) error {
	return io.EOF
}

func writeFileHeader(sessionID, path string, expectedSize int64) *pb.WriteFileRequest {
	return &pb.WriteFileRequest{
		Payload: &pb.WriteFileRequest_Header{
			Header: &pb.WriteFileHeader{
				SessionId:    sessionID,
				Path:         path,
				ExpectedSize: expectedSize,
			},
		},
	}
}

func writeFileData(data string) *pb.WriteFileRequest {
	return &pb.WriteFileRequest{
		Payload: &pb.WriteFileRequest_Data{Data: []byte(data)},
	}
}

func identityContext(identity string) context.Context {
	cert := &x509.Certificate{Subject: pkix.Name{CommonName: identity}}
	if u, err := url.Parse(identity); err == nil && u.Scheme != "" {
		cert.URIs = []*url.URL{u}
	}
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}},
	})
}

func testPolicyEngine(t *testing.T, clients map[string]string) *policyEngine {
	t.Helper()
	engine := &policyEngine{}
	for identity, role := range clients {
		engine.clients = append(engine.clients, clientPolicy{Identity: identity, Role: role})
	}
	return engine
}

func assertSessionIDs(t *testing.T, sessions []*pb.SessionInfo, want []string) {
	t.Helper()
	if len(sessions) != len(want) {
		t.Fatalf("session count=%d want=%d sessions=%+v", len(sessions), len(want), sessions)
	}
	got := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		got[s.GetSessionId()] = true
	}
	for _, id := range want {
		if !got[id] {
			t.Fatalf("missing session %q in %+v", id, sessions)
		}
	}
}
