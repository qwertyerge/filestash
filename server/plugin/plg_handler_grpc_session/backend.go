package plg_handler_grpc_session

import (
	"os"
	"strings"
	"time"

	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
)

func fileInfoFromOS(info os.FileInfo) *pb.FileInfo {
	fileType := "file"
	if info.IsDir() {
		fileType = "directory"
	}
	return &pb.FileInfo{
		Name:          info.Name(),
		Type:          fileType,
		Size:          info.Size(),
		ModTimeUnixMs: unixMillis(info.ModTime()),
		Mode:          uint32(info.Mode()),
	}
}

func unixMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano() / int64(time.Millisecond)
}

func redactBackendParams(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "pass") ||
			strings.Contains(lower, "secret") ||
			strings.Contains(lower, "token") ||
			strings.Contains(lower, "key") ||
			strings.Contains(lower, "credential") {
			out[key] = "[REDACTED]"
			continue
		}
		out[key] = value
	}
	return out
}

func isWriteMode(mode pb.AccessMode) bool {
	return mode == pb.AccessMode_ACCESS_MODE_READ_WRITE
}
