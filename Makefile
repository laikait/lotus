# LOTUS — Laika Optimized Transport & Unified Server
# A next-generation, cross-platform web server.
# Requires GNU make, Go (see go.mod) and, for lint/vuln, network access to install tools.

MODULE   := github.com/laikait/lotus
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
	-X $(MODULE)/pkg/version.Version=$(VERSION) \
	-X $(MODULE)/pkg/version.Commit=$(COMMIT) \
	-X $(MODULE)/pkg/version.Date=$(DATE)

STATICCHECK_VERSION := v0.8.2
GOVULNCHECK_VERSION := v1.10.0

PLATFORMS := linux/amd64 linux/arm64 freebsd/amd64 windows/amd64 darwin/arm64

.PHONY: all build test race vet fmt fmt-check lint vuln cross dist check clean

all: check build

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/lotus ./cmd/lotus

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -s -w .

fmt-check:
	@out=$$(gofmt -s -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

lint:
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

cross:
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "building $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' \
			-o bin/lotus-$$os-$$arch$$ext ./cmd/lotus || exit 1; \
	done

dist:
	scripts/release.sh $(VERSION)

check: fmt-check vet test

clean:
	rm -rf bin dist
