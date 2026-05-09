package tui

import "strings"

type Field struct {
	Name      string
	Sensitive bool
}

var DefaultBackendTypes = []string{
	"local",
	"tmp",
	"sftp",
	"ftp",
	"webdav",
	"s3",
	"gdrive",
	"dropbox",
}

func BackendFields(backend string) []Field {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "local":
		return fields("password")
	case "tmp":
		return fields("userID")
	case "sftp", "ftp":
		if strings.EqualFold(strings.TrimSpace(backend), "sftp") {
			return fields("hostname", "username", "password", "path", "port", "passphrase", "hostkey")
		}
		return fields("hostname", "username", "password", "path", "port", "conn")
	case "webdav":
		return fields("url", "username", "password", "path")
	case "s3":
		return fields("access_key_id", "secret_access_key", "region", "endpoint", "role_arn", "session_token", "path", "encryption_key", "number_thread", "timeout")
	case "gdrive":
		return fields("token", "refresh", "expiry")
	case "dropbox":
		return fields("access_token")
	default:
		return nil
	}
}

func fields(names ...string) []Field {
	out := make([]Field, 0, len(names))
	for _, name := range names {
		out = append(out, Field{Name: name, Sensitive: IsSensitiveField(name)})
	}
	return out
}

func IsSensitiveField(name string) bool {
	normalized := strings.ToLower(name)
	normalized = strings.ReplaceAll(normalized, "_", "")
	normalized = strings.ReplaceAll(normalized, "-", "")
	normalized = strings.ReplaceAll(normalized, " ", "")

	return strings.Contains(normalized, "password") ||
		strings.Contains(normalized, "pass") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "key") ||
		strings.Contains(normalized, "credential") ||
		strings.Contains(normalized, "refresh") ||
		strings.Contains(normalized, "grant")
}
