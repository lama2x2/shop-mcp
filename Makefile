VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BIN     := bin/shop-mcp
DB      ?= shop.db

.PHONY: build test lint run golden clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/shop-mcp

test:
	go test -race ./...

lint:
	golangci-lint run ./...

run: build
	./$(BIN) --db $(DB)

golden:
	sqlite3 -readonly $(DB) < testdata/golden.sql

clean:
	rm -rf bin
