# vaultui — plain `go build` commands. Each produces one standalone binary.
#
#   make build      → ./vaultui            (this machine)
#   make all        → dist/vaultui-<os>-<arch> for macOS + Linux
#   make install    → /usr/local/bin/vaultui

VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

# CGO_ENABLED=0 makes the binary fully static (runs with nothing installed).
export CGO_ENABLED := 0

.PHONY: build all install test lint integration clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o vaultui ./cmd/vaultui

all:
	GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/vaultui-darwin-arm64 ./cmd/vaultui
	GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/vaultui-darwin-amd64 ./cmd/vaultui
	GOOS=linux  GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/vaultui-linux-amd64  ./cmd/vaultui
	GOOS=linux  GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/vaultui-linux-arm64  ./cmd/vaultui

install: build
	sudo install -m 0755 vaultui /usr/local/bin/vaultui

test:
	go test ./...

# Needs a running test Vault (VAULT_ADDR + VAULT_TOKEN).
integration:
	go test -tags integration ./internal/vault/

lint:
	go vet ./...
	go vet -tags integration ./...
	test -z "$$(gofmt -l .)"

clean:
	rm -rf vaultui dist
