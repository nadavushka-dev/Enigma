.PHONY: check test build hooks

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
