package plg_handler_grpc_session

import (
	"errors"

	. "github.com/mickael-kerjean/filestash/server/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func grpcError(err error) error {
	if err == nil {
		return nil
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
	case errors.Is(err, ErrNotValid), errors.Is(err, ErrFilesystemError):
		code = codes.InvalidArgument
	case errors.Is(err, ErrTimeout):
		code = codes.FailedPrecondition
	case errors.Is(err, ErrNotReachable):
		code = codes.Unavailable
	}
	return status.Error(code, err.Error())
}
