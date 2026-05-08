package plg_handler_grpc_session

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	. "github.com/mickael-kerjean/filestash/server/common"
	"github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var (
	sidecarGracefulStopTimeout    = 5 * time.Second
	sidecarSessionShutdownTimeout = 250 * time.Millisecond
	sidecarSessionJanitorInterval = time.Minute
)

type sidecarTLSFiles struct {
	certFile     string
	keyFile      string
	clientCAFile string
}

type sidecarRuntime struct {
	server   *grpc.Server
	listener net.Listener
	sessions *sessionManager
	cancel   context.CancelFunc

	serveDone   chan struct{}
	janitorDone chan struct{}
	stopOnce    sync.Once
}

func loadMTLSConfig(files sidecarTLSFiles) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(files.certFile, files.keyFile)
	if err != nil {
		return nil, err
	}
	caBytes, err := os.ReadFile(files.clientCAFile)
	if err != nil {
		return nil, err
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caBytes) {
		return nil, ErrNotValid
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
	}, nil
}

func startSidecarServer(parent context.Context) (*sidecarRuntime, error) {
	if parent == nil {
		parent = context.Background()
	}
	tlsConfig, err := loadMTLSConfig(sidecarTLSFiles{
		certFile:     PluginTLSCertFile(),
		keyFile:      PluginTLSKeyFile(),
		clientCAFile: PluginTLSClientCAFile(),
	})
	if err != nil {
		return nil, err
	}
	policies, err := parsePolicyConfig([]byte(PluginPolicies()))
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(parent)
	listener, err := net.Listen("tcp", PluginListenAddr())
	if err != nil {
		cancel()
		return nil, err
	}

	sessions := newSessionManager(sessionManagerOptions{})
	service, err := newSidecarService(sessions, policies)
	if err != nil {
		cancel()
		_ = listener.Close()
		return nil, err
	}
	grpcServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsConfig)))
	pb.RegisterFilestashSidecarServiceServer(grpcServer, service)

	runtime := &sidecarRuntime{
		server:    grpcServer,
		listener:  listener,
		sessions:  sessions,
		cancel:    cancel,
		serveDone: make(chan struct{}),
	}
	runtime.startSessionJanitor(ctx)
	go runtime.serve()
	go func() {
		<-ctx.Done()
		runtime.stop()
	}()

	Log.Info("[sidecar_grpc] listening on %s", listener.Addr().String())
	return runtime, nil
}

func (r *sidecarRuntime) serve() {
	defer close(r.serveDone)
	if err := r.server.Serve(r.listener); err != nil &&
		!errors.Is(err, grpc.ErrServerStopped) &&
		!errors.Is(err, net.ErrClosed) {
		Log.Error("sidecar_grpc::serve err=%s", err.Error())
	}
}

func (r *sidecarRuntime) stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		r.waitForJanitor()
		sessions := r.beginSessionShutdown()
		if r.server != nil {
			r.stopServer()
		}
		if r.listener != nil {
			_ = r.listener.Close()
		}
		if r.serveDone != nil {
			<-r.serveDone
		}
		r.finishSessionShutdown(sessions)
	})
}

func (r *sidecarRuntime) startSessionJanitor(ctx context.Context) {
	if r == nil || r.sessions == nil || ctx == nil {
		return
	}
	interval := sidecarSessionJanitorInterval
	if interval <= 0 {
		interval = time.Minute
	}
	done := make(chan struct{})
	r.janitorDone = done
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.sessions.scanExpiredSessions()
			}
		}
	}()
}

func (r *sidecarRuntime) waitForJanitor() {
	if r == nil || r.janitorDone == nil {
		return
	}
	select {
	case <-r.janitorDone:
	case <-time.After(sidecarSessionShutdownTimeout):
	}
}

func (r *sidecarRuntime) stopServer() {
	stopped := make(chan struct{})
	go func() {
		r.server.GracefulStop()
		close(stopped)
	}()

	timer := time.NewTimer(sidecarGracefulStopTimeout)
	defer timer.Stop()
	select {
	case <-stopped:
	case <-timer.C:
		r.server.Stop()
		select {
		case <-stopped:
		case <-time.After(sidecarGracefulStopTimeout):
		}
	}
}

func (r *sidecarRuntime) beginSessionShutdown() []*sidecarSession {
	if r.sessions == nil {
		return nil
	}
	r.sessions.mu.Lock()
	r.sessions.shuttingDown = true
	sessions := make([]*sidecarSession, 0, len(r.sessions.sessions))
	for _, session := range r.sessions.sessions {
		sessions = append(sessions, session)
	}
	r.sessions.mu.Unlock()

	for _, session := range sessions {
		session.mu.Lock()
		if session.state != pb.SessionState_SESSION_STATE_CLOSED {
			session.state = pb.SessionState_SESSION_STATE_CLOSED
		}
		cancel := session.cancel
		session.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	return sessions
}

func (r *sidecarRuntime) finishSessionShutdown(sessions []*sidecarSession) {
	if len(sessions) == 0 {
		return
	}
	deadline := time.Now()
	if sidecarSessionShutdownTimeout > 0 {
		deadline = deadline.Add(sidecarSessionShutdownTimeout)
	}

	for _, session := range sessions {
		session.waitForOpsUntil(deadline)
		session.closeBackendOnce()
	}
}
