# Sidecar gRPC TUI Design

## Goal

Add a `tui` subcommand to the packaged sidecar binary so an operator can open a one-time sidecar session and perform all sidecar filesystem operations interactively from a terminal.

The TUI is a client of the existing `features.sidecar_grpc` endpoint. It does not bypass mTLS, policy, session lifecycle, or backend validation. It should work naturally from the packaged working directory created by `packaging/sidecar-grpc/scripts/package.sh`.

## Entry Point

`cmd/sidecar` keeps the current behavior when run without arguments: it starts the Filestash sidecar service.

New command:

```sh
filestash-sidecar tui [flags]
```

Supported flags:

- `--addr`: sidecar gRPC address. Defaults through config discovery, then `127.0.0.1:9443`.
- `--config`: explicit Filestash config path.
- `--cert`: mTLS client certificate path.
- `--key`: mTLS client private key path.
- `--ca`: CA file used to verify the sidecar server certificate.
- `--server-name`: optional TLS server name override.

The command exits non-zero when it cannot discover enough connection settings or cannot establish the mTLS gRPC connection.

## Configuration Discovery

Discovery order:

1. Explicit CLI flags.
2. Package convention paths relative to the current working directory:
   - `./conf/config.json`
   - `./data/state/config/config.json`
   - `../conf/config.json` when launched from `bin/`
3. `FILESTASH_PATH/state/config/config.json` when `FILESTASH_PATH` is set.
4. Built-in fallback address `127.0.0.1:9443`.

Certificate defaults use package convention paths:

- Client certificate: `conf/certs/client.crt`
- Client key: `conf/certs/client.key`
- CA: `conf/certs/client-ca.crt`

Config parsing reads:

- `features.sidecar_grpc.listen_addr`
- `features.sidecar_grpc.tls.client_ca_file`

The server-side `cert_file` and `key_file` are not used by the TUI client. If paths in config are relative, they are resolved relative to the package root when discovered from package paths, or relative to the config file directory for explicit `--config`.

## TUI Framework

Use Bubble Tea from Charmbracelet, with Bubbles for list and text input primitives when useful.

Current upstream documentation uses:

```go
import tea "charm.land/bubbletea/v2"
```

The implementation should keep Bubble Tea dependencies isolated under the TUI package so the sidecar service code remains independent of terminal UI concerns.

## Architecture

Create three clear boundaries:

### `cmd/sidecar`

Owns command dispatch:

- No args or `serve`: start the sidecar service.
- `tui`: construct TUI options, discover connection settings, and run the Bubble Tea program.

### `server/plugin/plg_handler_grpc_session/client`

Owns client-side sidecar integration:

- Config discovery.
- mTLS gRPC dial.
- Thin RPC wrapper around `pb.FilestashSidecarServiceClient`.
- Helpers for streaming `ReadFile` and `WriteFile`.

This package must be testable without Bubble Tea. RPC wrappers should accept an interface compatible with the generated client so tests can use fakes.

### `server/plugin/plg_handler_grpc_session/tui`

Owns terminal state and rendering:

- Backend selection wizard.
- Backend parameter entry.
- Session open/renew/close flow.
- File browser state.
- Modal prompts for dangerous or parameterized operations.
- Dispatch of sidecar client calls as Bubble Tea commands.

The TUI package should not import Filestash server internals other than the sidecar protobuf/client package.

## Backend Selection and Parameter Entry

The TUI presents a backend type selector first. Known backends get guided parameter prompts:

- `local`: `password`
- `tmp`: `userID`
- `sftp`: `hostname`, `username`, `password`, `path`, `port`, `passphrase`, `hostkey`
- `ftp`: `hostname`, `username`, `password`, `path`, `port`, `conn`
- `webdav`: `url`, `username`, `password`, `path`
- `s3`: `access_key_id`, `secret_access_key`, `region`, `endpoint`, `role_arn`, `session_token`, `path`, `encryption_key`, `number_thread`, `timeout`
- `gdrive`: `token`, `refresh`, `expiry`
- `dropbox`: `access_token`
- other registered backends: generic key/value editor

Unknown backend types remain supported through a generic key/value editor because the sidecar service accepts any backend registered in the server binary.

Sensitive field names such as `password`, `passphrase`, `secret`, `token`, `key`, `credential`, `refresh`, and `access_grant` are masked in the UI.

Open options:

- `root_path`, default `/`
- access mode: read or read-write
- lease duration, idle timeout, max lifetime, renewable
- external ref kind/id for auditing, optional

## Main File Browser

After `Open`, the TUI enters a file browser view:

- Header: sidecar address, session id, backend type, current path, access mode.
- Main list: files and directories returned by `List`.
- Footer: shortcuts and last status/error message.

Navigation:

- `enter`: enter directory or read selected file.
- `backspace`/`h`: parent directory.
- `r`: refresh current directory.
- `/`: jump path.
- `q`: close the session and quit, with confirmation when a session is active.

File operation shortcuts:

- `i`: `Stat` selected entry.
- `v`: `ReadFile` selected file into a pager modal.
- `u`: upload/write a local file to the current path using `WriteFile`.
- `n`: `Mkdir`.
- `t`: `Touch`.
- `R`: `Rename`.
- `d`: `Remove`, with confirmation.
- `w`: toggle requested access mode only before opening a session.
- `e`: renew current session.
- `s`: get current session info.
- `S`: list sessions.
- `F`: force-close a selected session when the mTLS identity has operator policy.
- `c`: close current session.

Read-only sessions must still show mutating shortcuts, but invoking them should produce a clear `read-only session` message without sending the RPC when the TUI knows the effective mode is read-only.

## File Transfer Behavior

`ReadFile`:

- Streams from the sidecar and displays text files in a pager modal.
- For binary or very large output, prompts for a local destination path and writes the stream to disk.
- Uses sidecar stream limits as enforced by the server; the TUI surfaces `ResourceExhausted` clearly.

`WriteFile`:

- Prompts for local source path and remote destination name/path.
- Sends a header followed by chunks.
- Defaults `overwrite=false`; if the server returns conflict, the TUI asks for explicit overwrite confirmation and retries with `overwrite=true`.
- Shows bytes written on success.

## Error Handling

The TUI should preserve gRPC status meaning in user-facing messages:

- `Unauthenticated`: mTLS identity missing, invalid, or not accepted.
- `PermissionDenied`: policy or ownership violation.
- `InvalidArgument`: bad path, missing backend params, malformed request.
- `FailedPrecondition`: closed/expired/read-only session.
- `ResourceExhausted`: session or stream limit.
- `Unavailable`: sidecar or backend unavailable.

Long errors are shown in a modal details view. The footer only shows a concise summary.

## Session Lifecycle

The TUI opens one primary session at a time. It should close the active session on normal quit. If the terminal process is killed, the server lease and janitor remain the authoritative cleanup mechanism.

Operator actions for `ListSessions`, `GetSession`, and `ForceClose` are available from the TUI, but failures are normal for non-operator policies and should not break the browser.

## Packaging

The package script continues to produce:

```text
dist/filestash-sidecar-grpc/
  bin/filestash-sidecar
  bin/start.sh
  conf/config.json
  conf/certs/
  scripts/gen-etls-certs.sh
  data/
```

No additional launcher is required. Users run:

```sh
./bin/filestash-sidecar tui
```

or from the package root:

```sh
bin/filestash-sidecar tui
```

## Testing

Client package tests:

- Config discovery finds `conf/config.json` from package root.
- Config discovery finds `../conf/config.json` from `bin/`.
- Explicit flags override config and convention defaults.
- Relative certificate paths resolve consistently.
- mTLS dial config loads client cert/key/CA and sets a TLS server name when provided.
- `ReadFile` collects streamed chunks.
- `WriteFile` sends header and chunk messages in order.

TUI package tests:

- Backend schema for known backends produces expected fields.
- Sensitive fields are masked.
- Wizard state transitions from backend select to params to open.
- File browser navigation updates current path.
- Read-only mode blocks mutating shortcuts before dispatch.
- Conflict during upload moves into overwrite confirmation state.
- Close-on-quit dispatches a close command for active sessions.

Integration smoke:

- Build the package.
- Generate certs.
- Start sidecar.
- Run a non-interactive TUI smoke mode or state-level test that dials the generated endpoint and can open/list/close a `tmp` or `local` session.

## Non-Goals

- No application-layer encryption beyond the existing mandatory mTLS transport.
- No terminal text editor for remote file contents in the first version.
- No persistent profile storage beyond reading package/config convention paths.
- No attempt to discover backend login form schemas dynamically from the server.
