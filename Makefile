# zorgscope — everything runs inside Docker; only `docker` and `make` are required locally.
#
# Conventions:
#   * Go toolchain, linters, Playwright and flyctl are used through official images, never installed locally.
#   * `make app` is the one command to bring the dashboard up on http://localhost:8080.

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
COMPOSE_E2E    := docker compose -f deploy/compose.e2e.yml
GOCACHE_VOL    := $(APP)-gocache
GOMOD_VOL      := $(APP)-gomod
CGO_ENABLED    ?= 0
# Run a command inside the Go image with module & build caches persisted in named volumes.
GO_RUN          = docker run --rm -t \
                    -v "$(CURDIR)":/src -w /src \
                    -v $(GOMOD_VOL):/go/pkg/mod -v $(GOCACHE_VOL):/root/.cache/go-build \
                    -e CGO_ENABLED=$(CGO_ENABLED) $(GO_IMAGE)
# The scratch-based flyctl image has no HOME, so point it at the mounted host config explicitly.
FLY_DOCKER_ARGS = --platform "$(FLY_PLATFORM)" \
                    -v "$(CURDIR)":/src -w /src \
                    -e FLY_API_TOKEN -e FLY_CONFIG_DIR=/fly-config \
                    -v "$(FLY_CONFIG_DIR)":/fly-config
FLY_RUN          = docker run --rm -i $(FLY_DOCKER_ARGS) $(FLY_IMAGE)
FLY_RUN_IT       = docker run --rm -it $(FLY_DOCKER_ARGS) $(FLY_IMAGE)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-19s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------- run
.PHONY: app zorgscope
app: ## Build and run the dashboard locally in Docker (http://localhost:$(PORT))
	@test -f .env || { cp deploy/env.example .env; echo ">> created .env from deploy/env.example — fill in your tokens"; }
	$(COMPOSE) up --build -d
	@echo ">> zorgscope running at http://localhost:$(PORT)"
zorgscope: app ## Alias for `make app`

.PHONY: stop logs
stop: ## Stop the local dashboard
	$(COMPOSE) down
logs: ## Tail local dashboard logs
	$(COMPOSE) logs -f

# ---------------------------------------------------------------- quality
.PHONY: test test-domain lint fmt tidy
test: ## Unit + integration tests with race detector and coverage
	$(GO_RUN) sh -c 'CGO_ENABLED=1 go test -race -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1'
test-domain: ## Domain tests only, enforce ≥90% coverage
	$(GO_RUN) sh -c 'go test -coverprofile=domain.out ./internal/domain/... && go tool cover -func=domain.out | tail -1 | awk "{ if (\$$3+0 < 90) { print \"domain coverage below 90%\"; exit 1 } }"'
lint: ## go vet + golangci-lint
	$(GO_RUN) go vet ./...
	docker run --rm -t -v "$(CURDIR)":/src -w /src -v $(GOMOD_VOL):/go/pkg/mod $(LINT_IMAGE) golangci-lint run ./...
fmt: ## gofmt all Go files
	$(GO_RUN) gofmt -l -w cmd internal
tidy: ## go mod tidy
	$(GO_RUN) go mod tidy

.PHONY: go
go: ## Run any go command in the Go container: make go ARGS="test ./... -run TestX -v"
	$(GO_RUN) go $(ARGS)

.PHONY: e2e
e2e: ## End-to-end tests: app + fake sources + Playwright (Docker Compose)
	$(COMPOSE_E2E) up --build --abort-on-container-exit --exit-code-from playwright
	$(COMPOSE_E2E) down -v

.PHONY: demo
demo: ## Run the dashboard against fake sources on http://localhost:$(PORT) (no tokens needed)
	$(COMPOSE_E2E) up --build -d fakesources zorgscope
	@echo ">> demo running at http://localhost:$(PORT) (stop with: make demo-stop)"
.PHONY: demo-stop
demo-stop: ## Stop the demo
	$(COMPOSE_E2E) down -v

.PHONY: docs-check
docs-check: ## Lint Markdown and check links in docs/
	docker run --rm -v "$(CURDIR)":/work -w /work davidanson/markdownlint-cli2:latest "docs/**/*.md" "README.md"
	docker run --rm -v "$(CURDIR)":/work -w /work lycheeverse/lychee:latest --offline --no-progress "docs/**/*.md" "README.md"

.PHONY: check
check: lint test docs-check ## Everything CI runs except e2e and deploy

# ---------------------------------------------------------------- build & deploy
.PHONY: build image
build: ## Build static binaries into ./bin
	$(GO_RUN) sh -c 'go build -trimpath -ldflags="-s -w" -o bin/ ./cmd/...'
image: ## Build the production container image
	docker build -f deploy/Dockerfile -t $(APP):local .

.PHONY: deploy fly-login fly-whoami fly-validate fly-deploy fly-status fly-checks fly-logs
.PHONY: fly-releases fly-volumes fly-secrets fly-secrets-import fly-ssh fly
fly-login: ## Authenticate flyctl in Docker; saves the session under ~/.fly
	$(FLY_RUN_IT) auth login
fly-whoami: ## Show the Fly account used by local make targets
	$(FLY_RUN) auth whoami
fly-validate: ## Strictly validate deploy/fly.toml for FLY_APP
	$(FLY_RUN) config validate --strict --app "$(FLY_APP)" --config "$(FLY_CONFIG)"
fly-deploy: fly-validate ## Validate and deploy FLY_APP with Fly's remote builder
	$(FLY_RUN) deploy --remote-only --app "$(FLY_APP)" --config "$(FLY_CONFIG)"
deploy: fly-deploy ## Alias for `make fly-deploy`
fly-status: ## Show the Fly app and Machine status
	$(FLY_RUN) status --app "$(FLY_APP)"
fly-checks: ## Show deployed liveness and readiness check results
	$(FLY_RUN) checks list --app "$(FLY_APP)"
fly-logs: ## Stream production logs until interrupted
	$(FLY_RUN) logs --app "$(FLY_APP)"
fly-releases: ## List recent production releases
	$(FLY_RUN) releases --app "$(FLY_APP)"
fly-volumes: ## List persistent volumes and attachment state
	$(FLY_RUN) volumes list --app "$(FLY_APP)"
fly-secrets: ## List secret names and deployment status; never values
	$(FLY_RUN) secrets list --app "$(FLY_APP)"
fly-secrets-import: ## Import NAME=VALUE secrets from stdin, not arguments
	$(FLY_RUN) secrets import --app "$(FLY_APP)"
fly-ssh: ## Open an interactive console on the Fly Machine
	$(FLY_RUN_IT) ssh console --app "$(FLY_APP)"
fly: ## Run any non-interactive flyctl command with ARGS="..."
	@test -n "$(ARGS)" || { echo 'usage: make fly ARGS="apps list"'; exit 2; }
	$(FLY_RUN) $(ARGS)

.PHONY: clean
clean: ## Remove build output, caches and local data
	rm -rf bin coverage.out domain.out
	-$(COMPOSE) down -v
	-docker volume rm $(GOMOD_VOL) $(GOCACHE_VOL)
