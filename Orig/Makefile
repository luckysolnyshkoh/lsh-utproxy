APP=lsh-utproxy
PKG=./cmd/lsh-utproxy
VERSION?=dev
COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

GOFLAGS?=
LDFLAGS=-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build run clean test vet fmt lint tidy linux-amd64 linux-arm64 linux-arm darwin-amd64 windows-amd64

build:
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(APP) $(PKG)

run: build
	./$(APP) -c config/example.cfg

clean:
	rm -f $(APP) $(APP).exe
	rm -rf dist

test:
	go test -v ./...

vet:
	go vet ./...

fmt:
	gofmt -w -s .
	goimports -w . 2>/dev/null || true

tidy:
	go mod tidy

linux-amd64:
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(APP)-linux-amd64 $(PKG)

linux-arm64:
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(APP)-linux-arm64 $(PKG)

linux-arm:
	GOOS=linux GOARCH=arm GOARM=7 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(APP)-linux-armv7 $(PKG)

darwin-amd64:
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(APP)-darwin-amd64 $(PKG)

windows-amd64:
	GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(APP)-windows-amd64.exe $(PKG)

release: linux-amd64 linux-arm64 linux-arm
	@echo built in dist/