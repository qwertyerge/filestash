package plg_handler_grpc_session

import (
	"context"
	"sync"

	. "github.com/mickael-kerjean/filestash/server/common"
)

var activeSidecarRuntime struct {
	sync.Mutex
	runtime *sidecarRuntime
}

func init() {
	Hooks.Register.Onload(onSidecarLoad)
	Hooks.Register.OnQuit(stopActiveSidecarRuntime)
}

func onSidecarLoad() {
	stopActiveSidecarRuntime()
	if !hydrateSidecarConfig() {
		return
	}
	runtime, err := startSidecarServer(context.Background())
	if err != nil {
		Log.Error("sidecar_grpc::start err=%s", err.Error())
		return
	}
	replaceActiveSidecarRuntime(runtime)
}

func replaceActiveSidecarRuntime(next *sidecarRuntime) {
	activeSidecarRuntime.Lock()
	old := activeSidecarRuntime.runtime
	if old == next {
		activeSidecarRuntime.Unlock()
		return
	}
	activeSidecarRuntime.runtime = next
	activeSidecarRuntime.Unlock()

	if old != nil {
		old.stop()
	}
}

func stopActiveSidecarRuntime() {
	replaceActiveSidecarRuntime(nil)
}

func getActiveSidecarRuntime() *sidecarRuntime {
	activeSidecarRuntime.Lock()
	defer activeSidecarRuntime.Unlock()
	return activeSidecarRuntime.runtime
}

func hydrateSidecarConfig() bool {
	enabled := PluginEnable()
	PluginListenAddr()
	PluginTLSCertFile()
	PluginTLSKeyFile()
	PluginTLSClientCAFile()
	PluginPolicies()
	PluginMaxSessions()
	PluginDefaultLeaseSeconds()
	PluginDefaultIdleTimeoutSeconds()
	PluginDefaultMaxLifetimeSeconds()
	PluginMaxStreamBytes()
	return enabled
}
