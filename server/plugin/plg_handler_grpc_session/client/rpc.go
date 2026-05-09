package client

import (
	"bytes"
	"context"
	"io"

	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
	"google.golang.org/grpc"
)

const writeFileChunkSize = 32 * 1024

type SidecarClient interface {
	Open(ctx context.Context, req *pb.OpenRequest) (*pb.OpenResponse, error)
	Close(ctx context.Context, sessionID string) (*pb.CloseSessionResponse, error)
	RenewSession(ctx context.Context, sessionID string) (*pb.SessionLease, error)
	GetSession(ctx context.Context, sessionID string) (*pb.SessionInfo, error)
	ListSessions(ctx context.Context, includeClosed bool) (*pb.ListSessionsResponse, error)
	ForceClose(ctx context.Context, sessionID, reason string) (*pb.CloseSessionResponse, error)
	List(ctx context.Context, sessionID, path string) (*pb.ListResponse, error)
	Stat(ctx context.Context, sessionID, path string) (*pb.StatResponse, error)
	ReadFile(ctx context.Context, sessionID, path string, offset, limit int64) ([]byte, error)
	WriteFile(ctx context.Context, sessionID, path string, r io.Reader, overwrite bool, expectedSize int64) (int64, error)
	Mkdir(ctx context.Context, sessionID, path string) (*pb.MutationResponse, error)
	Remove(ctx context.Context, sessionID, path string) (*pb.MutationResponse, error)
	Rename(ctx context.Context, sessionID, from, to string) (*pb.MutationResponse, error)
	Touch(ctx context.Context, sessionID, path string) (*pb.MutationResponse, error)
}

type sidecarClient struct {
	generated pb.FilestashSidecarServiceClient
}

func New(conn *grpc.ClientConn) SidecarClient {
	return NewFromGenerated(pb.NewFilestashSidecarServiceClient(conn))
}

func NewFromGenerated(generated pb.FilestashSidecarServiceClient) SidecarClient {
	return &sidecarClient{generated: generated}
}

func (c *sidecarClient) Open(ctx context.Context, req *pb.OpenRequest) (*pb.OpenResponse, error) {
	return c.generated.Open(ctx, req)
}

func (c *sidecarClient) Close(ctx context.Context, sessionID string) (*pb.CloseSessionResponse, error) {
	return c.generated.Close(ctx, &pb.CloseSessionRequest{SessionId: sessionID})
}

func (c *sidecarClient) RenewSession(ctx context.Context, sessionID string) (*pb.SessionLease, error) {
	return c.generated.RenewSession(ctx, &pb.RenewSessionRequest{SessionId: sessionID})
}

func (c *sidecarClient) GetSession(ctx context.Context, sessionID string) (*pb.SessionInfo, error) {
	return c.generated.GetSession(ctx, &pb.GetSessionRequest{SessionId: sessionID})
}

func (c *sidecarClient) ListSessions(ctx context.Context, includeClosed bool) (*pb.ListSessionsResponse, error) {
	return c.generated.ListSessions(ctx, &pb.ListSessionsRequest{IncludeClosed: includeClosed})
}

func (c *sidecarClient) ForceClose(ctx context.Context, sessionID, reason string) (*pb.CloseSessionResponse, error) {
	return c.generated.ForceClose(ctx, &pb.ForceCloseRequest{SessionId: sessionID, Reason: reason})
}

func (c *sidecarClient) List(ctx context.Context, sessionID, path string) (*pb.ListResponse, error) {
	return c.generated.List(ctx, &pb.ListRequest{SessionId: sessionID, Path: path})
}

func (c *sidecarClient) Stat(ctx context.Context, sessionID, path string) (*pb.StatResponse, error) {
	return c.generated.Stat(ctx, &pb.StatRequest{SessionId: sessionID, Path: path})
}

func (c *sidecarClient) ReadFile(ctx context.Context, sessionID, path string, offset, limit int64) ([]byte, error) {
	stream, err := c.generated.ReadFile(ctx, &pb.ReadFileRequest{
		SessionId: sessionID,
		Path:      path,
		Offset:    offset,
		Limit:     limit,
	})
	if err != nil {
		return nil, err
	}

	var out bytes.Buffer
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			return out.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
		out.Write(chunk.GetData())
	}
}

func (c *sidecarClient) WriteFile(ctx context.Context, sessionID, path string, r io.Reader, overwrite bool, expectedSize int64) (int64, error) {
	stream, err := c.generated.WriteFile(ctx)
	if err != nil {
		return 0, err
	}

	if err := stream.Send(&pb.WriteFileRequest{
		Payload: &pb.WriteFileRequest_Header{
			Header: &pb.WriteFileHeader{
				SessionId:    sessionID,
				Path:         path,
				Overwrite:    overwrite,
				ExpectedSize: expectedSize,
			},
		},
	}); err != nil {
		return 0, err
	}

	buf := make([]byte, writeFileChunkSize)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			if err := stream.Send(&pb.WriteFileRequest{
				Payload: &pb.WriteFileRequest_Data{Data: data},
			}); err != nil {
				return 0, err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return 0, readErr
		}
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		return 0, err
	}
	return resp.GetBytesWritten(), nil
}

func (c *sidecarClient) Mkdir(ctx context.Context, sessionID, path string) (*pb.MutationResponse, error) {
	return c.generated.Mkdir(ctx, &pb.MkdirRequest{SessionId: sessionID, Path: path})
}

func (c *sidecarClient) Remove(ctx context.Context, sessionID, path string) (*pb.MutationResponse, error) {
	return c.generated.Remove(ctx, &pb.RemoveRequest{SessionId: sessionID, Path: path})
}

func (c *sidecarClient) Rename(ctx context.Context, sessionID, from, to string) (*pb.MutationResponse, error) {
	return c.generated.Rename(ctx, &pb.RenameRequest{SessionId: sessionID, From: from, To: to})
}

func (c *sidecarClient) Touch(ctx context.Context, sessionID, path string) (*pb.MutationResponse, error) {
	return c.generated.Touch(ctx, &pb.TouchRequest{SessionId: sessionID, Path: path})
}
