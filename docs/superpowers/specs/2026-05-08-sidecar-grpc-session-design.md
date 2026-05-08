# Sidecar gRPC One-Time Backend Session Design

## Summary

Add a Filestash plugin that exposes a TCP gRPC sidecar endpoint for one-time access to a single backend. The endpoint follows an `open -> operate -> close` lifecycle:

1. `Open` receives backend connection parameters, backend credentials, a required `root_path`, lease options, and an optional external reference.
2. The plugin creates a short-lived backend handle and returns a `session_id`.
3. File operations use that `session_id` and are chrooted under `root_path`.
4. `Close`, TTL expiry, idle expiry, max lifetime, or operator force-close releases the backend handle.

This plugin is intentionally stateless from the caller's business perspective: sessions are temporary capability leases used by an external remote shell or remote desktop orchestrator and discarded after use.

## Goals

- Use Filestash's plugin framework and backend registry.
- Allow `Open` to use arbitrary connection parameters and credentials for any registered backend.
- Expose gRPC operations for core directory and file access.
- Run as an in-process Filestash plugin with an independent TCP gRPC server.
- Require mTLS for every gRPC connection.
- Support session management and operator-side control.
- Keep external shell or desktop lifecycle independent from Filestash session lifecycle.
- Use `RenewSession` as a simple lease heartbeat driven by an external orchestrator.

## Non-Goals

- Do not add a browser UI.
- Do not add an HTTP fallback or gRPC-gateway in the first version.
- Do not cover zip, unzip, search, metadata, sharing, thumbnails, or viewer behavior.
- Do not use the existing Filestash Web session, cookies, or `/api/files` routes.
- Do not run `Open` through `model.NewBackend`, because that path enforces `Config.Conn` allowlisting.
- Do not add application-layer asymmetric encryption in the first version; mTLS is the transport security boundary.

## Current Filestash Fit

Filestash already provides the pieces this feature needs:

- `server/common/types.go` defines `IBackend` with `Ls`, `Stat`, `Cat`, `Mkdir`, `Rm`, `Mv`, `Save`, `Touch`, and `LoginForm`.
- `server/common/backend.go` provides the global `Backend` registry.
- `server/common/plugin.go` provides plugin hooks, including `Hooks.Register.HttpEndpoint`, `Onload`, and `OnQuit`.
- Existing handler plugins such as `plg_handler_mcp` demonstrate backend operation from a protocol-specific entrypoint.
- Existing backend plugins already register SFTP, FTP, S3, WebDAV, local, and other backends.

The new plugin should reuse the backend registry, config schema pattern, logging, and lifecycle hooks, while keeping the gRPC protocol separate from the current HTTP controllers.

## Architecture

Add a built-in plugin under `server/plugin/plg_handler_grpc_session`.

The plugin starts a TCP gRPC server from `Hooks.Register.Onload` when `features.sidecar_grpc.enable` is true. It shuts down from `Hooks.Register.OnQuit`, cancels active session contexts, and closes all backend handles.

Core modules:

- `grpc server`: Listens on TCP, enforces mTLS, and exposes session and filesystem RPCs.
- `session manager`: Owns `session_id -> session record` with backend handle, root path, owner identity, effective policy, lease state, external reference, and close state.
- `policy engine`: Maps mTLS client identity to policy and computes effective permissions and lease settings.
- `backend adapter`: Resolves session-relative paths under `root_path` and calls `IBackend`.
- `janitor`: Periodically closes sessions that are expired, idle, over max lifetime, or force-closed.

The external remote shell or desktop orchestrator owns the higher-level logical lifecycle. A Filestash sidecar session is only a file-access lease inside that larger lifecycle. The orchestrator may renew or close the lease, but Filestash does not interpret shell or desktop activity.

## gRPC Service

The first version exposes a service similar to:

```proto
service FilestashSidecarService {
  rpc Open(OpenRequest) returns (OpenResponse);
  rpc Close(CloseSessionRequest) returns (CloseSessionResponse);
  rpc RenewSession(RenewSessionRequest) returns (SessionLease);
  rpc GetSession(GetSessionRequest) returns (SessionInfo);
  rpc ListSessions(ListSessionsRequest) returns (ListSessionsResponse);
  rpc ForceClose(ForceCloseRequest) returns (CloseSessionResponse);

  rpc List(ListRequest) returns (ListResponse);
  rpc Stat(StatRequest) returns (StatResponse);
  rpc ReadFile(ReadFileRequest) returns (stream ReadFileChunk);
  rpc WriteFile(stream WriteFileRequest) returns (WriteFileResponse);
  rpc Mkdir(MkdirRequest) returns (MutationResponse);
  rpc Remove(RemoveRequest) returns (MutationResponse);
  rpc Rename(RenameRequest) returns (MutationResponse);
  rpc Touch(TouchRequest) returns (MutationResponse);
}
```

### Open Request

```proto
message OpenRequest {
  string backend_type = 1;
  map<string, string> backend_params = 2;
  string root_path = 3;
  AccessMode mode = 4;
  LeaseOptions lease = 5;
  ExternalRef external_ref = 6;
  map<string, string> client_metadata = 7;
}

message LeaseOptions {
  uint32 duration_seconds = 1;
  uint32 idle_timeout_seconds = 2;
  uint32 max_lifetime_seconds = 3;
  bool renewable = 4;
}

message ExternalRef {
  string kind = 1;
  string id = 2;
}
```

`backend_type` must exist in `Backend.Drivers()`. All registered backends are supported by default and cannot be disabled by backend type in policy.

`backend_params` contains connection parameters and credentials. It is never logged raw and never returned from inspection APIs.

`root_path` is required. All later file paths are resolved relative to it.

`mode` requests read-only or read-write access. The server may reduce it based on policy.

`lease` declares the client's desired lease behavior. The server computes effective values from policy and returns them.

`external_ref` is an audit and troubleshooting reference to a caller-owned object. It does not affect authorization. The name intentionally avoids implying network connection ownership or lifecycle coupling.

### Renew Session

```proto
message RenewSessionRequest {
  string session_id = 1;
}

message SessionLease {
  string session_id = 1;
  google.protobuf.Timestamp expires_at = 2;
  google.protobuf.Timestamp idle_expires_at = 3;
  google.protobuf.Timestamp max_expires_at = 4;
  bool renewable = 5;
}
```

`RenewSession` is a keepalive. The external orchestrator decides when remote shell or desktop activity counts as effective operation and calls `RenewSession(session_id)`. Filestash only validates identity, ownership or operator role, and lease rules.

Each renewal moves `expires_at` to `now + effective duration_seconds` and resets the idle deadline, but never beyond `max_expires_at`. If `renewable` is false or max lifetime has been reached, the server returns `FailedPrecondition`.

## Access Control

Every gRPC request requires mTLS. The client certificate identity is extracted from SAN first, then subject as a fallback. That identity maps to a policy.

Roles:

- `client`: Can open sessions and manage only sessions it owns.
- `operator`: Can list, inspect, renew, close, or force-close all sessions.

Policy controls:

- Host/IP/CIDR targets allowed for networked backends.
- Access modes allowed: read-only or read-write.
- Default and maximum lease settings.
- Per-identity and global session concurrency.
- Streaming and file-size limits.
- Operator role.

Policy does not include backend type allowlisting. Any registered backend may be opened. For backends with a network target, the policy engine extracts host or endpoint fields and applies CIDR checks. For backends without a network target, such as local or temporary backends, policy applies identity, access mode, lease, and resource limits.

## Security Model

The security boundary is TCP gRPC with mandatory mTLS. The service treats any holder of an allowed client certificate as able to request backend access within that identity's policy.

Application-layer asymmetric encryption is not included in the first version. It should be added only if TLS is terminated outside the trusted boundary, requests can be persisted by intermediaries, or end-to-end secrecy across an untrusted proxy is required.

Credentials handling:

- `backend_params` may contain credentials.
- Credentials are kept in memory only as needed for backend initialization and backend handle state.
- Logs, metrics, `GetSession`, and `ListSessions` must redact credential fields.
- Session inspection may expose non-sensitive backend metadata such as backend type, redacted target, root path, owner identity, external ref, access mode, and lease timestamps.

Path safety:

- `Open.root_path` is required.
- Operation paths are relative to `root_path`.
- For `List` and `Stat`, an empty path or `.` means the session root.
- For file writes and directory mutation, the target path must identify a child under the session root.
- The resolver rejects malformed paths, absolute-path escape attempts, and `..` traversal outside the root.
- `Rename` validates both source and destination.
- `Remove` only affects paths inside the root and must not remove the session root itself.
- Read-only sessions reject writes with `FailedPrecondition`.

Resource safety:

- Sessions have TTL, idle timeout, max lifetime, and close state.
- `Close` is idempotent.
- Janitor cleanup releases expired sessions.
- Streaming RPCs enforce configured byte limits and return `ResourceExhausted` on overflow.
- Per-identity and global session limits prevent unbounded backend handles.

## Data Flow

### Open

1. gRPC performs TLS handshake and verifies the client certificate.
2. The server extracts client identity.
3. The policy engine computes effective permissions and limits.
4. The server validates backend type, backend target, root path, access mode, lease options, and session concurrency.
5. The server creates an `App{Context: sessionContext}`.
6. The server calls `Backend.Get(backend_type).Init(backend_params, app)` directly.
7. The backend verifies `root_path` through `Stat(root_path)` or `Ls(root_path)`.
8. The session manager stores a new session record.
9. The server returns `session_id`, effective access mode, and effective lease.

### Operation

1. Lookup session.
2. Reject closed, expired, idle-expired, or max-lifetime-expired sessions.
3. Validate caller is the owner or an operator.
4. Validate write access for mutating operations.
5. Resolve path under `root_path`.
6. Call the relevant `IBackend` method.
7. Update `last_used_at`.
8. Return file metadata, mutation result, or stream chunks.

### Renew

1. Lookup session.
2. Validate caller is the owner or an operator.
3. Reject closed or non-renewable sessions.
4. Refresh `expires_at` and idle deadline within `max_expires_at`.
5. Return the updated lease.

### Close and Force Close

`Close` is available to owner and operator. `ForceClose` is operator-only.

Closing cancels the session context, marks the session closed, removes it from active listings, and calls `Close() error` on the backend handle if implemented. Backend close errors are logged, but close remains idempotent from the client's perspective.

## Error Mapping

Use standard gRPC status codes:

- `Unauthenticated`: Missing, invalid, or unmapped mTLS identity.
- `PermissionDenied`: Policy violation, ownership violation, or operator-only action by a client.
- `InvalidArgument`: Invalid request, malformed path, missing required root path, or unsupported access mode.
- `NotFound`: Session or path not found.
- `FailedPrecondition`: Closed session, expired session, read-only write attempt, non-renewable lease renewal, or max lifetime reached.
- `ResourceExhausted`: Concurrency, file size, stream size, or rate limit exceeded.
- `Unavailable`: Backend connection or backend service unavailable.
- `Internal`: Unexpected plugin or backend adapter error after redaction.

## Configuration

Use `features.sidecar_grpc.*`:

- `features.sidecar_grpc.enable`: Enable the plugin. Default `false`.
- `features.sidecar_grpc.listen_addr`: TCP bind address such as `127.0.0.1:9443` or `0.0.0.0:9443`.
- `features.sidecar_grpc.tls.cert_file`: Server certificate path.
- `features.sidecar_grpc.tls.key_file`: Server private key path.
- `features.sidecar_grpc.tls.client_ca_file`: CA file for client certificate verification.
- `features.sidecar_grpc.policies`: JSON policy configuration.
- `features.sidecar_grpc.limits.default_lease_seconds`: Default lease duration.
- `features.sidecar_grpc.limits.default_idle_timeout_seconds`: Default idle timeout.
- `features.sidecar_grpc.limits.default_max_lifetime_seconds`: Default max session lifetime.
- `features.sidecar_grpc.limits.max_sessions`: Global maximum active sessions.
- `features.sidecar_grpc.limits.max_stream_bytes`: Default stream byte limit.

Example policy:

```json
{
  "clients": [
    {
      "identity": "spiffe://workspace/orchestrator",
      "role": "operator",
      "host_cidrs": ["10.0.0.0/8", "192.168.0.0/16"],
      "access_modes": ["read", "write"],
      "max_sessions": 20,
      "lease": {
        "duration_seconds": 900,
        "idle_timeout_seconds": 120,
        "max_lifetime_seconds": 3600,
        "renewable": true
      },
      "limits": {
        "max_stream_bytes": 1073741824
      }
    }
  ]
}
```

The implementation should keep config reading separate from policy evaluation:

- `config.go`: Schema defaults and raw config retrieval.
- `policy.go`: Parse policies, match identity, validate targets, and compute effective lease and access mode.
- `session.go`: Session registry and lifecycle.
- `server.go`: gRPC server startup and shutdown.
- `handler.go`: RPC implementations.
- `path.go`: Chroot path resolver.

## Testing

### Unit Tests

- Path resolver: relative paths, empty root path for read operations, trailing slash, `..`, absolute paths, root boundary, root removal rejection, rename source and destination.
- Lease manager: TTL, idle timeout, max lifetime, renewable false, renewal near max lifetime, idempotent close.
- Policy engine: identity match, CIDR checks, access mode reduction, session concurrency, operator permissions.
- Redaction: `GetSession`, `ListSessions`, logs, and errors do not leak credential values.

### gRPC Handler Tests

- Register a fake backend in `Backend`.
- Use generated test client and test mTLS certificates.
- Verify `Open -> List -> ReadFile -> WriteFile -> RenewSession -> Close`.
- Verify regular clients cannot access sessions owned by another identity.
- Verify operator can list and force-close all sessions.
- Verify read-only sessions reject mutating RPCs.

### Integration Smoke Tests

- Enable plugin config and start a TCP mTLS server.
- Use `local` or `tmp` backend for `Open -> List -> WriteFile -> ReadFile -> Rename -> Remove -> Close`.
- Verify janitor removes expired sessions.
- Verify backend `Close() error` is called when implemented.

## Implementation Notes

- The plugin should be added to `server/plugin/index.go` with a blank import once implemented.
- gRPC generated files should live inside the plugin or under a clearly named internal package.
- The first implementation should avoid touching the existing Web UI and HTTP file controllers.
- The session manager should use per-session contexts so backend plugins that watch `app.Context.Done()` can release resources.
- Logging should include session ID, owner identity, external ref, backend type, redacted target, and error code where possible.
- Session IDs should be random, unguessable values generated with crypto-grade randomness.
