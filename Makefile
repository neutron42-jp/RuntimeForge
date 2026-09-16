UI_DIR := web/ui
BIN := bin/runtimeforge
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
VERSION ?= 0.1.0
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X runtimeforge/internal/version.Version=$(VERSION) \
           -X runtimeforge/internal/version.Commit=$(COMMIT) \
           -X runtimeforge/internal/version.Date=$(DATE)

.PHONY: all build ui install uninstall test vet fmt clean run

all: build

## Build the embedded web UI into web/dist
ui:
	cd $(UI_DIR) && npm install && npm run build

## Build the runtimeforge binary (embedding the current web/dist)
build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/runtimeforge

## Install to $(BINDIR) and restart the systemd user service if enabled
install: build
	install -d $(BINDIR)
	install -m 0755 $(BIN) $(BINDIR)/runtimeforge
	@echo "installed $(BINDIR)/runtimeforge ($(VERSION) $(COMMIT))"
	@if systemctl --user is-enabled runtimeforge >/dev/null 2>&1; then \
		systemctl --user restart runtimeforge && echo "restarted runtimeforge.service"; \
	fi

uninstall:
	rm -f $(BINDIR)/runtimeforge

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

run: build
	$(BIN) serve

clean:
	rm -rf $(BIN)
