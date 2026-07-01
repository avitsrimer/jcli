BINARY      ?= jcli
PKG         ?= ./cmd/jcli
INSTALL_DIR ?= $(HOME)/bin
# must match the cert common-name created by scripts/make-cert.sh.
# changing it breaks the keychain ACL — see scripts/make-cert.sh header.
SIGN_ID     ?= jcli Code Signing

GO ?= GOTOOLCHAIN=local go

# absolute path of the built binary, used to target only the agent spawned from
# this binary (an installed copy at a different path is left alone).
BIN_ABS := $(abspath $(BINARY))

.PHONY: build test lint fmt cross-build cert sign show-dr install stop-agent

# stop a credential agent spawned from this binary. the agent is a long-lived
# process holding the old code in memory, so a rebuild/re-sign must replace it —
# otherwise the next command reconnects to the stale agent. a fresh one spawns on
# demand at the next command. no-op (never fails the build) when none is running.
stop-agent:
	@pkill -f '$(BIN_ABS) __agent' 2>/dev/null && echo "stopped stale agent ($(BINARY) __agent)" || true

build: stop-agent
	$(GO) build -o $(BINARY) $(PKG)

test:
	$(GO) test -race ./...

lint:
	golangci-lint run

fmt:
	gofmt -s -w .
	goimports -w .

# prove the non-darwin keychain stub keeps the repo cross-buildable.
cross-build:
	GOOS=linux CGO_ENABLED=0 $(GO) build ./...
	GOOS=linux CGO_ENABLED=0 $(GO) vet ./...

# create-or-reuse the self-signed code-signing identity (idempotent; never regenerates).
cert:
	./scripts/make-cert.sh

sign: build
	codesign --force --options runtime --sign "$(SIGN_ID)" $(BINARY)
	@$(MAKE) --no-print-directory show-dr

# print the designated requirement so it can be recorded/verified.
# rebuild + re-sign with the same cert must produce a STABLE DR string.
show-dr: build
	@echo "designated requirement for $(BINARY):"
	@codesign -d -r- $(BINARY) 2>/dev/null || echo "  (binary not signed yet — run 'make sign')"

install: sign
	install -d $(INSTALL_DIR)
	install -m 0755 $(BINARY) $(INSTALL_DIR)/$(BINARY)
	@pkill -f '$(abspath $(INSTALL_DIR))/$(BINARY) __agent' 2>/dev/null \
		&& echo "stopped stale agent ($(INSTALL_DIR)/$(BINARY) __agent)" || true
