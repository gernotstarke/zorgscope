# zorgscope — everything runs inside Docker; only `docker` and `make` are required locally (C-2).
#
# Local development mirrors production: the same backend image Fly runs, in a local container,
# against a libSQL server that speaks the same protocol as Turso. That is two processes, so two
# terminals:
#
#     terminal 1:  make backend    the backend image and its database on http://localhost:8080
#     terminal 2:  make client     the browser, pointed at that backend
#
# Add `make fakes` in a third terminal to develop against fixture upstreams instead of the real
# GitHub, Plausible and Todoist (FR-9.2).

SHELL          := /bin/sh
APP            := zorgscope
PORT           ?= 8080
GO_IMAGE       ?= golang:1.26
LINT_IMAGE     ?= golangci/golangci-lint:v2.12.0
IMAGEMAGICK    ?= dpokidov/imagemagick:latest
LIBSQL_SHELL   ?= ghcr.io/tursodatabase/libsql-shell:latest
FLY_IMAGE      ?= flyio/flyctl:latest
FLY_PLATFORM   ?= linux/amd64
FLY_APP        ?= zorgscope
FLY_CONFIG     ?= deploy/fly.toml
FLY_CONFIG_DIR ?= $(HOME)/.fly
COMPOSE        := docker compose -f deploy/compose.yml
GOCACHE_VOL    := $(APP)-gocache
GOMOD_VOL      := $(APP)-gomod
CGO_ENABLED    ?= 0

# Run a command inside the Go image with module and build caches persisted in named volumes.
GO_RUN          = docker run --rm -t \
                    -v "$(CURDIR)":/src -w /src \
                    -v $(GOMOD_VOL):/go/pkg/mod -v $(GOCACHE_VOL):/root/.cache/go-build \
                    -e CGO_ENABLED=$(CGO_ENABLED) $(GO_IMAGE)

# The same, but sharing the database container's network namespace so that the store tests reach
# libsql-server at localhost:8080 without publishing a port or guessing a Compose network name.
GO_RUN_DB       = docker run --rm -t \
                    --network=container:$$($(COMPOSE) ps -q db) \
                    -v "$(CURDIR)":/src -w /src \
                    -v $(GOMOD_VOL):/go/pkg/mod -v $(GOCACHE_VOL):/root/.cache/go-build \
                    $(GO_IMAGE)

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
.PHONY: backend client fakes stop logs
backend: check-env ## Run the backend image and its database locally; Ctrl-C stops it (terminal 1)
	@echo ">> backend on http://localhost:$(PORT) — Ctrl-C to stop"
	$(COMPOSE) up --build

client: ## Open the browser at the local backend (terminal 2)
	@printf '==> checking backend on http://localhost:%s ...\n' "$(PORT)"
	@if command -v curl >/dev/null 2>&1; then \
	  curl -fsS --max-time 3 "http://localhost:$(PORT)/healthz" >/dev/null 2>&1 || { \
	    printf '==> nothing answering there — run "make backend" in another terminal first\n'; exit 1; }; \
	  printf '==> backend answering (/healthz)\n'; \
	fi
	@printf '==> sign in with ZORGSCOPE_TOKEN from .env\n'
	@open "http://localhost:$(PORT)" 2>/dev/null || printf '==> open http://localhost:%s in your browser\n' "$(PORT)"

fakes: ## Serve fixture GitHub, Plausible and Todoist responses on http://localhost:9090 (terminal 3)
	docker run --rm -t -p 9090:9090 \
	  -v "$(CURDIR)":/src -w /src \
	  -v $(GOMOD_VOL):/go/pkg/mod -v $(GOCACHE_VOL):/root/.cache/go-build \
	  -e CGO_ENABLED=$(CGO_ENABLED) $(GO_IMAGE) go run ./cmd/fakesources

stop: ## Stop the local backend and database
	$(COMPOSE) down
logs: ## Tail local backend logs
	$(COMPOSE) logs -f

# check-env refuses to start on an incomplete .env instead of letting the container exit and retry.
# It tests only that a value is present — never what it is — and echoes no values (QS-4.3).
.PHONY: check-env
check-env:
	@test -f .env || { cp deploy/env.example .env; \
	  printf '\n  Created .env from deploy/env.example.\n\n'; \
	  printf '  Fill in ZORGSCOPE_TOKEN and REFRESH_SECRET (openssl rand -base64 32 each),\n'; \
	  printf '  then the tokens of the sources you want, and run make backend again.\n\n'; exit 1; }
	@ok=1; \
	need() { grep -Eq "^$$1=[^[:space:]#]" .env || { printf '  missing in .env: %s\n' "$$1"; ok=0; }; }; \
	need ZORGSCOPE_TOKEN; \
	need REFRESH_SECRET; \
	test $$ok -eq 1 || { \
	  printf '\n  The backend exits on a bad configuration rather than starting half-ready.\n'; \
	  printf '  Both values need at least 32 characters: openssl rand -base64 32\n\n'; exit 1; }

# ---------------------------------------------------------------- database
.PHONY: db-up db-shell db-migrate db-reset
db-up: ## Start only the local libsql-server
	$(COMPOSE) up -d db
db-shell: db-up ## Open a SQL shell against the local database
	docker run --rm -it --network=container:$$($(COMPOSE) ps -q db) $(LIBSQL_SHELL) http://localhost:8080
db-migrate: db-up ## Apply the schema to the local database (TURSO_URL overrides the target)
	@# The server migrates on every start-up, so this is for the operator rather than for running
	@# zorgscope: preparing a fresh Turso database before the first deploy, and confirming the
	@# schema applied without reading a Machine's logs. Migrate is idempotent.
	@# Override TURSO_URL and TURSO_AUTH_TOKEN to point it at production instead of the container.
	$(GO_RUN_DB) sh -c 'TURSO_URL=$${TURSO_URL:-http://localhost:8080} \
	  TURSO_AUTH_TOKEN=$${TURSO_AUTH_TOKEN:-} go run ./cmd/migrate'
db-reset: ## Drop the local database and its volume
	-$(COMPOSE) down -v

# ---------------------------------------------------------------- quality
.PHONY: test test-unit test-domain lint fmt tidy go
test: db-up ## All tests, with the race detector, against the local database
	@# -p 1 runs one package at a time. More than one package now tests against the database —
	@# internal/adapters/libsql and internal/refresh — and there is one local libsql-server holding
	@# one database, which each of them truncates between tests. Run in parallel they delete each
	@# other's rows and deadlock on writes ("SQLite error: database is locked"); run sequentially
	@# they are deterministic.
	$(GO_RUN_DB) sh -c 'CGO_ENABLED=1 TEST_TURSO_URL=http://localhost:8080 \
	  go test -race -p 1 -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1'

test-unit: ## Tests that need no database (store tests skip themselves)
	$(GO_RUN) sh -c 'CGO_ENABLED=1 go test -race ./...'

test-domain: ## Domain tests only; fails below 90% coverage (QS-5.1)
	$(GO_RUN) sh -c 'go test -coverprofile=domain.out ./internal/domain/... && \
	  go tool cover -func=domain.out | tail -1 | \
	  awk "{ if (\$$3+0 < 90) { print \"domain coverage below 90%\"; exit 1 } }"'

lint: ## go vet + golangci-lint (includes the depguard architecture rules)
	$(GO_RUN) go vet ./...
	docker run --rm -t -v "$(CURDIR)":/src -w /src -v $(GOMOD_VOL):/go/pkg/mod $(LINT_IMAGE) golangci-lint run ./...

fmt: ## gofmt all Go files
	$(GO_RUN) gofmt -l -w cmd internal

tidy: ## go mod tidy
	$(GO_RUN) go mod tidy

go: ## Run any go command in the Go container: make go ARGS="test ./... -run TestX -v"
	$(GO_RUN) go $(ARGS)

.PHONY: docs-check check
docs-check: ## Lint Markdown and check links in docs/
	docker run --rm -v "$(CURDIR)":/work -w /work davidanson/markdownlint-cli2:latest "docs/**/*.md" "README.md"
	docker run --rm -v "$(CURDIR)":/work -w /work lycheeverse/lychee:latest --offline --no-progress "docs/**/*.md" "README.md"

check: lint test docs-check ## Everything CI runs except deploy

# ---------------------------------------------------------------- build & deploy
.PHONY: build image
build: ## Build static binaries into ./bin
	$(GO_RUN) sh -c 'go build -trimpath -ldflags="-s -w" -o bin/ ./cmd/...'
image: ## Build the production container image
	docker build -f deploy/Dockerfile -t $(APP):local .

.PHONY: deploy fly-login fly-whoami fly-validate fly-deploy fly-status fly-checks fly-logs
.PHONY: fly-releases fly-secrets fly-secrets-import fly-ssh fly
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
fly-checks: ## Show deployed liveness check results
	$(FLY_RUN) checks list --app "$(FLY_APP)"
fly-logs: ## Stream production logs until interrupted
	$(FLY_RUN) logs --app "$(FLY_APP)"
fly-releases: ## List recent production releases
	$(FLY_RUN) releases --app "$(FLY_APP)"
fly-secrets: ## List secret names and deployment status; never values
	$(FLY_RUN) secrets list --app "$(FLY_APP)"
fly-secrets-import: ## Import NAME=VALUE secrets from stdin, not arguments
	$(FLY_RUN) secrets import --app "$(FLY_APP)"
fly-ssh: ## Open an interactive console on the Fly Machine
	$(FLY_RUN_IT) ssh console --app "$(FLY_APP)"
fly: ## Run any non-interactive flyctl command with ARGS="..."
	@test -n "$(ARGS)" || { echo 'usage: make fly ARGS="apps list"'; exit 2; }
	$(FLY_RUN) $(ARGS)

# ---------------------------------------------------------------- assets
.PHONY: logo
logo: ## Regenerate the static logo assets from docs/logo/zorgscope-logo-sheet.jpeg
	@# The master emblem is cropped from the logo sheet (a cleaner render than the standalone
	@# file), its near-black background is flood-filled from the four corners to transparency so
	@# the mark works on both the light and the dark theme, and the result is quantised — the art
	@# is flat colour, so 32 colours is lossless in practice and keeps the page inside its budget.
	docker run --rm --entrypoint magick -v "$(CURDIR)":/w -w /w $(IMAGEMAGICK) \
	  docs/logo/zorgscope-logo-sheet.jpeg -crop 1120x920+950+140 +repage -alpha set -fuzz 10% \
	  -fill none -floodfill +0+0 "srgb(38,39,43)" -fill none -floodfill +1119+0 "srgb(38,39,43)" \
	  -fill none -floodfill +0+919 "srgb(38,39,43)" -fill none -floodfill +1119+919 "srgb(38,39,43)" \
	  -trim +repage -background none -gravity center -extent 816x816 /w/.logo-master.png
	@# 96 px, not 256: the only thing that renders logo.png is the page header, at around 28 px,
	@# and 96 covers a 3x display exactly. PNG does not gzip, so every byte of it is a byte on the
	@# wire against QS-2.3 — 256 px would spend seven kilobytes to draw a 28 px mark.
	docker run --rm --entrypoint magick -v "$(CURDIR)":/w -w /w $(IMAGEMAGICK) /w/.logo-master.png \
	  -resize 96x96 -colors 32 -strip -define png:compression-level=9 internal/web/static/logo.png
	docker run --rm --entrypoint magick -v "$(CURDIR)":/w -w /w $(IMAGEMAGICK) /w/.logo-master.png \
	  -resize 180x180 -colors 32 -strip -define png:compression-level=9 internal/web/static/apple-touch-icon.png
	docker run --rm --entrypoint magick -v "$(CURDIR)":/w -w /w $(IMAGEMAGICK) /w/.logo-master.png \
	  \( -clone 0 -resize 16x16 \) \( -clone 0 -resize 32x32 \) -delete 0 -colors 32 -strip internal/web/static/favicon.ico
	rm -f .logo-master.png
	@echo ">> regenerated internal/web/static/{logo.png,apple-touch-icon.png,favicon.ico}"

.PHONY: clean
clean: ## Remove build output, caches and local data
	rm -rf bin coverage.out domain.out
	-$(COMPOSE) down -v
	-docker volume rm $(GOMOD_VOL) $(GOCACHE_VOL)
