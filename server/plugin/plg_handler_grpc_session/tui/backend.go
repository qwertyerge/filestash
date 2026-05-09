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
	"dav",
	"s3",
	"gdrive",
	"dropbox",
}

func BackendFields(backend string) []Field {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "local":
		return []Field{{Name: "location", Sensitive: IsSensitiveField("location")}}
	case "tmp":
		return nil
	case "sftp", "ftp":
		return []Field{
			{Name: "hostname", Sensitive: IsSensitiveField("hostname")},
			{Name: "port", Sensitive: IsSensitiveField("port")},
			{Name: "username", Sensitive: IsSensitiveField("username")},
			{Name: "password", Sensitive: IsSensitiveField("password")},
		}
	case "webdav", "dav":
		return []Field{
			{Name: "url", Sensitive: IsSensitiveField("url")},
			{Name: "username", Sensitive: IsSensitiveField("username")},
			{Name: "password", Sensitive: IsSensitiveField("password")},
		}
	case "s3":
		return []Field{
			{Name: "endpoint", Sensitive: IsSensitiveField("endpoint")},
			{Name: "region", Sensitive: IsSensitiveField("region")},
			{Name: "bucket", Sensitive: IsSensitiveField("bucket")},
			{Name: "access_key_id", Sensitive: IsSensitiveField("access_key_id")},
			{Name: "secret_access_key", Sensitive: IsSensitiveField("secret_access_key")},
		}
	default:
		return nil
	}
}

func IsSensitiveField(name string) bool {
	normalized := strings.ToLower(name)
	normalized = strings.ReplaceAll(normalized, "_", "")
	normalized = strings.ReplaceAll(normalized, "-", "")
	normalized = strings.ReplaceAll(normalized, " ", "")

	return strings.Contains(normalized, "password") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "key") ||
		strings.Contains(normalized, "credential")
}
