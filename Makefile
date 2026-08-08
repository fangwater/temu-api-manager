.PHONY: build test

build:
	mkdir -p bin
	go build -o bin/temu-server ./cmd/server

test:
	go test ./...
