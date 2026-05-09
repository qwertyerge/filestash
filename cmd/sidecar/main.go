package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mickael-kerjean/filestash/server"
	. "github.com/mickael-kerjean/filestash/server/common"
	"github.com/mickael-kerjean/filestash/server/ctrl"
	"github.com/mickael-kerjean/filestash/server/model"
	_ "github.com/mickael-kerjean/filestash/server/pkg"
	"github.com/mickael-kerjean/filestash/server/pkg/workflow"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_authenticate_local"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_authenticate_passthrough"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_artifactory"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_backblaze"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_dav"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_dropbox"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_ftp"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_gdrive"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_git"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_ldap"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_local"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_mysql"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_nfs"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_nop"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_perkeep"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_psql"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_s3"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_samba"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_sftp"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_storj"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_tmp"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_url"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_webdav"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session"
	sidecarclient "github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/client"
	sidecartui "github.com/mickael-kerjean/filestash/server/plugin/plg_handler_grpc_session/tui"
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_starter_http"

	"github.com/gorilla/mux"
)

type sidecarCommand int

const (
	commandServe sidecarCommand = iota
	commandTUI
)

func main() {
	cmd, opts, err := parseSidecarCommand(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	ctx := withSignal()
	switch cmd {
	case commandServe:
		run(ctx, mux.NewRouter())
	case commandTUI:
		if err := runTUI(ctx, opts); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %d\n", cmd)
		os.Exit(2)
	}
}

func parseSidecarCommand(args []string) (sidecarCommand, sidecarclient.Options, error) {
	if len(args) == 0 {
		return commandServe, sidecarclient.Options{}, nil
	}
	switch args[0] {
	case "serve":
		if len(args) > 1 {
			return commandServe, sidecarclient.Options{}, fmt.Errorf("serve accepts no arguments")
		}
		return commandServe, sidecarclient.Options{}, nil
	case "tui":
		fs := flag.NewFlagSet("tui", flag.ContinueOnError)
		fs.SetOutput(ioDiscard{})
		var opts sidecarclient.Options
		fs.StringVar(&opts.Addr, "addr", "", "sidecar gRPC address")
		fs.StringVar(&opts.ConfigFile, "config", "", "Filestash config path")
		fs.StringVar(&opts.ClientCertFile, "cert", "", "mTLS client certificate path")
		fs.StringVar(&opts.ClientKeyFile, "key", "", "mTLS client key path")
		fs.StringVar(&opts.CAFile, "ca", "", "sidecar server CA path")
		fs.StringVar(&opts.ServerName, "server-name", "", "TLS server name override")
		if err := fs.Parse(args[1:]); err != nil {
			return commandTUI, opts, err
		}
		return commandTUI, opts, nil
	default:
		return commandServe, sidecarclient.Options{}, fmt.Errorf("unknown sidecar command %q", args[0])
	}
}

func runTUI(ctx context.Context, opts sidecarclient.Options) error {
	resolved, err := sidecarclient.ResolveOptions(opts)
	if err != nil {
		return err
	}
	conn, err := sidecarclient.Dial(ctx, resolved)
	if err != nil {
		return err
	}
	defer conn.Close()

	return sidecartui.Run(ctx, sidecartui.AppOptions{
		Client:     sidecarclient.New(conn),
		Connection: resolved,
		Context:    ctx,
	})
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

func run(ctx context.Context, router *mux.Router) {
	check(InitLogger(), "Logger init failed. err=%s")
	check(InitConfig(), "Config init failed. err=%s")
	check(workflow.Init(), "Workflow initialisation failure. err=%s")
	check(model.PluginDiscovery(), "Plugin discovery failed. err=%s")
	check(ctrl.InitPluginList(nil, model.PLUGINS), "Plugin initialisation failed. err=%s")
	if Hooks.Get.Starter() == nil {
		check(ErrNotFound, "Missing starter plugin. err=%s")
	}
	for _, fn := range Hooks.Get.Onload() {
		fn()
	}
	for _, obj := range Hooks.Get.HttpEndpoint() {
		obj(router)
	}
	server.Build(router)
	server.PluginRoutes(router)
	if os.Getenv("DEBUG") == "true" {
		server.DebugRoutes(router)
	}
	server.CatchAll(router)
	Hooks.Get.Starter()(ctx, router)
	for _, fn := range Hooks.Get.OnQuit() {
		fn()
	}
}

func check(err error, msg string) {
	if err == nil {
		return
	}
	Log.Error(msg, err.Error())
	os.Exit(1)
}

func withSignal() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
		<-quit
		cancel()
	}()
	return ctx
}
