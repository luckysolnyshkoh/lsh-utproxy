APP     := lsh-utproxy
PKG     := ./cmd/lsh-utproxy
VERSION ?= dev
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

GOFLAGS ?=
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

# The Makefile assumes a POSIX shell (Linux, macOS, WSL, Git Bash). On plain
# Windows use the "go build" commands from README.md instead.

.PHONY: help build run test race vet fmt tidy linux-amd64 linux-arm64 linux-arm \
	linux-i386 windows-amd64 windows-arm64 darwin-amd64 darwin-arm64 release clean

help:
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-16s %s\n", $$1, $$2}'

build: ## build for the current OS and architecture
	go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(APP) $(PKG)

run: build ## build and run with the example config
	./$(APP) -c config/example.cfg

test: ## run the unit and integration tests
	go test ./... -count=1

race: ## run the tests under the race detector (needs cgo)
	go test -race ./... -count=1

vet: ## go vet
	go vet ./...

fmt: ## gofmt -w -s over the tree
	gofmt -w -s cmd internal

tidy: ## go mod tidy
	go mod tidy

linux-amd64: ## build linux/amd64
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o dist/$(APP)-linux-amd64 $(PKG)

linux-arm64: ## build linux/arm64
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o dist/$(APP)-linux-arm64 $(PKG)

linux-arm: ## build linux/arm (ARMv7)
	GOOS=linux GOARCH=arm GOARM=7 go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o dist/$(APP)-linux-armv7 $(PKG)

linux-i386: ## build linux/386
	GOOS=linux GOARCH=386 go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o dist/$(APP)-linux-i386 $(PKG)

windows-amd64: ## build windows/amd64 (runs as a console app or a service)
	GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o dist/$(APP)-windows-amd64.exe $(PKG)

windows-arm64: ## build windows/arm64
	GOOS=windows GOARCH=arm64 go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o dist/$(APP)-windows-arm64.exe $(PKG)

darwin-amd64: ## build darwin/amd64
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o dist/$(APP)-darwin-amd64 $(PKG)

darwin-arm64: ## build darwin/arm64
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o dist/$(APP)-darwin-arm64 $(PKG)

release: linux-amd64 linux-arm64 linux-arm linux-i386 windows-amd64 windows-arm64 ## all supported platforms
	@echo "artifacts in dist/"

clean:
	rm -f $(APP) $(APP).exe
	rm -rf dist
