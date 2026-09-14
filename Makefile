# zorgscope — everything runs inside Docker; only `docker` and `make` are required locally (C-2).
#
# Local development mirrors production: the same backend image Fly runs, in a local container,
# against a libSQL server that speaks the same protocol as Turso. That is two processes, so two
# terminals:
#
#     terminal 1:  make backend    the backend image and its database on http://localhost:8080
#     terminal 2:  make client     the browser, pointed at that backend
#
# Add `make fakes` in a third terminal to develop against a fixture GitHub instead of the real
# one (FR-9.2). `make check` runs what CI runs; `make clean` resets.

SHELL          := /bin/sh
APP            := zorgscope
PORT           ?= 8080
GO_IMAGE       ?= golang:1.26
LINT_IMAGE     ?= golangci/golangci-lint:v2.12.0
FLY_IMAGE      ?= flyio/flyctl:latest
FLY_PLATFORM   ?= linux/amd64
FLY_APP        ?= zorgscope
FLY_CONFIG     ?= deploy/fly.toml
FLY_CONFIG_DIR ?= $(HOME)/.fly
COMPOSE        := docker compose -f deploy/compose.yml
GOCACHE_VOL    := $(APP)-gocache
GOMOD_VOL      := $(APP)-gomod

# Run a command inside the Go image with module and build caches persisted in named volumes.
GO_RUN    = docker run --rm -t \
              -v "$(CURDIR)":/src -w /src \
              -v $(GOMOD_VOL):/go/pkg/mod -v $(GOCACHE_VOL):/root/.cache/go-build \
              $(GO_IMAGE)

# The same, but sharing the database container's network namespace so that the store tests reach
# libsql-server at localhost:8080 without publishing a port or guessing a Compose network name.
GO_RUN_DB = docker run --rm -t \
              --network=container:$$($(COMPOSE) ps -q db) \
              -v "$(CURDIR)":/src -w /src \
              -v $(GOMOD_VOL):/go/pkg/mod -v $(GOCACHE_VOL):/root/.cache/go-build \
              $(GO_IMAGE)

# The scratch-based flyctl image has no HOME, so point it at the mounted host config explicitly.
FLY_RUN   = docker run --rm -i --platform "$(FLY_PLATFORM)" \
              -v "$(CURDIR)":/src -w /src \
              -e FLY_API_TOKEN -e FLY_CONFIG_DIR=/fly-config \
              -v "$(FLY_CONFIG_DIR)":/fly-config $(FLY_IMAGE)

.DEFAULT_GOAL := help
.PHONY: help backend client fakes check clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

backend: ## Run the backend image and its database locally; Ctrl-C stops it (terminal 1)
	@# Refuse to start on an incomplete .env instead of letting the container exit and retry.
	@# Tests only that a value is present — never what it is — and echoes no values (QS-4.3).
	@test -f .env || { cp deploy/env.example .env; \
	  printf '\n  Created .env from deploy/env.example.\n\n'; \
	  printf '  Fill in GITHUB_OAUTH_CLIENT_ID, GITHUB_OAUTH_CLIENT_SECRET and REFRESH_SECRET (openssl rand -base64 32),\n'; \
	  printf '  then the tokens of the sources you want, and run make backend again.\n\n'; exit 1; }
	@ok=1; \
	need() { grep -Eq "^$$1=[^[:space:]#]" .env || { printf '  missing in .env: %s\n' "$$1"; ok=0; }; }; \
	need GITHUB_OAUTH_CLIENT_ID; \
	need GITHUB_OAUTH_CLIENT_SECRET; \
	need REFRESH_SECRET; \
	test $$ok -eq 1 || { \
	  printf '\n  The backend exits on a bad configuration rather than starting half-ready.\n'; \
	  printf '  The OAuth pair comes from the GitHub OAuth App; REFRESH_SECRET needs at least 32 characters\n\n'; exit 1; }
	@echo ">> backend on http://localhost:$(PORT) — Ctrl-C to stop"
	$(COMPOSE) up --build

client: ## Open the browser at the local backend (terminal 2)
	@printf '==> checking backend on http://localhost:%s ...\n' "$(PORT)"
	@if command -v curl >/dev/null 2>&1; then \
	  curl -fsS --max-time 3 "http://localhost:$(PORT)/healthz" >/dev/null 2>&1 || { \
	    printf '==> nothing answering there — run "make backend" in another terminal first\n'; exit 1; }; \
	  printf '==> backend answering (/healthz)\n'; \
	fi
	@printf '==> sign in with GitHub\n'
	@open "http://localhost:$(PORT)" 2>/dev/null || printf '==> open http://localhost:%s in your browser\n' "$(PORT)"

fakes: ## Serve fixture GitHub, Plausible and Todoist responses on http://localhost:9090 (terminal 3)
	docker run --rm -t -p 9090:9090 \
	  -v "$(CURDIR)":/src -w /src \
	  -v $(GOMOD_VOL):/go/pkg/mod -v $(GOCACHE_VOL):/root/.cache/go-build \
	  $(GO_IMAGE) go run ./cmd/fakesources

check: ## Everything CI runs: vet, lint, tests, domain coverage gate, docs, fly.toml validation
	$(GO_RUN) go vet ./...
	docker run --rm -t -v "$(CURDIR)":/src -w /src -v $(GOMOD_VOL):/go/pkg/mod $(LINT_IMAGE) golangci-lint run ./...
	@# -p 1 runs one package at a time: more than one package tests against the single local
	@# libsql-server and truncates it between tests. Run in parallel they delete each other's
	@# rows and deadlock on writes; run sequentially they are deterministic.
	$(COMPOSE) up -d db
	$(GO_RUN_DB) sh -c 'CGO_ENABLED=1 TEST_TURSO_URL=http://localhost:8080 \
	  go test -race -p 1 -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1'
	$(GO_RUN) sh -c 'go test -coverprofile=domain.out ./internal/domain/... && \
	  go tool cover -func=domain.out | tail -1 | \
	  awk "{ if (\$$3+0 < 90) { print \"domain coverage below 90%\"; exit 1 } }"'
	docker run --rm -v "$(CURDIR)":/work -w /work davidanson/markdownlint-cli2:latest "docs/**/*.md" "README.md"
	docker run --rm -v "$(CURDIR)":/work -w /work lycheeverse/lychee:latest --offline --no-progress "docs/**/*.md" "README.md"
	$(FLY_RUN) config validate --strict --app "$(FLY_APP)" --config "$(FLY_CONFIG)"

clean: ## Stop the local backend; remove build output, caches and local data
	rm -rf bin coverage.out domain.out
	-$(COMPOSE) down -v
	-docker volume rm $(GOMOD_VOL) $(GOCACHE_VOL)
