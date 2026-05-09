package tui

import (
	"testing"
)

func TestBackendFieldsKnown(t *testing.T) {
	tests := []struct {
		backend string
		fields  []string
	}{
		{"local", []string{"location"}},
		{"tmp", nil},
		{"sftp", []string{"hostname", "port", "username", "password"}},
		{"ftp", []string{"hostname", "port", "username", "password"}},
		{"webdav", []string{"url", "username", "password"}},
		{"dav", []string{"url", "username", "password"}},
		{"s3", []string{"endpoint", "region", "bucket", "access_key_id", "secret_access_key"}},
	}

	for _, tt := range tests {
		t.Run(tt.backend, func(t *testing.T) {
			fields := BackendFields(tt.backend)
			got := make([]string, 0, len(fields))
			for _, field := range fields {
				got = append(got, field.Name)
			}
			if len(got) != len(tt.fields) {
				t.Fatalf("BackendFields(%q)=%v", tt.backend, got)
			}
			for i := range got {
				if got[i] != tt.fields[i] {
					t.Fatalf("BackendFields(%q)=%v", tt.backend, got)
				}
			}
		})
	}
}

func TestBackendFieldsDefaultBackendTypes(t *testing.T) {
	if len(DefaultBackendTypes) == 0 {
		t.Fatalf("DefaultBackendTypes must not be empty")
	}

	want := []string{"dav", "dropbox", "ftp", "gdrive", "local", "s3", "sftp", "tmp", "webdav"}
	for _, b := range want {
		found := false
		for _, candidate := range DefaultBackendTypes {
			if candidate == b {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("DefaultBackendTypes missing %q", b)
		}
	}
}

func TestSensitiveFieldsAreDetected(t *testing.T) {
	cases := []struct {
		name     string
		expected bool
	}{
		{"password", true},
		{"secret", true},
		{"token", true},
		{"api_key", true},
		{"access_key_id", true},
		{"credential", true},
		{"client_certificate", false},
		{"hostname", false},
		{"username", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if IsSensitiveField(tc.name) != tc.expected {
				t.Fatalf("IsSensitiveField(%q)=%v", tc.name, !tc.expected)
			}
		})
	}
}
