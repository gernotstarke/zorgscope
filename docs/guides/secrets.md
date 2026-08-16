# Secrets and tokens

zorgscope reads all secrets from environment variables (ADR‑0007). Locally they live in `.env` (git‑ignored,
template `deploy/env.example`); on fly.io set them with `make fly ARGS="secrets set NAME=value"`.

| Variable | Purpose | How to obtain / scope |
|----------|---------|-----------------------|
| `GITHUB_TOKEN` | Issues, PRs, workflow runs of monitored repos; notifications for mentions | GitHub → Settings → Developer settings → *Fine‑grained token*: resource owner `arc42` (needs org approval) and `gernotstarke`; repository permissions **Issues: read, Pull requests: read, Actions: read, Metadata: read**; account permission **Notifications: read**. If the fine‑grained token cannot cover the `arc42` org, use a classic token with `repo` (private repo access) + `notifications`. |
| `PLAUSIBLE_API_KEY` | Stats API v2 | plausible.io → Account settings → API keys → *Stats API* key. One key covers all sites of the account. |
| `TODOIST_TOKEN` | Read tasks | Todoist → Settings → Integrations → Developer → *API token*. |
| `SESSION_SECRET` | Signs/authenticates session and challenge cookies | `openssl rand -hex 32`. Rotating it logs every browser out. |
| `ENROLL_TOKEN` | Gate for enrolling the first passkey (`/enroll?token=…`) | `openssl rand -hex 24`. Rotate after use if you like; needed again only for recovery. |
| `FLY_API_TOKEN` | Deploy from CI / `make deploy` | fly.io → Tokens → *deploy token* scoped to the app; store as GitHub repository secret. |

Rules: never paste tokens into config files, issues, or commits; the app redacts known secrets from logs
(QS‑3.3) but you should not rely on that. Tokens are read‑only by construction (C‑6).

Recovery when all passkeys are lost: `make fly ARGS="secrets set ENROLL_TOKEN=$(openssl rand -hex 24)"`, then
`make fly ARGS="ssh console -C '/zorgscope reset-credentials'"`, then open `/enroll?token=…` again.
