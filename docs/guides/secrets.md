# Secrets and tokens

Deployment secrets come from environment variables (ADR‑0014). GitHub/Plausible credentials can use an
environment fallback, but their normal production lifecycle is the write-only config API. Locally,
deployment values live in `.env` (git-ignored, template `deploy/env.example`).

| Variable | Purpose | How to obtain / scope |
|----------|---------|-----------------------|
| `GITHUB_TOKEN` (optional fallback) / `github_token` API secret | Issues, PRs, Actions and notifications | GitHub fine-grained token with Issues, Pull requests, Actions and Metadata read plus Notifications read; use classic `repo` + `notifications` only when the fine-grained token cannot cover all required owners. |
| `PLAUSIBLE_API_KEY` (optional fallback) / `plausible_api_key` API secret | Stats API v2 | plausible.io account settings → API keys → Stats API. |
| `ZORGSCOPE_API_TOKEN` | Bootstrap bearer authentication for every `/api/v1/*` route | At least 32 high-entropy characters, stored in the future macOS client's Keychain/password manager. |
| `ZORGSCOPE_CONFIG_KEY` | AES-256-GCM master key for API-managed provider secrets | Base64 encoding of exactly 32 random bytes, e.g. `openssl rand -base64 32`; keep a separate backup. |
| `SESSION_SECRET`, `ENROLL_TOKEN` | Reserved for the future passkey/browser-session mode | Not used by production `AUTH_MODE=token`; generate before enabling passkey mode. |
| `FLY_API_TOKEN` | Deploy from CI / `make deploy` | fly.io → Tokens → *deploy token* scoped to the app; store as GitHub repository secret. |

Rules: never paste tokens into YAML, issues or commits. Config GET returns status only for the two provider
secrets; runtime encryption is not a substitute for least-privilege upstream scopes. Rotating
`ZORGSCOPE_CONFIG_KEY` requires re-encrypting or resetting managed provider secrets first.
