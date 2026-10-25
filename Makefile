GOWORK ?= off
export GOWORK

.PHONY: setup check test build run generate check-generated hooks
setup:
	LEFTHOOK=0 pnpm install --frozen-lockfile
check: check-generated
	pnpm exec stuhelper-check repository
	node scripts/check-contracts.mjs
	node scripts/check-boundaries.mjs
	node --test scripts/*.test.mjs
	test -z "$$(gofmt -l module transport internal application examples)"
	go vet ./...
	go test -race ./...
	go build ./...
	pnpm exec stuhelper-check workflows
test:
	go test -race ./...
build:
	go build ./...
run:
	go run ./examples/server
generate:
	node scripts/generate.mjs
check-generated:
	node scripts/generate.mjs --check
hooks:
	pnpm exec lefthook install
