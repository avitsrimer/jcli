BINARY      ?= jcli
PKG         ?= ./cmd/jcli
INSTALL_DIR ?= $(HOME)/bin

GO ?= GOTOOLCHAIN=local go

# absolute path of the built binary, used to target only the agent spawned from
# this binary (an installed copy at a different path is left alone).
BIN_ABS := $(abspath $(BINARY))

.PHONY: build test lint fmt cross-build install stop-agent

# stop a credential agent spawned from this binary. the agent is a long-lived
# process holding the old binary's code and the token in memory, so a rebuild
# must replace it — otherwise the next command reconnects to the stale agent. a
# fresh one spawns on demand at the next command. no-op (never fails the build)
# when none is running.
stop-agent:
	@pkill -f '$(BIN_ABS) __agent' 2>/dev/null && echo "stopped stale agent ($(BINARY) __agent)" || true

build: stop-agent
	$(GO) build -o $(BINARY) $(PKG)

test:
	$(GO) test -race ./...

lint:
	GOTOOLCHAIN=local golangci-lint run

fmt:
	gofmt -s -w .
	goimports -w .

# prove the non-darwin keychain stub keeps the repo cross-buildable.
cross-build:
	GOOS=linux CGO_ENABLED=0 $(GO) build ./...
	GOOS=linux CGO_ENABLED=0 $(GO) vet ./...

install: build
	install -d $(INSTALL_DIR)
	install -m 0755 $(BINARY) $(INSTALL_DIR)/$(BINARY)
	@pkill -f '$(abspath $(INSTALL_DIR))/$(BINARY) __agent' 2>/dev/null \
		&& echo "stopped stale agent ($(INSTALL_DIR)/$(BINARY) __agent)" || true
