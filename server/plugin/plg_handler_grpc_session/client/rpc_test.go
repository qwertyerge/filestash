package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestUnaryMethodsConstructRequestsAndReturnGeneratedResponses(t *testing.T) {
	fake := &fakeGeneratedClient{}
	client := NewFromGenerated(fake)
	ctx := context.Background()

	openReq := &pb.OpenRequest{BackendType: "local", RootPath: "/tmp"}
	openResp, err := client.Open(ctx, openReq)
	if err != nil {
		t.Fatal(err)
	}
	if openResp != fake.openResp || fake.openReq != openReq {
		t.Fatalf("open response/request mismatch")
	}

	if err := client.Close(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if fake.closeReq.GetSessionId() != "s1" {
		t.Fatalf("close request mismatch")
	}

	lease, err := client.Renew(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if lease != fake.renewResp || fake.renewReq.GetSessionId() != "s1" {
		t.Fatalf("renew response/request mismatch")
	}

	info, err := client.GetSession(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if info != fake.getResp || fake.getReq.GetSessionId() != "s1" {
		t.Fatalf("get response/request mismatch")
	}

	sessions, err := client.ListSessions(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if sessions != nil || !fake.listSessionsReq.GetIncludeClosed() {
		t.Fatalf("list sessions response/request mismatch")
	}

	if err := client.ForceClose(ctx, "s1", "operator"); err != nil {
		t.Fatal(err)
	}
	if fake.forceReq.GetSessionId() != "s1" || fake.forceReq.GetReason() != "operator" {
		t.Fatalf("force close request mismatch")
	}

	listFiles, err := client.List(ctx, "s1", "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(listFiles) != 0 || fake.listReq.GetSessionId() != "s1" || fake.listReq.GetPath() != "/" {
		t.Fatalf("list response/request mismatch")
	}

	statFile, err := client.Stat(ctx, "s1", "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if statFile.GetName() != "note.txt" || fake.statReq.GetSessionId() != "s1" || fake.statReq.GetPath() != "note.txt" {
		t.Fatalf("stat response/request mismatch")
	}

	if err := client.Mkdir(ctx, "s1", "new"); err != nil {
		t.Fatal(err)
	}
	if fake.mkdirReq.GetSessionId() != "s1" || fake.mkdirReq.GetPath() != "new" {
		t.Fatalf("mkdir request mismatch")
	}

	if err := client.Remove(ctx, "s1", "old"); err != nil {
		t.Fatal(err)
	}
	if fake.removeReq.GetSessionId() != "s1" || fake.removeReq.GetPath() != "old" {
		t.Fatalf("remove request mismatch")
	}

	if err := client.Rename(ctx, "s1", "old", "new"); err != nil {
		t.Fatal(err)
	}
	if fake.renameReq.GetSessionId() != "s1" || fake.renameReq.GetFrom() != "old" || fake.renameReq.GetTo() != "new" {
		t.Fatalf("rename request mismatch")
	}

	if err := client.Touch(ctx, "s1", "note.txt"); err != nil {
		t.Fatal(err)
	}
	if fake.touchReq.GetSessionId() != "s1" || fake.touchReq.GetPath() != "note.txt" {
		t.Fatalf("touch request mismatch")
	}
}

func TestReadFileCollectsChunks(t *testing.T) {
	fake := &fakeGeneratedClient{readChunks: [][]byte{[]byte("he"), []byte("llo")}}
	client := NewFromGenerated(fake)

	got, err := client.ReadFile(context.Background(), "s1", "note.txt", 3, 5)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("read=%q", got)
	}
	if fake.readReq.GetSessionId() != "s1" || fake.readReq.GetPath() != "note.txt" ||
		fake.readReq.GetOffset() != 3 || fake.readReq.GetLimit() != 5 {
		t.Fatalf("read request=%+v", fake.readReq)
	}
}

func TestWriteFileSendsHeaderAndChunks(t *testing.T) {
	fake := &fakeGeneratedClient{}
	client := NewFromGenerated(fake)

	written, err := client.WriteFile(context.Background(), "s1", "out.txt", strings.NewReader("hello"), 5, false)
	if err != nil {
		t.Fatal(err)
	}
	if written != 5 {
		t.Fatalf("written=%d", written)
	}
	if len(fake.writeStream.sent) != 2 {
		t.Fatalf("sent messages=%d", len(fake.writeStream.sent))
	}
	header := fake.writeStream.sent[0].GetHeader()
	if header.GetSessionId() != "s1" || header.GetPath() != "out.txt" ||
		header.GetOverwrite() || header.GetExpectedSize() != 5 {
		t.Fatalf("header=%+v", header)
	}
	if got := string(bytes.Join(writeChunks(fake.writeStream.sent[1:]), nil)); got != "hello" {
		t.Fatalf("chunks=%q", got)
	}
	if !fake.writeStream.closed {
		t.Fatal("stream was not closed")
	}
}

func TestWriteFileReturnsConflictForCallerControlledRetry(t *testing.T) {
	fake := &fakeGeneratedClient{writeErr: status.Error(codes.AlreadyExists, "exists")}
	client := NewFromGenerated(fake)

	_, err := client.WriteFile(context.Background(), "s1", "out.txt", strings.NewReader("hello"), 5, false)
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("err=%v", err)
	}
}

func TestWriteFileReturnsCloseStatusAfterSendError(t *testing.T) {
	fake := &fakeGeneratedClient{
		writeSendErr: io.ErrClosedPipe,
		writeErr:     status.Error(codes.AlreadyExists, "exists"),
	}
	client := NewFromGenerated(fake)

	_, err := client.WriteFile(context.Background(), "s1", "out.txt", strings.NewReader("hello"), 5, false)
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("err=%v", err)
	}
	if !fake.writeStream.closed {
		t.Fatal("expected CloseAndRecv after send error")
	}
}

func TestWriteFileCancelsStreamOnReaderError(t *testing.T) {
	readErr := errors.New("local read failed")
	fake := &fakeGeneratedClient{}
	client := NewFromGenerated(fake)

	_, err := client.WriteFile(context.Background(), "s1", "out.txt", errorReader{err: readErr}, 0, false)
	if !errors.Is(err, readErr) {
		t.Fatalf("err=%v", err)
	}
	if fake.writeCtx == nil {
		t.Fatal("missing write context")
	}
	select {
	case <-fake.writeCtx.Done():
	default:
		t.Fatal("stream context was not canceled")
	}
}

func writeChunks(reqs []*pb.WriteFileRequest) [][]byte {
	chunks := make([][]byte, 0, len(reqs))
	for _, req := range reqs {
		chunks = append(chunks, req.GetData())
	}
	return chunks
}

type fakeGeneratedClient struct {
	openReq          *pb.OpenRequest
	openResp         *pb.OpenResponse
	closeReq         *pb.CloseSessionRequest
	closeResp        *pb.CloseSessionResponse
	renewReq         *pb.RenewSessionRequest
	renewResp        *pb.SessionLease
	getReq           *pb.GetSessionRequest
	getResp          *pb.SessionInfo
	listSessionsReq  *pb.ListSessionsRequest
	listSessionsResp *pb.ListSessionsResponse
	forceReq         *pb.ForceCloseRequest
	forceResp        *pb.CloseSessionResponse
	listReq          *pb.ListRequest
	listResp         *pb.ListResponse
	statReq          *pb.StatRequest
	statResp         *pb.StatResponse
	readReq          *pb.ReadFileRequest
	readChunks       [][]byte
	writeStream      *fakeWriteFileStream
	writeCtx         context.Context
	writeSendErr     error
	writeErr         error
	mkdirReq         *pb.MkdirRequest
	mkdirResp        *pb.MutationResponse
	removeReq        *pb.RemoveRequest
	removeResp       *pb.MutationResponse
	renameReq        *pb.RenameRequest
	renameResp       *pb.MutationResponse
	touchReq         *pb.TouchRequest
	touchResp        *pb.MutationResponse
}

func (f *fakeGeneratedClient) Open(ctx context.Context, in *pb.OpenRequest, opts ...grpc.CallOption) (*pb.OpenResponse, error) {
	f.openReq = in
	if f.openResp == nil {
		f.openResp = &pb.OpenResponse{SessionId: "s1"}
	}
	return f.openResp, nil
}

func (f *fakeGeneratedClient) Close(ctx context.Context, in *pb.CloseSessionRequest, opts ...grpc.CallOption) (*pb.CloseSessionResponse, error) {
	f.closeReq = in
	if f.closeResp == nil {
		f.closeResp = &pb.CloseSessionResponse{SessionId: in.GetSessionId(), State: pb.SessionState_SESSION_STATE_CLOSED}
	}
	return f.closeResp, nil
}

func (f *fakeGeneratedClient) RenewSession(ctx context.Context, in *pb.RenewSessionRequest, opts ...grpc.CallOption) (*pb.SessionLease, error) {
	f.renewReq = in
	if f.renewResp == nil {
		f.renewResp = &pb.SessionLease{SessionId: in.GetSessionId()}
	}
	return f.renewResp, nil
}

func (f *fakeGeneratedClient) GetSession(ctx context.Context, in *pb.GetSessionRequest, opts ...grpc.CallOption) (*pb.SessionInfo, error) {
	f.getReq = in
	if f.getResp == nil {
		f.getResp = &pb.SessionInfo{SessionId: in.GetSessionId()}
	}
	return f.getResp, nil
}

func (f *fakeGeneratedClient) ListSessions(ctx context.Context, in *pb.ListSessionsRequest, opts ...grpc.CallOption) (*pb.ListSessionsResponse, error) {
	f.listSessionsReq = in
	if f.listSessionsResp == nil {
		f.listSessionsResp = &pb.ListSessionsResponse{}
	}
	return f.listSessionsResp, nil
}

func (f *fakeGeneratedClient) ForceClose(ctx context.Context, in *pb.ForceCloseRequest, opts ...grpc.CallOption) (*pb.CloseSessionResponse, error) {
	f.forceReq = in
	if f.forceResp == nil {
		f.forceResp = &pb.CloseSessionResponse{SessionId: in.GetSessionId(), State: pb.SessionState_SESSION_STATE_CLOSED}
	}
	return f.forceResp, nil
}

func (f *fakeGeneratedClient) List(ctx context.Context, in *pb.ListRequest, opts ...grpc.CallOption) (*pb.ListResponse, error) {
	f.listReq = in
	if f.listResp == nil {
		f.listResp = &pb.ListResponse{}
	}
	return f.listResp, nil
}

func (f *fakeGeneratedClient) Stat(ctx context.Context, in *pb.StatRequest, opts ...grpc.CallOption) (*pb.StatResponse, error) {
	f.statReq = in
	if f.statResp == nil {
		f.statResp = &pb.StatResponse{File: &pb.FileInfo{Name: in.GetPath()}}
	}
	return f.statResp, nil
}

func (f *fakeGeneratedClient) ReadFile(ctx context.Context, in *pb.ReadFileRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[pb.ReadFileChunk], error) {
	f.readReq = in
	return &fakeReadFileStream{chunks: f.readChunks}, nil
}

func (f *fakeGeneratedClient) WriteFile(ctx context.Context, opts ...grpc.CallOption) (grpc.ClientStreamingClient[pb.WriteFileRequest, pb.WriteFileResponse], error) {
	f.writeCtx = ctx
	f.writeStream = &fakeWriteFileStream{sendErr: f.writeSendErr, err: f.writeErr}
	return f.writeStream, nil
}

func (f *fakeGeneratedClient) Mkdir(ctx context.Context, in *pb.MkdirRequest, opts ...grpc.CallOption) (*pb.MutationResponse, error) {
	f.mkdirReq = in
	if f.mkdirResp == nil {
		f.mkdirResp = &pb.MutationResponse{Ok: true}
	}
	return f.mkdirResp, nil
}

func (f *fakeGeneratedClient) Remove(ctx context.Context, in *pb.RemoveRequest, opts ...grpc.CallOption) (*pb.MutationResponse, error) {
	f.removeReq = in
	if f.removeResp == nil {
		f.removeResp = &pb.MutationResponse{Ok: true}
	}
	return f.removeResp, nil
}

func (f *fakeGeneratedClient) Rename(ctx context.Context, in *pb.RenameRequest, opts ...grpc.CallOption) (*pb.MutationResponse, error) {
	f.renameReq = in
	if f.renameResp == nil {
		f.renameResp = &pb.MutationResponse{Ok: true}
	}
	return f.renameResp, nil
}

func (f *fakeGeneratedClient) Touch(ctx context.Context, in *pb.TouchRequest, opts ...grpc.CallOption) (*pb.MutationResponse, error) {
	f.touchReq = in
	if f.touchResp == nil {
		f.touchResp = &pb.MutationResponse{Ok: true}
	}
	return f.touchResp, nil
}

type fakeReadFileStream struct {
	fakeClientStream
	chunks [][]byte
	index  int
}

func (s *fakeReadFileStream) Recv() (*pb.ReadFileChunk, error) {
	if s.index >= len(s.chunks) {
		return nil, io.EOF
	}
	chunk := &pb.ReadFileChunk{Data: s.chunks[s.index]}
	s.index++
	return chunk, nil
}

type fakeWriteFileStream struct {
	fakeClientStream
	sent    []*pb.WriteFileRequest
	closed  bool
	sendErr error
	err     error
}

func (s *fakeWriteFileStream) Send(req *pb.WriteFileRequest) error {
	if s.sendErr != nil {
		return s.sendErr
	}
	s.sent = append(s.sent, req)
	return nil
}

func (s *fakeWriteFileStream) CloseAndRecv() (*pb.WriteFileResponse, error) {
	s.closed = true
	if s.err != nil {
		return nil, s.err
	}
	var total int64
	for _, req := range s.sent {
		total += int64(len(req.GetData()))
	}
	return &pb.WriteFileResponse{BytesWritten: total}, nil
}

type fakeClientStream struct{}

func (fakeClientStream) Header() (metadata.MD, error) { return nil, nil }
func (fakeClientStream) Trailer() metadata.MD         { return nil }
func (fakeClientStream) CloseSend() error             { return nil }
func (fakeClientStream) Context() context.Context     { return context.Background() }
func (fakeClientStream) SendMsg(m any) error          { return nil }
func (fakeClientStream) RecvMsg(m any) error          { return io.EOF }

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}
