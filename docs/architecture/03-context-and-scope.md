# 3. Context and scope

Business context, external interfaces (EXT‑1 … EXT‑7) and the context diagram are maintained in
[requirements ch. 3](../requirements/03-scope-and-context.md).

## Technical context

| Neighbour | Channel | zorgscope side |
|-----------|---------|----------------|
| Browser | HTTPS (fly edge terminates TLS, forwards HTTP to the container port 8080) | `internal/server` |
| GitHub | HTTPS to `api.github.com/graphql` and `/notifications` | `internal/adapters/github` |
| Plausible | HTTPS to `plausible.io/api/v2/query` | `internal/adapters/plausible` |
| Todoist | HTTPS to `api.todoist.com` | `internal/adapters/todoist` |
| Feeds | HTTPS/HTTP GET to configured URLs | `internal/adapters/feed` |
| Watched URLs | HTTPS HEAD/GET, TLS handshake for cert expiry | `internal/adapters/watch` |
| SQLite file | local file on volume `/data/zorgscope.db` | `internal/adapters/sqlite` |
| Environment | env vars, `config/zorgscope.yaml` | `internal/config` |
| fly.io | health checks on `/healthz`, `/readyz` | `internal/server` |
