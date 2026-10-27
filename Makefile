# tjsocks — development tasks
#
# The module path is a placeholder: run `make rename` once after cloning into
# your own GitHub account so imports match the repository URL.

MODULE   ?= github.com/your-github-user/tjsocks
BIN      ?= bin/tjsocks
IMAGE    ?= tjsocks
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS   = -s -w \
            -X $(MODULE)/internal/version.Version=$(VERSION) \
            -X $(MODULE)/internal/version.Commit=$(COMMIT) \
            -X $(MODULE)/internal/version.Date=$(DATE)

.DEFAULT_GOAL := help
.PHONY: help build test test-race vet fmt fmt-check cover lint run docker docker-run clean tidy rename vendor

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

build: ## Build the binary into bin/
	go build -mod=vendor -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/tjsocks

test: ## Run the unit tests
	go test -mod=vendor ./...

test-race: ## Run the unit tests with the race detector
	go test -mod=vendor -race ./...

vet: ## Run go vet
	go vet -mod=vendor ./...

fmt: ## Format the sources
	gofmt -w cmd internal

fmt-check: ## Fail when sources are not gofmt'd
	@test -z "$$(gofmt -l cmd internal)" || { gofmt -l cmd internal; exit 1; }

cover: ## Report test coverage
	go test -mod=vendor -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -n 1

lint: fmt-check vet ## Formatting + vet

run: build ## Run locally (requires TJ_USER / TJ_PASSWORD)
	./$(BIN)

docker: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
	  --build-arg DATE=$(DATE) -t $(IMAGE) .

docker-run: docker ## Run the container image on :1080
	docker run --rm -p 1080:1080/tcp -p 1080:1080/udp \
	  -e TJ_USER -e TJ_PASSWORD -e SOCKS_USER -e SOCKS_PASS $(IMAGE)

tidy: ## Tidy modules and refresh vendor/
	go mod tidy
	go mod vendor

vendor: ## Refresh the vendor directory
	go mod vendor

rename: ## Rewrite the module path (usage: make rename MODULE=github.com/you/tjsocks)
	@test -n "$(MODULE)" || { echo "MODULE is required"; exit 1; }
	@echo "Renaming module to $(MODULE)"
	@sed -i.bak "s|github.com/your-github-user/tjsocks|$(MODULE)|g" go.mod $$(find cmd internal -name '*.go') Dockerfile Makefile
	@find . -name '*.bak' -delete
	@go build -mod=vendor ./... && echo "OK"

clean: ## Remove build artefacts
	rm -rf bin coverage.out
