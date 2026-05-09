package client

import (
	"bytes"
	"context"
	"io"

	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
	"google.golang.org/grpc"
)

const writeFileChunkSize = 32 * 1024

type Sidecar interface {
	Open(ctx context.Context, req *pb.OpenRequest) (*pb.OpenResponse, error)
	Close(ctx context.Context, sessionID string) error
	Renew(ctx context.Context, sessionID string) (*pb.SessionLease, error)
	GetSession(ctx context.Context, sessionID string) (*pb.SessionInfo, error)
	ListSessions(ctx context.Context, includeClosed bool) ([]*pb.SessionInfo, error)
	ForceClose(ctx context.Context, sessionID, reason string) error
	List(ctx context.Context, sessionID, path string) ([]*pb.FileInfo, error)
	Stat(ctx context.Context, sessionID, path string) (*pb.FileInfo, error)
	ReadFile(ctx context.Context, sessionID, path string, offset, limit int64) ([]byte, error)
	WriteFile(ctx context.Context, sessionID, path string, r io.Reader, expectedSize int64, overwrite bool) (int64, error)
	Mkdir(ctx context.Context, sessionID, path string) error
	Remove(ctx context.Context, sessionID, path string) error
	Rename(ctx context.Context, sessionID, from, to string) error
	Touch(ctx context.Context, sessionID, path string) error
}

type sidecarClient struct {
	generated pb.FilestashSidecarServiceClient
}

func New(conn *grpc.ClientConn) Sidecar {
	return NewFromGenerated(pb.NewFilestashSidecarServiceClient(conn))
}

func NewFromGenerated(generated pb.FilestashSidecarServiceClient) Sidecar {
	return &sidecarClient{generated: generated}
}

func (c *sidecarClient) Open(ctx context.Context, req *pb.OpenRequest) (*pb.OpenResponse, error) {
	return c.generated.Open(ctx, req)
}

func (c *sidecarClient) Close(ctx context.Context, sessionID string) error {
	_, err := c.generated.Close(ctx, &pb.CloseSessionRequest{SessionId: sessionID})
	return err
}

func (c *sidecarClient) Renew(ctx context.Context, sessionID string) (*pb.SessionLease, error) {
	return c.generated.RenewSession(ctx, &pb.RenewSessionRequest{SessionId: sessionID})
}

func (c *sidecarClient) GetSession(ctx context.Context, sessionID string) (*pb.SessionInfo, error) {
	return c.generated.GetSession(ctx, &pb.GetSessionRequest{SessionId: sessionID})
}

func (c *sidecarClient) ListSessions(ctx context.Context, includeClosed bool) ([]*pb.SessionInfo, error) {
	resp, err := c.generated.ListSessions(ctx, &pb.ListSessionsRequest{IncludeClosed: includeClosed})
	if err != nil {
		return nil, err
	}
	return resp.GetSessions(), nil
}

func (c *sidecarClient) ForceClose(ctx context.Context, sessionID, reason string) error {
	_, err := c.generated.ForceClose(ctx, &pb.ForceCloseRequest{SessionId: sessionID, Reason: reason})
	return err
}

func (c *sidecarClient) List(ctx context.Context, sessionID, path string) ([]*pb.FileInfo, error) {
	resp, err := c.generated.List(ctx, &pb.ListRequest{SessionId: sessionID, Path: path})
	if err != nil {
		return nil, err
	}
	return resp.GetFiles(), nil
}

func (c *sidecarClient) Stat(ctx context.Context, sessionID, path string) (*pb.FileInfo, error) {
	resp, err := c.generated.Stat(ctx, &pb.StatRequest{SessionId: sessionID, Path: path})
	if err != nil {
		return nil, err
	}
	return resp.GetFile(), nil
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

func (c *sidecarClient) WriteFile(ctx context.Context, sessionID, path string, r io.Reader, expectedSize int64, overwrite bool) (int64, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := c.generated.WriteFile(streamCtx)
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
		return 0, closeAndRecvError(stream, err)
	}

	buf := make([]byte, writeFileChunkSize)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			if err := stream.Send(&pb.WriteFileRequest{
				Payload: &pb.WriteFileRequest_Data{Data: data},
			}); err != nil {
				return 0, closeAndRecvError(stream, err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			cancel()
			return 0, readErr
		}
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		return 0, err
	}
	return resp.GetBytesWritten(), nil
}

func closeAndRecvError(stream grpc.ClientStreamingClient[pb.WriteFileRequest, pb.WriteFileResponse], sendErr error) error {
	_, closeErr := stream.CloseAndRecv()
	if closeErr != nil {
		return closeErr
	}
	return sendErr
}

func (c *sidecarClient) Mkdir(ctx context.Context, sessionID, path string) error {
	_, err := c.generated.Mkdir(ctx, &pb.MkdirRequest{SessionId: sessionID, Path: path})
	return err
}

func (c *sidecarClient) Remove(ctx context.Context, sessionID, path string) error {
	_, err := c.generated.Remove(ctx, &pb.RemoveRequest{SessionId: sessionID, Path: path})
	return err
}

func (c *sidecarClient) Rename(ctx context.Context, sessionID, from, to string) error {
	_, err := c.generated.Rename(ctx, &pb.RenameRequest{SessionId: sessionID, From: from, To: to})
	return err
}

func (c *sidecarClient) Touch(ctx context.Context, sessionID, path string) error {
	_, err := c.generated.Touch(ctx, &pb.TouchRequest{SessionId: sessionID, Path: path})
	return err
}
