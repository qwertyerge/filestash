package plg_handler_grpc_session

import (
	"os"
	"testing"
	"time"

	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
)

type fakeInfo struct {
	name  string
	isDir bool
	size  int64
	mod   time.Time
	mode  os.FileMode
}

func (f fakeInfo) Name() string {
	return f.name
}

func (f fakeInfo) Size() int64 {
	return f.size
}

func (f fakeInfo) Mode() os.FileMode {
	return f.mode
}

func (f fakeInfo) ModTime() time.Time {
	return f.mod
}

func (f fakeInfo) IsDir() bool {
	return f.isDir
}

func (f fakeInfo) Sys() any {
	return nil
}

func TestFileInfoFromOS(t *testing.T) {
	mtime := time.Unix(1_700_000_123, 456_000_000)

	t.Run("directory", func(t *testing.T) {
		got := fileInfoFromOS(fakeInfo{name: "docs", isDir: true, size: 42, mod: mtime, mode: os.ModeDir | 0o755})
		if got.Name != "docs" || got.Type != "directory" || got.Size != 42 {
			t.Fatalf("unexpected file info: %+v", got)
		}
		if got.ModTimeUnixMs != mtime.UnixNano()/int64(time.Millisecond) {
			t.Fatalf("mod_time_unix_ms=%d", got.ModTimeUnixMs)
		}
		if got.Mode != uint32(os.ModeDir|0o755) {
			t.Fatalf("mode=%d", got.Mode)
		}
	})

	t.Run("file", func(t *testing.T) {
		got := fileInfoFromOS(fakeInfo{name: "note.txt", size: 7, mod: mtime, mode: 0o644})
		if got.Name != "note.txt" || got.Type != "file" || got.Size != 7 {
			t.Fatalf("unexpected file info: %+v", got)
		}
		if got.ModTimeUnixMs != mtime.UnixNano()/int64(time.Millisecond) {
			t.Fatalf("mod_time_unix_ms=%d", got.ModTimeUnixMs)
		}
		if got.Mode != uint32(0o644) {
			t.Fatalf("mode=%d", got.Mode)
		}
	})
}

func TestUnixMillis(t *testing.T) {
	if got := unixMillis(time.Time{}); got != 0 {
		t.Fatalf("expected zero, got %d", got)
	}

	mtime := time.Unix(1_700_000_123, 456_000_000)
	if got := unixMillis(mtime); got != mtime.UnixNano()/int64(time.Millisecond) {
		t.Fatalf("expected %d, got %d", mtime.UnixNano()/int64(time.Millisecond), got)
	}
}

func TestRedactBackendParams(t *testing.T) {
	input := map[string]string{
		"username":        "alice",
		"password":        "p4ss",
		"API_TOKEN":       "tok",
		"pass_phrase":     "pp",
		"Secret_Key":      "shh",
		"hostname":        "sftp.internal",
		"privateKey":      "super-secret",
		"access_key":      "ak",
		"credential_file": "pem",
		"refresh":         "rfr",
		"access-grant":    "ag",
		"access_grant":    "ag2",
		"accessGrant":     "ag3",
		"unrelated_data":  "value",
	}

	got := redactBackendParams(input)
	for _, key := range []string{
		"password",
		"API_TOKEN",
		"pass_phrase",
		"Secret_Key",
		"privateKey",
		"access_key",
		"credential_file",
		"refresh",
		"access-grant",
		"access_grant",
		"accessGrant",
	} {
		if got[key] == input[key] {
			t.Fatalf("expected %s redacted", key)
		}
	}
	if got["hostname"] == "[REDACTED]" {
		t.Fatal("expected non-credential key not to be redacted")
	}
	if got["unrelated_data"] != "value" {
		t.Fatalf("expected unrelated data untouched, got %q", got["unrelated_data"])
	}
}

func TestRedactBackendParamsDoesNotMutateInput(t *testing.T) {
	input := map[string]string{"token": "secret-token"}
	original := map[string]string{"token": "secret-token"}

	got := redactBackendParams(input)
	if got["token"] == input["token"] {
		t.Fatalf("expected token redacted")
	}
	if got["token"] != "[REDACTED]" {
		t.Fatalf("expected token to be redacted")
	}
	if input["token"] != original["token"] {
		t.Fatalf("input mutated, got %q", input["token"])
	}
	got["token"] = "changed"
	if input["token"] != original["token"] {
		t.Fatalf("redaction output should not mutate source map")
	}
}

func TestIsWriteMode(t *testing.T) {
	if !isWriteMode(pb.AccessMode_ACCESS_MODE_READ_WRITE) {
		t.Fatal("expected read-write mode to be writable")
	}
	if isWriteMode(pb.AccessMode_ACCESS_MODE_READ) {
		t.Fatal("expected read mode not to be writable")
	}
	if isWriteMode(pb.AccessMode(-1)) {
		t.Fatal("expected unknown mode not to be writable")
	}
}
