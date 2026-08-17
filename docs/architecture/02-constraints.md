# 2. Constraints

All constraints are listed and motivated in [requirements ch. 6](../requirements/06-constraints.md) (C‑1 … C‑14).
Technical consequences drawn from them:

| Constraint | Consequence in the architecture |
|-----------|-----------------------------------|
| C‑1 Go | Std-lib first: `net/http` (method+pattern mux), `encoding/json`, `log/slog`, `database/sql`, `crypto/*`. Few, well-known dependencies (see ADR-0002). |
| C‑2 Docker + make only | Every make target wraps a `docker run`/`docker compose`; caches in named volumes; multi‑stage Dockerfile builds the same binary CI ships. |
| C‑3 fly.io single machine | In‑process scheduler instead of external cron; SQLite on volume instead of a database service; `min_machines_running = 1`. |
| C‑5 single user | No tenants or roles. Bootstrap bearer auth is temporary; passkey-backed device credentials are planned. |
| C‑6 read‑only | Tokens with read scopes; no write endpoints towards upstream. |
| C‑7 clients do not call upstream | Only backend adapters possess GitHub/Plausible secrets; optional browser assets remain self-hosted with strict CSP. |
| C‑9 layout | Package boundaries enforced by `depguard` in golangci‑lint. |
| C‑14 cheap agents | Explicit interfaces, one concern per package, plan tasks with test‑first steps. |
| C‑15 complete runtime config API | Runtime config is a transactional application concern, not a checked-in-file-only concern; activation rebuilds affected schedules. |
| C‑16 encrypted write-only secrets | AES-256-GCM ciphertext persists in a mode-0600 volume file; the master key stays only in Fly secrets/environment. |
| C‑17 client-neutral versioned API | Delivery returns stable JSON DTOs and never embeds domain rules in Wails or browser code. |
