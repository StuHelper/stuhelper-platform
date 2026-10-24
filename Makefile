GOWORK ?= off
export GOWORK

.PHONY: check test build run generate check-generated

check: check-generated
	python3 scripts/check_contracts.py
	python3 scripts/check_boundaries.py
	python3 -m unittest discover -s scripts -p 'test_*.py'
	test -z "$$(gofmt -l cmd module transport internal application examples)"
	go vet ./...
	go test -race ./...
	go build -o bin/stuhelper-platform ./cmd/platform

test:
	go test -race ./...

build:
	go build -o bin/stuhelper-platform ./cmd/platform

run:
	go run ./cmd/platform

generate:
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 --config contracts/oapi-codegen.yaml contracts/platform.openapi.yaml
	npm run generate

check-generated:
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	cp internal/api/gen/platform.gen.go "$$tmp/go"; \
	cp sdk/platform.gen.ts "$$tmp/ts"; \
	$(MAKE) generate && \
	cmp internal/api/gen/platform.gen.go "$$tmp/go" && \
	cmp sdk/platform.gen.ts "$$tmp/ts"

setup:
	npm ci --ignore-scripts
