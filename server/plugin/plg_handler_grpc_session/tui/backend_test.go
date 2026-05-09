package tui

import (
	"testing"
)

func TestBackendFieldsKnown(t *testing.T) {
	tests := []struct {
		backend string
		fields  []string
	}{
		{"local", []string{"password"}},
		{"tmp", []string{"userID"}},
		{"sftp", []string{"hostname", "username", "password", "path", "port", "passphrase", "hostkey"}},
		{"ftp", []string{"hostname", "username", "password", "path", "port", "conn"}},
		{"webdav", []string{"url", "username", "password", "path"}},
		{"s3", []string{"access_key_id", "secret_access_key", "region", "endpoint", "role_arn", "session_token", "path", "encryption_key", "number_thread", "timeout"}},
		{"gdrive", []string{"token", "refresh", "expiry"}},
		{"dropbox", []string{"access_token"}},
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

	want := []string{"dropbox", "ftp", "gdrive", "local", "s3", "sftp", "tmp", "webdav"}
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

func TestBackendFieldsDefaultBackendTypesHaveGuidedFields(t *testing.T) {
	for _, backend := range DefaultBackendTypes {
		if fields := BackendFields(backend); len(fields) == 0 {
			t.Fatalf("BackendFields(%q) returned no guided fields", backend)
		}
	}
}

func TestBackendFieldsDoesNotAdvertiseUnregisteredDavAlias(t *testing.T) {
	for _, backend := range DefaultBackendTypes {
		if backend == "dav" {
			t.Fatal("DefaultBackendTypes advertised unregistered dav backend")
		}
	}
	if fields := BackendFields("dav"); len(fields) != 0 {
		t.Fatalf("BackendFields(%q)=%v", "dav", fields)
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
		{"passphrase", true},
		{"refresh", true},
		{"access_grant", true},
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
