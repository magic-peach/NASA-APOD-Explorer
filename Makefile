# Common tasks. Run `make help` to list them.

BINARY := bin/server
IMAGE  := nasa-apod-explorer:local

.PHONY: help run demo build test race cover bench lint fmt docker-build docker-run

help: ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "} {printf "  %-14s %s\n", $$1, $$2}'

run: ## Run the server locally (reads env vars; see .env.example)
	go run ./cmd/server

demo: ## Run with built-in sample images instead of NASA's API
	DEMO_MODE=1 go run ./cmd/server

build: ## Build the binary into bin/
	CGO_ENABLED=0 go build -trimpath -o $(BINARY) ./cmd/server

test: ## Run all tests
	go test ./...

race: ## Run all tests with the race detector
	go test -race ./...

cover: ## Tests with coverage report (coverage.html)
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1
	go tool cover -html=coverage.out -o coverage.html

bench: ## Run benchmarks (cache and singleflight)
	go test -run '^$$' -bench . -benchmem ./internal/cache ./internal/apod

lint: ## gofmt check, go vet and golangci-lint
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed on:"; gofmt -l .; exit 1)
	go vet ./...
	golangci-lint run

fmt: ## Format all Go code
	gofmt -w .

docker-build: ## Build the container image
	docker build -t $(IMAGE) .

docker-run: docker-build ## Run the image with a named volume for SQLite
	docker run --rm -p 8080:8080 --env-file $$( [ -f .env ] && echo .env || echo /dev/null ) -v apod-data:/data $(IMAGE)
