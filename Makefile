.PHONY: build test

build:
	go build ./...

test: bin/sqlc-gen-go-server.wasm
	go test ./...

all: bin/sqlc-gen-go-server bin/sqlc-gen-go-server.wasm

bin/sqlc-gen-go-server: bin go.mod go.sum $(wildcard **/*.go)
	cd plugin && go build -o ../bin/sqlc-gen-go-server ./main.go

bin/sqlc-gen-go-server.wasm: bin/sqlc-gen-go-server
	cd plugin && GOOS=wasip1 GOARCH=wasm go build -o ../bin/sqlc-gen-go-server.wasm main.go

bin:
	mkdir -p bin
