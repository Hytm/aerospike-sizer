# `make` seul liste les cibles. `make ci` : les vérifications locales avant chaque merge (pas de CI GitHub).
SHELL := /bin/bash
.DEFAULT_GOAL := help

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/Hytm/aerospike-sizer/internal/buildinfo.Version=$(VERSION)
BIN := bin/sizer
DEV_ADDR ?= 127.0.0.1:7311

.PHONY: help
help: ## Liste les cibles
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: fmt
fmt: ## Formate le code Go
	gofmt -w cmd internal

.PHONY: lint
lint: ## gofmt, go vet, staticcheck
	@test -z "$$(gofmt -l cmd internal)" || { gofmt -l cmd internal; exit 1; }
	go vet ./...
	go tool staticcheck ./...

.PHONY: test
test: ## Tests Go (avec -race)
	go test -race ./...

.PHONY: build
build: ## Construit bin/sizer
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/sizer

.PHONY: ci
ci: lint test build ## CI locale : lint, tests, build

.PHONY: run
run: build ## Serveur au premier plan sur DEV_ADDR (n'affecte pas le service)
	SIZER_ADDR=$(DEV_ADDR) ./$(BIN) serve

.PHONY: deploy
deploy: build ## Relève le service launchd sans coupure (deux exemplaires)
	./$(BIN) deploy

.PHONY: status
status: build ## État du service launchd
	./$(BIN) status
