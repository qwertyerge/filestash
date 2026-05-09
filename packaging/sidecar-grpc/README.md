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
