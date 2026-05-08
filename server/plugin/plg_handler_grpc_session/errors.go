package plg_handler_grpc_session

import (
	"errors"

	. "github.com/mickael-kerjean/filestash/server/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var ErrSessionInactive = errors.New("session is not active")
var ErrShuttingDown = errors.New("sidecar server is shutting down")

func grpcError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}

	code := codes.Internal
	switch {
	case errors.Is(err, ErrNotAuthorized):
		code = codes.Unauthenticated
	case errors.Is(err, ErrPermissionDenied), errors.Is(err, ErrNotAllowed):
		code = codes.PermissionDenied
	case errors.Is(err, ErrNotFound):
		code = codes.NotFound
	case errors.Is(err, ErrConflict):
		code = codes.AlreadyExists
	case errors.Is(err, ErrNotValid):
		code = codes.InvalidArgument
	case errors.Is(err, ErrTimeout), errors.Is(err, ErrSessionInactive):
		code = codes.FailedPrecondition
	case errors.Is(err, ErrNotReachable), errors.Is(err, ErrFilesystemError), errors.Is(err, ErrShuttingDown):
		code = codes.Unavailable
	}
	return status.Error(code, err.Error())
}
