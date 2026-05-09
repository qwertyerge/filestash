package client

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveOptionsFindsPackageRootConfig(t *testing.T) {
	root := t.TempDir()
	writeTempFile(t, filepath.Join(root, "conf", "config.json"), `{
  "features": {
    "sidecar_grpc": {
      "listen_addr": "127.0.0.1:9443",
      "tls": {
        "client_ca_file": "conf/certs/client-ca.crt"
      }
    }
  }
}`)
	writeTempFile(t, filepath.Join(root, "conf", "certs", "client.crt"), "cert")
	writeTempFile(t, filepath.Join(root, "conf", "certs", "client.key"), "key")
	writeTempFile(t, filepath.Join(root, "conf", "certs", "client-ca.crt"), "ca")

	got, err := ResolveOptions(Options{WorkDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigFile != filepath.Join(root, "conf", "config.json") {
		t.Fatalf("ConfigFile=%q", got.ConfigFile)
	}
	if got.Addr != "127.0.0.1:9443" {
		t.Fatalf("Addr=%q", got.Addr)
	}
	if got.ClientCertFile != filepath.Join(root, "conf", "certs", "client.crt") {
		t.Fatalf("ClientCertFile=%q", got.ClientCertFile)
	}
	if got.ClientKeyFile != filepath.Join(root, "conf", "certs", "client.key") {
		t.Fatalf("ClientKeyFile=%q", got.ClientKeyFile)
	}
	if got.CAFile != filepath.Join(root, "conf", "certs", "client-ca.crt") {
		t.Fatalf("CAFile=%q", got.CAFile)
	}
}

func TestResolveOptionsPrefersRuntimeStateConfig(t *testing.T) {
	root := t.TempDir()
	writeTempFile(t, filepath.Join(root, "conf", "config.json"), `{
  "features": {
    "sidecar_grpc": {
      "listen_addr": "127.0.0.1:9443"
    }
  }
}`)
	writeTempFile(t, filepath.Join(root, "data", "state", "config", "config.json"), `{
  "features": {
    "sidecar_grpc": {
      "listen_addr": "127.0.0.1:9555"
    }
  }
}`)

	got, err := ResolveOptions(Options{WorkDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigFile != filepath.Join(root, "data", "state", "config", "config.json") {
		t.Fatalf("ConfigFile=%q", got.ConfigFile)
	}
	if got.Addr != "127.0.0.1:9555" {
		t.Fatalf("Addr=%q", got.Addr)
	}
}

func TestResolveOptionsFindsConfigFromBinDirectory(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, filepath.Join(root, "conf", "config.json"), `{
  "features": {
    "sidecar_grpc": {
      "listen_addr": "127.0.0.1:9444"
    }
  }
}`)
	writeTempFile(t, filepath.Join(root, "conf", "certs", "client.crt"), "cert")
	writeTempFile(t, filepath.Join(root, "conf", "certs", "client.key"), "key")
	writeTempFile(t, filepath.Join(root, "conf", "certs", "client-ca.crt"), "ca")

	got, err := ResolveOptions(Options{WorkDir: bin})
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigFile != filepath.Join(root, "conf", "config.json") {
		t.Fatalf("ConfigFile=%q", got.ConfigFile)
	}
	if got.Addr != "127.0.0.1:9444" {
		t.Fatalf("Addr=%q", got.Addr)
	}
}

func TestResolveOptionsExplicitFlagsOverrideConfig(t *testing.T) {
	root := t.TempDir()
	writeTempFile(t, filepath.Join(root, "conf", "config.json"), `{
  "features": {
    "sidecar_grpc": {
      "listen_addr": "127.0.0.1:9444",
      "tls": {
        "client_ca_file": "conf/certs/client-ca.crt"
      }
    }
  }
}`)

	got, err := ResolveOptions(Options{
		WorkDir:        root,
		Addr:           "127.0.0.1:9555",
		ClientCertFile: "/tmp/client.crt",
		ClientKeyFile:  "/tmp/client.key",
		CAFile:         "/tmp/ca.crt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != "127.0.0.1:9555" {
		t.Fatalf("Addr=%q", got.Addr)
	}
	if got.ClientCertFile != "/tmp/client.crt" {
		t.Fatalf("ClientCertFile=%q", got.ClientCertFile)
	}
	if got.ClientKeyFile != "/tmp/client.key" {
		t.Fatalf("ClientKeyFile=%q", got.ClientKeyFile)
	}
	if got.CAFile != "/tmp/ca.crt" {
		t.Fatalf("CAFile=%q", got.CAFile)
	}
}

func TestResolveOptionsExplicitConfigKeepsPackageCertDefaults(t *testing.T) {
	root := t.TempDir()
	writeTempFile(t, filepath.Join(root, "conf", "config.json"), `{
  "features": {
    "sidecar_grpc": {
      "listen_addr": "127.0.0.1:9444"
    }
  }
}`)

	got, err := ResolveOptions(Options{
		WorkDir:    root,
		ConfigFile: "conf/config.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientCertFile != filepath.Join(root, "conf", "certs", "client.crt") {
		t.Fatalf("ClientCertFile=%q", got.ClientCertFile)
	}
	if got.ClientKeyFile != filepath.Join(root, "conf", "certs", "client.key") {
		t.Fatalf("ClientKeyFile=%q", got.ClientKeyFile)
	}
	if got.CAFile != filepath.Join(root, "conf", "certs", "client-ca.crt") {
		t.Fatalf("CAFile=%q", got.CAFile)
	}
}

func TestResolveOptionsParentConfigOnlyFromBinDirectory(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "scripts")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, filepath.Join(root, "conf", "config.json"), `{
  "features": {
    "sidecar_grpc": {
      "listen_addr": "127.0.0.1:9444"
    }
  }
}`)

	got, err := ResolveOptions(Options{WorkDir: child})
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigFile != "" {
		t.Fatalf("ConfigFile=%q", got.ConfigFile)
	}
	if got.Addr != "127.0.0.1:9443" {
		t.Fatalf("Addr=%q", got.Addr)
	}
}

func TestResolveOptionsUsesFallbackAddressWithoutConfig(t *testing.T) {
	got, err := ResolveOptions(Options{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != "127.0.0.1:9443" {
		t.Fatalf("Addr=%q", got.Addr)
	}
}

func TestResolveOptionsResolvesRelativeCAFromExplicitConfig(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "configs")
	writeTempFile(t, filepath.Join(configDir, "config.json"), `{
  "features": {
    "sidecar_grpc": {
      "listen_addr": "127.0.0.1:9555",
      "tls": {
        "client_ca_file": "relative/ca/client-ca.crt"
      }
    }
  }
}`)
	writeTempFile(t, filepath.Join(configDir, "relative", "ca", "client-ca.crt"), "ca")

	got, err := ResolveOptions(Options{
		WorkDir:    root,
		ConfigFile: "configs/config.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.CAFile != filepath.Join(configDir, "relative", "ca", "client-ca.crt") {
		t.Fatalf("CAFile=%q", got.CAFile)
	}
	if got.ConfigFile != filepath.Join(configDir, "config.json") {
		t.Fatalf("ConfigFile=%q", got.ConfigFile)
	}
}

func TestResolveOptionsFindsConfigFromFilestashPath(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(t.TempDir(), "state-root")
	t.Setenv("FILESTASH_PATH", data)
	writeTempFile(t, filepath.Join(data, "state", "config", "config.json"), `{
  "features": {
    "sidecar_grpc": {
      "listen_addr": "127.0.0.1:9445",
      "tls": {
        "client_ca_file": "conf/certs/client-ca.crt"
      }
    }
  }
}`)

	got, err := ResolveOptions(Options{WorkDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigFile != filepath.Join(data, "state", "config", "config.json") {
		t.Fatalf("ConfigFile=%q", got.ConfigFile)
	}
	if got.Addr != "127.0.0.1:9445" {
		t.Fatalf("Addr=%q", got.Addr)
	}
	if got.ClientCertFile != filepath.Join(root, "conf", "certs", "client.crt") {
		t.Fatalf("ClientCertFile=%q", got.ClientCertFile)
	}
	if got.CAFile != filepath.Join(root, "conf", "certs", "client-ca.crt") {
		t.Fatalf("CAFile=%q", got.CAFile)
	}
}

func TestResolveOptionsRejectsMalformedExplicitConfig(t *testing.T) {
	root := t.TempDir()
	writeTempFile(t, filepath.Join(root, "conf", "config.json"), `{"features":`)

	_, err := ResolveOptions(Options{
		WorkDir:    root,
		ConfigFile: "conf/config.json",
	})
	if err == nil {
		t.Fatal("expected malformed explicit config to fail")
	}
}

func TestResolveOptionsRejectsMalformedDiscoveredConfig(t *testing.T) {
	root := t.TempDir()
	writeTempFile(t, filepath.Join(root, "conf", "config.json"), `{"features":`)

	_, err := ResolveOptions(Options{WorkDir: root})
	if err == nil {
		t.Fatal("expected malformed discovered config to fail")
	}
}
