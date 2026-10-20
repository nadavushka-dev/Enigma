.PHONY: check test build hooks e2e e2e-long

check: ## everything the pre-commit hook runs
	@test -z "$$(gofmt -l .)" || (gofmt -l . && false)
	go vet ./...
	go test -race -count=1 ./...

test:
	go test -race ./...

build:
	go build -o bin/enigma ./cmd/enigma

hooks:
	git config core.hooksPath .githooks

e2e: ## Docker end-to-end: 3 Linux containers with sshd (needs Docker, ~20s; ENIGMA_E2E_MAC=1 adds this Mac)
	go test -tags docker -count=1 -timeout 15m -run TestDocker ./cmd/enigma/

e2e-long: ## e2e plus a long network cut that outlives ssh keepalive (~80s)
	ENIGMA_E2E_LONG=1 go test -tags docker -count=1 -timeout 20m -run TestDocker ./cmd/enigma/
