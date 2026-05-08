package plg_handler_grpc_session

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

func TestLoadMTLSConfigRejectsMissingClientCA(t *testing.T) {
	files := writeMTLSTestFiles(t)

	_, err := loadMTLSConfig(sidecarTLSFiles{
		certFile:     files.serverCert,
		keyFile:      files.serverKey,
		clientCAFile: filepath.Join(t.TempDir(), "missing-ca.pem"),
	})
	if err == nil {
		t.Fatal("expected missing client CA file to fail")
	}
}

func TestLoadMTLSConfigRejectsMissingServerKeyPair(t *testing.T) {
	files := writeMTLSTestFiles(t)
	dir := t.TempDir()

	_, err := loadMTLSConfig(sidecarTLSFiles{
		certFile:     filepath.Join(dir, "missing-server.pem"),
		keyFile:      filepath.Join(dir, "missing-server-key.pem"),
		clientCAFile: files.caCert,
	})
	if err == nil {
		t.Fatal("expected missing server certificate and key to fail")
	}
}

func TestLoadMTLSConfigRejectsInvalidClientCA(t *testing.T) {
	files := writeMTLSTestFiles(t)
	invalidCA := filepath.Join(t.TempDir(), "invalid-ca.pem")
	if err := os.WriteFile(invalidCA, []byte("not a pem encoded ca"), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := loadMTLSConfig(sidecarTLSFiles{
		certFile:     files.serverCert,
		keyFile:      files.serverKey,
		clientCAFile: invalidCA,
	})
	if err == nil {
		t.Fatal("expected invalid client CA to fail")
	}
}

func TestLoadMTLSConfigValidShape(t *testing.T) {
	files := writeMTLSTestFiles(t)

	cfg, err := loadMTLSConfig(sidecarTLSFiles{
		certFile:     files.serverCert,
		keyFile:      files.serverKey,
		clientCAFile: files.caCert,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		t.Fatalf("MinVersion=%x", cfg.MinVersion)
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("ClientAuth=%v", cfg.ClientAuth)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("Certificates=%d", len(cfg.Certificates))
	}
	if cfg.ClientCAs == nil || len(cfg.ClientCAs.Subjects()) == 0 {
		t.Fatal("expected client CA pool to be populated")
	}
}

func TestLoadPolicyRejectsInvalidConfig(t *testing.T) {
	files := writeMTLSTestFiles(t)
	overrideSidecarConfig(t, files, `{"clients":[{"identity":""}]}`)

	runtime, err := startSidecarServer(context.Background())
	if err == nil {
		runtime.stop()
		t.Fatal("expected invalid policy config to fail")
	}
}

func TestSidecarServerEnforcesMTLS(t *testing.T) {
	files := writeMTLSTestFiles(t)
	overrideSidecarConfig(t, files, `{"clients":[{"identity":"client-a","access_modes":["read"]}]}`)

	runtime, err := startSidecarServer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.stop)

	conn := dialSidecar(t, runtime.listener.Addr().String(), clientTLSConfig(t, files, true))
	defer conn.Close()
	client := pb.NewFilestashSidecarServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.ListSessions(ctx, &pb.ListSessionsRequest{}); err != nil {
		t.Fatal(err)
	}

	badConn, err := grpc.DialContext(
		context.Background(),
		runtime.listener.Addr().String(),
		grpc.WithTransportCredentials(credentials.NewTLS(clientTLSConfig(t, files, false))),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer badConn.Close()
	badClient := pb.NewFilestashSidecarServiceClient(badConn)
	badCtx, badCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer badCancel()
	if _, err := badClient.ListSessions(badCtx, &pb.ListSessionsRequest{}); err == nil {
		t.Fatal("expected client without certificate to fail")
	}
}

func TestSidecarServerStopClosesActiveSessions(t *testing.T) {
	files := writeMTLSTestFiles(t)
	overrideSidecarConfig(t, files, `{"clients":[{"identity":"client-a","access_modes":["read"]}]}`)

	runtime, err := startSidecarServer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	backend := &closeTrackingBackend{}
	if _, err := runtime.sessions.open(context.Background(), openSessionInput{
		ownerIdentity: "client-a",
		backendType:   "local",
		backend:       backend,
		rootPath:      "/work",
		mode:          pb.AccessMode_ACCESS_MODE_READ,
		lease: effectiveLease{
			duration:    time.Minute,
			idleTimeout: time.Minute,
			maxLifetime: time.Hour,
			renewable:   true,
		},
	}); err != nil {
		runtime.stop()
		t.Fatal(err)
	}

	runtime.stop()
	if backend.closed != 1 {
		t.Fatalf("closed=%d", backend.closed)
	}
}

func TestSidecarRuntimeStopClosesBackendWithoutWaitingForStuckOperation(t *testing.T) {
	files := writeMTLSTestFiles(t)
	overrideSidecarConfig(t, files, `{"clients":[{"identity":"client-a","access_modes":["read"]}]}`)
	oldTimeout := sidecarSessionShutdownTimeout
	sidecarSessionShutdownTimeout = 10 * time.Millisecond
	t.Cleanup(func() { sidecarSessionShutdownTimeout = oldTimeout })

	runtime, err := startSidecarServer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	backend := &closeTrackingBackend{}
	session, err := runtime.sessions.open(context.Background(), openSessionInput{
		ownerIdentity: "client-a",
		backendType:   "local",
		backend:       backend,
		rootPath:      "/work",
		mode:          pb.AccessMode_ACCESS_MODE_READ,
		lease: effectiveLease{
			duration:    time.Minute,
			idleTimeout: time.Minute,
			maxLifetime: time.Hour,
			renewable:   true,
		},
	})
	if err != nil {
		runtime.stop()
		t.Fatal(err)
	}
	release := session.beginOperation()
	defer release()

	stopped := make(chan struct{})
	go func() {
		runtime.stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("runtime stop waited for stuck session operation")
	}
	if backend.closed != 1 {
		t.Fatalf("closed=%d", backend.closed)
	}
	if session.ctx.Err() == nil {
		t.Fatal("expected runtime stop to cancel session context")
	}
}

func TestSidecarRuntimeStopCancelsSessionContextBeforeGracefulStop(t *testing.T) {
	files := writeMTLSTestFiles(t)
	overrideSidecarConfig(t, files, `{"clients":[{"identity":"client-a","access_modes":["read"]}]}`)
	oldGracefulTimeout := sidecarGracefulStopTimeout
	sidecarGracefulStopTimeout = 500 * time.Millisecond
	t.Cleanup(func() { sidecarGracefulStopTimeout = oldGracefulTimeout })

	runtime, err := startSidecarServer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	backend := &contextBlockedListBackend{
		ctx:     sessionCtx,
		started: make(chan struct{}),
	}
	session, err := runtime.sessions.open(context.Background(), openSessionInput{
		ownerIdentity: "client-a",
		backendType:   "local",
		backend:       backend,
		rootPath:      "/work",
		mode:          pb.AccessMode_ACCESS_MODE_READ,
		lease: effectiveLease{
			duration:    time.Minute,
			idleTimeout: time.Minute,
			maxLifetime: time.Hour,
			renewable:   true,
		},
		ctx:    sessionCtx,
		cancel: sessionCancel,
	})
	if err != nil {
		runtime.stop()
		t.Fatal(err)
	}

	conn := dialSidecar(t, runtime.listener.Addr().String(), clientTLSConfig(t, files, true))
	defer conn.Close()
	client := pb.NewFilestashSidecarServiceClient(conn)
	listDone := make(chan error, 1)
	go func() {
		_, err := client.List(context.Background(), &pb.ListRequest{SessionId: session.id, Path: "."})
		listDone <- err
	}()
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		runtime.stop()
		t.Fatal("timed out waiting for blocked list operation")
	}

	stopped := make(chan struct{})
	go func() {
		runtime.stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(150 * time.Millisecond):
		t.Fatal("runtime stop did not cancel session context before graceful stop timeout")
	}
	select {
	case err := <-listDone:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("list operation did not finish after runtime stop")
	}
	if backend.closed != 1 {
		t.Fatalf("closed=%d", backend.closed)
	}
}

func TestSidecarRuntimeStopUsesSingleShutdownDeadlineForStuckSessions(t *testing.T) {
	oldTimeout := sidecarSessionShutdownTimeout
	sidecarSessionShutdownTimeout = 25 * time.Millisecond
	t.Cleanup(func() { sidecarSessionShutdownTimeout = oldTimeout })

	ctx, cancel := context.WithCancel(context.Background())
	runtime := &sidecarRuntime{
		sessions: newSessionManager(sessionManagerOptions{}),
		cancel:   cancel,
	}
	backends := make([]*closeTrackingBackend, 4)
	for i := range backends {
		backends[i] = &closeTrackingBackend{}
		session, err := runtime.sessions.open(ctx, openSessionInput{
			ownerIdentity: "client-a",
			backendType:   "local",
			backend:       backends[i],
			rootPath:      "/work",
			mode:          pb.AccessMode_ACCESS_MODE_READ,
			lease: effectiveLease{
				duration:    time.Minute,
				idleTimeout: time.Minute,
				maxLifetime: time.Hour,
				renewable:   true,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		release := session.beginOperation()
		defer release()
	}

	start := time.Now()
	runtime.stop()
	elapsed := time.Since(start)
	if elapsed > 75*time.Millisecond {
		t.Fatalf("runtime stop used per-session cleanup timeout; elapsed=%s", elapsed)
	}
	for i, backend := range backends {
		if backend.closed != 1 {
			t.Fatalf("backend %d closed=%d", i, backend.closed)
		}
	}
}

func TestSidecarRuntimeStopRejectsOpenCommitAfterShutdownStarts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &sidecarRuntime{
		sessions: newSessionManager(sessionManagerOptions{
			id: func() (string, error) { return "late-open", nil },
		}),
		cancel: cancel,
	}
	initStarted := make(chan struct{}, 1)
	releaseInit := make(chan struct{})
	driver := registerOpenTestBackend(t, &openTestDriver{
		initStarted: initStarted,
		releaseInit: releaseInit,
	})
	svc, err := newSidecarService(runtime.sessions, testOpenPolicy("client-a"))
	if err != nil {
		t.Fatal(err)
	}

	openDone := make(chan error, 1)
	go func() {
		_, err := svc.Open(identityContext("client-a"), &pb.OpenRequest{
			BackendType: driver.name,
			RootPath:    "/work",
			Mode:        pb.AccessMode_ACCESS_MODE_READ,
		})
		openDone <- err
	}()
	waitOpenTestInitStarted(t, initStarted)

	runtime.stop()
	close(releaseInit)

	select {
	case err := <-openDone:
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("code=%s err=%v", status.Code(err), err)
		}
	case <-time.After(time.Second):
		t.Fatal("open did not finish after shutdown")
	}
	if len(runtime.sessions.sessions) != 0 {
		t.Fatalf("sessions=%+v", runtime.sessions.sessions)
	}
	if driver.handle.closedCount() != 1 {
		t.Fatalf("closed=%d", driver.handle.closedCount())
	}
	if ctx.Err() == nil {
		t.Fatal("expected runtime context to be canceled")
	}
}

func TestSidecarOnloadDisableStopsExistingRuntime(t *testing.T) {
	stopActiveSidecarRuntime()
	t.Cleanup(stopActiveSidecarRuntime)
	files := writeMTLSTestFiles(t)
	enabled := true
	listenAddr := "127.0.0.1:0"
	policies := `{"clients":[{"identity":"client-a","access_modes":["read"]}]}`
	overrideSidecarConfigValues(t, files, &enabled, &listenAddr, &policies)

	onSidecarLoad()
	oldRuntime := getActiveSidecarRuntime()
	if oldRuntime == nil {
		t.Fatal("expected runtime to start")
	}
	oldAddr := oldRuntime.listener.Addr().String()

	enabled = false
	onSidecarLoad()
	if runtime := getActiveSidecarRuntime(); runtime != nil {
		t.Fatal("expected disabled onload to leave no active runtime")
	}
	listener, err := net.Listen("tcp", oldAddr)
	if err != nil {
		t.Fatalf("expected old listener address to be released: %v", err)
	}
	_ = listener.Close()
}

func TestSidecarOnloadStopsOldRuntimeBeforeStartingReplacement(t *testing.T) {
	stopActiveSidecarRuntime()
	t.Cleanup(stopActiveSidecarRuntime)
	files := writeMTLSTestFiles(t)
	enabled := true
	listenAddr := "127.0.0.1:0"
	policies := `{"clients":[{"identity":"client-a","access_modes":["read"]}]}`
	overrideSidecarConfigValues(t, files, &enabled, &listenAddr, &policies)

	onSidecarLoad()
	oldRuntime := getActiveSidecarRuntime()
	if oldRuntime == nil {
		t.Fatal("expected runtime to start")
	}
	listenAddr = oldRuntime.listener.Addr().String()

	onSidecarLoad()
	newRuntime := getActiveSidecarRuntime()
	if newRuntime == nil {
		t.Fatal("expected replacement runtime to start")
	}
	if newRuntime == oldRuntime {
		t.Fatal("expected replacement runtime, got original runtime")
	}
	if newRuntime.listener.Addr().String() != listenAddr {
		t.Fatalf("addr=%s want %s", newRuntime.listener.Addr().String(), listenAddr)
	}
}

func TestSidecarOnloadStartFailureLeavesNoActiveRuntime(t *testing.T) {
	stopActiveSidecarRuntime()
	t.Cleanup(stopActiveSidecarRuntime)
	files := writeMTLSTestFiles(t)
	enabled := true
	listenAddr := "127.0.0.1:0"
	policies := `{"clients":[{"identity":"client-a","access_modes":["read"]}]}`
	overrideSidecarConfigValues(t, files, &enabled, &listenAddr, &policies)

	onSidecarLoad()
	oldRuntime := getActiveSidecarRuntime()
	if oldRuntime == nil {
		t.Fatal("expected runtime to start")
	}
	oldAddr := oldRuntime.listener.Addr().String()
	listenAddr = oldAddr
	policies = `{"clients":[{"identity":""}]}`

	onSidecarLoad()
	if runtime := getActiveSidecarRuntime(); runtime != nil {
		t.Fatal("expected failed onload start to leave no active runtime")
	}
	listener, err := net.Listen("tcp", oldAddr)
	if err != nil {
		t.Fatalf("expected old listener address to be released: %v", err)
	}
	_ = listener.Close()
}

type mtlsTestFiles struct {
	caCert     string
	serverCert string
	serverKey  string
	clientCert string
	clientKey  string
}

func writeMTLSTestFiles(t *testing.T) mtlsTestFiles {
	t.Helper()

	dir := t.TempDir()
	caKey := newRSAKey(t)
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "filestash-test-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	serverKey := newRSAKey(t)
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	clientKey := newRSAKey(t)
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "client-a"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{"client-a"},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caTemplate, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	files := mtlsTestFiles{
		caCert:     filepath.Join(dir, "ca.pem"),
		serverCert: filepath.Join(dir, "server.pem"),
		serverKey:  filepath.Join(dir, "server-key.pem"),
		clientCert: filepath.Join(dir, "client.pem"),
		clientKey:  filepath.Join(dir, "client-key.pem"),
	}
	writePEM(t, files.caCert, "CERTIFICATE", caDER)
	writePEM(t, files.serverCert, "CERTIFICATE", serverDER)
	writePEM(t, files.serverKey, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(serverKey))
	writePEM(t, files.clientCert, "CERTIFICATE", clientDER)
	writePEM(t, files.clientKey, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(clientKey))
	return files
}

func newRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := pem.Encode(file, &pem.Block{Type: typ, Bytes: der}); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func overrideSidecarConfig(t *testing.T, files mtlsTestFiles, policies string) {
	t.Helper()
	enabled := true
	listenAddr := "127.0.0.1:0"
	overrideSidecarConfigValues(t, files, &enabled, &listenAddr, &policies)
}

func overrideSidecarConfigValues(t *testing.T, files mtlsTestFiles, enabled *bool, listenAddr, policies *string) {
	t.Helper()
	oldEnable := PluginEnable
	oldCertFile := PluginTLSCertFile
	oldKeyFile := PluginTLSKeyFile
	oldClientCAFile := PluginTLSClientCAFile
	oldListenAddr := PluginListenAddr
	oldPolicies := PluginPolicies

	PluginEnable = func() bool { return *enabled }
	PluginTLSCertFile = func() string { return files.serverCert }
	PluginTLSKeyFile = func() string { return files.serverKey }
	PluginTLSClientCAFile = func() string { return files.caCert }
	PluginListenAddr = func() string { return *listenAddr }
	PluginPolicies = func() string { return *policies }

	t.Cleanup(func() {
		PluginEnable = oldEnable
		PluginTLSCertFile = oldCertFile
		PluginTLSKeyFile = oldKeyFile
		PluginTLSClientCAFile = oldClientCAFile
		PluginListenAddr = oldListenAddr
		PluginPolicies = oldPolicies
	})
}

func clientTLSConfig(t *testing.T, files mtlsTestFiles, includeClientCert bool) *tls.Config {
	t.Helper()
	caPEM, err := os.ReadFile(files.caCert)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("test CA did not parse")
	}
	cfg := &tls.Config{
		RootCAs:    pool,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS12,
	}
	if includeClientCert {
		cert, err := tls.LoadX509KeyPair(files.clientCert, files.clientKey)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg
}

func dialSidecar(t *testing.T, addr string, cfg *tls.Config) *grpc.ClientConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(
		ctx,
		addr,
		grpc.WithTransportCredentials(credentials.NewTLS(cfg)),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

type contextBlockedListBackend struct {
	closeTrackingBackend
	ctx     context.Context
	started chan struct{}
	closed  int
}

func (b *contextBlockedListBackend) Ls(string) ([]os.FileInfo, error) {
	close(b.started)
	<-b.ctx.Done()
	return nil, nil
}

func (b *contextBlockedListBackend) Close() error {
	b.closed++
	return nil
}
