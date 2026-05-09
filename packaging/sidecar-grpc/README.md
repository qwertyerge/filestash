# Filestash Sidecar gRPC Package

This package layout is generated into `dist/filestash-sidecar-grpc`.

```text
filestash-sidecar-grpc/
  bin/
    filestash-sidecar
    start.sh
  conf/
    config.json
    certs/
  scripts/
    gen-etls-certs.sh
  data/
```

`gen-etls-certs.sh` generates the transport certificates used by the current implementation's mandatory mTLS boundary.

## Build

```sh
./packaging/sidecar-grpc/scripts/package.sh
```

## First Run

```sh
cd dist/filestash-sidecar-grpc
./scripts/gen-etls-certs.sh
./bin/start.sh
```

The default client identity is `spiffe://filestash-sidecar/orchestrator`; it must match the `features.sidecar_grpc.policies` identity in `conf/config.json`.

## TUI Client

Start the sidecar service, then run the TUI from another terminal in the package directory:

```sh
./bin/filestash-sidecar tui
```

The TUI discovers `conf/config.json`, `data/state/config/config.json`, or `../conf/config.json` when launched from `bin/`. It also reads `FILESTASH_PATH/state/config/config.json` when `FILESTASH_PATH` is set. By convention it uses:

```text
conf/certs/client.crt
conf/certs/client.key
conf/certs/client-ca.crt
```

Supported overrides:

```sh
./bin/filestash-sidecar tui \
  --addr 127.0.0.1:9443 \
  --config conf/config.json \
  --cert conf/certs/client.crt \
  --key conf/certs/client.key \
  --ca conf/certs/client-ca.crt \
  --server-name localhost
```
