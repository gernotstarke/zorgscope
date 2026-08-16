# 2. Constraints

All constraints are listed and motivated in [requirements ch. 6](../requirements/06-constraints.md) (C‑1 … C‑14).
Technical consequences drawn from them:

| Constraint | Consequence in the architecture |
|-----------|-----------------------------------|
| C‑1 Go | Std‑lib first: `net/http` (Go 1.22+ mux), `html/template`, `log/slog`, `database/sql`. Few, well‑known dependencies (see ADR‑0002). |
| C‑2 Docker + make only | Every make target wraps a `docker run`/`docker compose`; caches in named volumes; multi‑stage Dockerfile builds the same binary CI ships. |
| C‑3 fly.io single machine | In‑process scheduler instead of external cron; SQLite on volume instead of a database service; `min_machines_running = 1`. |
| C‑5 single user, passkeys | No user table beyond one account row and its credentials; no roles. |
| C‑6 read‑only | Tokens with read scopes; no write endpoints towards upstream. |
| C‑7 no third‑party browser requests | htmx vendored under `web/static/`, system font stack, CSP `default-src 'self'`. |
| C‑9 layout | Package boundaries enforced by `depguard` in golangci‑lint. |
| C‑14 cheap agents | Explicit interfaces, one concern per package, plan tasks with test‑first steps. |
