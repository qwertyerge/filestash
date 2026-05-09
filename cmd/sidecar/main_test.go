package main

import "testing"

func TestParseSidecarCommandDefaultsToServe(t *testing.T) {
	cmd, opts, err := parseSidecarCommand(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cmd != commandServe {
		t.Fatalf("cmd=%v", cmd)
	}
	if opts.Addr != "" {
		t.Fatalf("opts=%+v", opts)
	}
}

func TestParseSidecarCommandAcceptsServe(t *testing.T) {
	cmd, _, err := parseSidecarCommand([]string{"serve"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd != commandServe {
		t.Fatalf("cmd=%v", cmd)
	}
}

func TestParseSidecarCommandAcceptsTUIFlags(t *testing.T) {
	cmd, opts, err := parseSidecarCommand([]string{
		"tui",
		"--addr", "127.0.0.1:9444",
		"--config", "/tmp/config.json",
		"--cert", "/tmp/client.crt",
		"--key", "/tmp/client.key",
		"--ca", "/tmp/ca.crt",
		"--server-name", "localhost",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cmd != commandTUI {
		t.Fatalf("cmd=%v", cmd)
	}
	if opts.Addr != "127.0.0.1:9444" ||
		opts.ConfigFile != "/tmp/config.json" ||
		opts.ClientCertFile != "/tmp/client.crt" ||
		opts.ClientKeyFile != "/tmp/client.key" ||
		opts.CAFile != "/tmp/ca.crt" ||
		opts.ServerName != "localhost" {
		t.Fatalf("opts=%+v", opts)
	}
}

func TestParseSidecarCommandRejectsUnknownCommand(t *testing.T) {
	_, _, err := parseSidecarCommand([]string{"bogus"})
	if err == nil {
		t.Fatal("expected error")
	}
}
