# zorgscope — everything runs inside Docker; only `docker` and `make` are required locally (C-2).
#
# Stateless: no database, no refresh pipeline. The backend fetches straight from GitHub on demand
# (FR-1.2). That is one process to run against the real GitHub:
#
#     make dev    the backend image on http://localhost:8080, real GitHub; Ctrl-C stops it
#
# `make check` runs what CI runs, plus markdownlint and fly.toml validation;
# `make deploy` deploys from this laptop; `make clean` resets.

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

# The scratch-based flyctl image has no HOME, so point it at the mounted host config explicitly.
FLY_RUN   = docker run --rm -i --platform "$(FLY_PLATFORM)" \
              -v "$(CURDIR)":/src -w /src \
              -e FLY_API_TOKEN -e FLY_CONFIG_DIR=/fly-config \
              -v "$(FLY_CONFIG_DIR)":/fly-config $(FLY_IMAGE)

.DEFAULT_GOAL := help
.PHONY: help dev check deploy clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

dev: ## Run the backend image locally against the real GitHub on http://localhost:8080; Ctrl-C stops it
	@# Refuse to start on an incomplete .env instead of letting the container exit and retry.
	@# Tests only that a value is present — never what it is — and echoes no values (QS-4.3).
	@test -f .env || { cp deploy/env.example .env; \
	  printf '\n  Created .env from deploy/env.example.\n\n'; \
	  printf '  Fill in GITHUB_OAUTH_CLIENT_ID and GITHUB_OAUTH_CLIENT_SECRET from the local GitHub OAuth App\n'; \
	  printf '  (callback http://localhost:8080/auth/callback), plus GITHUB_TOKEN, and run make dev again.\n\n'; exit 1; }
	@ok=1; \
	need() { grep -Eq "^$$1=[^[:space:]#]" .env || { printf '  missing in .env: %s\n' "$$1"; ok=0; }; }; \
	need GITHUB_OAUTH_CLIENT_ID; \
	need GITHUB_OAUTH_CLIENT_SECRET; \
	need GITHUB_TOKEN; \
	test $$ok -eq 1 || { \
	  printf '\n  The backend exits on a bad configuration rather than starting half-ready.\n'; \
	  printf '  The OAuth pair comes from the local GitHub OAuth App (callback http://localhost:8080/auth/callback);\n'; \
	  printf '  GITHUB_TOKEN is a personal access token used to call the GitHub API.\n\n'; exit 1; }
	@echo ">> backend on http://localhost:$(PORT) — Ctrl-C to stop"
	$(COMPOSE) up --build

check: ## Everything CI runs, plus fly.toml validation: vet, lint, race tests, domain coverage, markdownlint
	$(GO_RUN) go vet ./...
	docker run --rm -t -v "$(CURDIR)":/src -w /src -v $(GOMOD_VOL):/go/pkg/mod $(LINT_IMAGE) golangci-lint run ./...
	$(GO_RUN) sh -c 'go test -race -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1'
	$(GO_RUN) sh -c 'go test -coverprofile=domain.out ./internal/domain/... && \
	  go tool cover -func=domain.out | tail -1 | \
	  awk "{ if (\$$3+0 < 90) { print \"domain coverage below 90%\"; exit 1 } }"'
	docker run --rm -v "$(CURDIR)":/work -w /work davidanson/markdownlint-cli2:latest "docs/**/*.md" "README.md"
	$(FLY_RUN) config validate --strict --app "$(FLY_APP)" --config "$(FLY_CONFIG)"

deploy: ## Build remotely on Fly and deploy; needs a fly login on this machine
	$(FLY_RUN) deploy --remote-only --config $(FLY_CONFIG)

clean: ## Stop the local backend; remove build output and caches
	rm -rf bin coverage.out domain.out
	-$(COMPOSE) down
	-docker volume rm $(GOMOD_VOL) $(GOCACHE_VOL)
