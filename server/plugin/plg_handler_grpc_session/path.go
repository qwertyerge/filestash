package plg_handler_grpc_session

import (
	"path"
	"strings"

	. "github.com/mickael-kerjean/filestash/server/common"
)

type pathUse int

const (
	pathUseRead pathUse = iota
	pathUseMutateChild
)

type pathResolver struct {
	root string
}

func newPathResolver(root string) (pathResolver, error) {
	root = path.Clean(root)
	if root == "." || !strings.HasPrefix(root, "/") {
		return pathResolver{}, ErrNotValid
	}

	return pathResolver{root: root}, nil
}

func (r pathResolver) resolve(input string, usage pathUse) (string, error) {
	if r.root == "" {
		return "", ErrFilesystemError
	}

	if input == "" || input == "." {
		if usage == pathUseRead {
			return r.root, nil
		}
		return "", ErrNotValid
	}

	if path.IsAbs(input) {
		return "", ErrNotValid
	}

	input = path.Clean(input)
	if input == "." {
		if usage == pathUseRead {
			return r.root, nil
		}
		return "", ErrNotValid
	}
	if input == ".." || strings.HasPrefix(input, "../") {
		return "", ErrNotValid
	}

	resolved := path.Join(r.root, input)
	if r.root != "/" && !strings.HasPrefix(resolved, r.root+"/") {
		return "", ErrNotValid
	}

	return resolved, nil
}
