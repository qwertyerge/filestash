package plg_handler_grpc_session

import . "github.com/mickael-kerjean/filestash/server/common"

var PluginEnable = func() bool {
	return Config.Get("features.sidecar_grpc.enable").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Name = "enable"
		f.Type = "enable"
		f.Target = []string{"sidecar_grpc_listen_addr"}
		f.Description = "Enable/Disable the sidecar gRPC endpoint"
		f.Default = false
		return f
	}).Bool()
}

var PluginListenAddr = func() string {
	return Config.Get("features.sidecar_grpc.listen_addr").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Id = "sidecar_grpc_listen_addr"
		f.Name = "listen_addr"
		f.Type = "text"
		f.Default = "127.0.0.1:9443"
		return f
	}).String()
}

var PluginTLSCertFile = func() string {
	return Config.Get("features.sidecar_grpc.tls.cert_file").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Name = "cert_file"
		f.Type = "text"
		return f
	}).String()
}

var PluginTLSKeyFile = func() string {
	return Config.Get("features.sidecar_grpc.tls.key_file").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Name = "key_file"
		f.Type = "text"
		return f
	}).String()
}

var PluginTLSClientCAFile = func() string {
	return Config.Get("features.sidecar_grpc.tls.client_ca_file").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Name = "client_ca_file"
		f.Type = "text"
		return f
	}).String()
}

var PluginPolicies = func() string {
	return Config.Get("features.sidecar_grpc.policies").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Name = "policies"
		f.Type = "long_text"
		f.Default = `{"clients":[]}`
		return f
	}).String()
}

var PluginMaxSessions = func() int {
	return Config.Get("features.sidecar_grpc.limits.max_sessions").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Name = "max_sessions"
		f.Type = "number"
		f.Default = 100
		return f
	}).Int()
}

var PluginDefaultLeaseSeconds = func() int {
	return Config.Get("features.sidecar_grpc.limits.default_lease_seconds").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Name = "default_lease_seconds"
		f.Type = "number"
		f.Default = 0
		return f
	}).Int()
}

var PluginDefaultIdleTimeoutSeconds = func() int {
	return Config.Get("features.sidecar_grpc.limits.default_idle_timeout_seconds").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Name = "default_idle_timeout_seconds"
		f.Type = "number"
		f.Default = 0
		return f
	}).Int()
}

var PluginDefaultMaxLifetimeSeconds = func() int {
	return Config.Get("features.sidecar_grpc.limits.default_max_lifetime_seconds").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Name = "default_max_lifetime_seconds"
		f.Type = "number"
		f.Default = 0
		return f
	}).Int()
}

var PluginMaxStreamBytes = func() int64 {
	return int64(Config.Get("features.sidecar_grpc.limits.max_stream_bytes").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Name = "max_stream_bytes"
		f.Type = "number"
		f.Default = int64(0)
		return f
	}).Int())
}
