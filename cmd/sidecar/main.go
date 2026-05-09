package main

import (
	"context"
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
	_ "github.com/mickael-kerjean/filestash/server/plugin/plg_starter_http"

	"github.com/gorilla/mux"
)

func main() {
	run(mux.NewRouter())
}

func run(router *mux.Router) {
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
	Hooks.Get.Starter()(withSignal(), router)
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
