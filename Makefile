.PHONY: check test build dist snapshot hooks e2e e2e-long

# Dev builds are ordered by commit time (SPEC §11): a newer build upgrades the group.
VERSION ?= v0.0.0-dev.$(shell TZ=UTC git show -s --format=%cd --date=format-local:%Y%m%d%H%M%S HEAD).$(shell git rev-parse --short=7 HEAD)
LDFLAGS := -X github.com/nadavushka-dev/enigma/internal/buildinfo.Version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
DIST ?= $(HOME)/.local/share/enigma/bin/$(VERSION)

check: ## everything the pre-commit hook runs
	@test -z "$$(gofmt -l .)" || (gofmt -l . && false)
	go vet ./...
	go test -race -count=1 ./...

test:
	go test -race ./...

build:
	go build -ldflags "$(LDFLAGS)" -o bin/enigma ./cmd/enigma

dist: ## binaries for every platform, for machine add and upgrades (DIST=<data>/bin/<version>)
	@mkdir -p $(DIST)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "$(DIST)/enigma-$$os-$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o $(DIST)/enigma-$$os-$$arch ./cmd/enigma || exit 1; \
	done

snapshot: ## a local release build with GoReleaser, nothing published (dist/)
	goreleaser release --snapshot --clean

hooks:
	git config core.hooksPath .githooks

e2e: ## Docker end-to-end: 3 Linux containers with sshd (needs Docker, ~20s; ENIGMA_E2E_MAC=1 adds this Mac)
	go test -tags docker -count=1 -timeout 15m -run TestDocker ./cmd/enigma/

e2e-long: ## e2e plus a long network cut that outlives ssh keepalive (~80s)
	ENIGMA_E2E_LONG=1 go test -tags docker -count=1 -timeout 20m -run TestDocker ./cmd/enigma/
