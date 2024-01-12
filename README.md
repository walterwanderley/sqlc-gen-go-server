# sqlc-gen-go-server

[Sqlc plugin](https://sqlc.dev) to generate [gRPC](https://grpc.io/) or [Connect](https://connectrpc.com/) server from SQL.

## Building from source

Assuming you have the Go toolchain set up, from the project root you can simply `make all`.

```sh
make all
```

This will produce a standalone binary and a WASM blob in the `bin` directory.
They don't depend on each other, they're just two different plugin styles. You can
use either with sqlc, but we recommend WASM and all of the configuration examples
here assume you're using a WASM plugin.

To use a local WASM build with sqlc, just update your configuration with a `file://`
URL pointing at the WASM blob in your `bin` directory:

```yaml
plugins:
- name: go-server
  wasm:
    url: file:///path/to/bin/sqlc-gen-go-server.wasm
    sha256: ""
```

As-of sqlc v1.24.0 the `sha256` is optional, but without it sqlc won't cache your
module internally which will impact performance.

## Plugin options

You can use all of the [Golang plugin's options](https://docs.sqlc.dev/en/latest/reference/config.html#go) as well as the options described below.

```yaml
version: 2
plugins:
- name: go-server
  wasm:
    url: file:///path/to/bin/sqlc-gen-go-server.wasm
    sha256: ""
sql:
- schema: "query.sql"
  queries: "query.sql"
  engine: "postgresql"
  codegen:
  - plugin: go-server
    out: "db"
    options:
      package: "db"
      emit_json_tags: true
      server_type: "grpc" # The server type: grpc or connect.
      module: "my-module" # The module name for the generated go.mod.
      tracing: false # If true, enable distributed tracing with open telemetry.
      litefs: false # If true, enable support for distributed SQLite powered by embedded LiteFS.
      litestream: false # If true, enable support for continuous backup sqlite to S3 powered by embeded Litestream
      migration_path: "" # If you want to execute database migrations on startup
      skip_go_mod: false # If true, skip the generation of the go.mod
```

## Post-process

After execute `sqlc generate` you need to compile protocol buffer and fix go.mod.

1. Install the required tools:

```sh
go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@latest
go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2@latest
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
go install github.com/bufbuild/buf/cmd/buf@latest
```

2. Compile the generated protocol buffer:

```sh
cd proto && buf mod update
cd ..
buf generate
```

3. Fix go.mod:

```sh
go mod tidy
```