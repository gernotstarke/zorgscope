# 4. Functional requirements

Epics (E‑x) with user stories (FR‑x) and acceptance criteria (AC) a test can check.
Priority: **M** must (v1), **S** should (v2), **W** won't.

"The user" is always Gernot (S‑1). Terms in *italics* are defined in the [glossary](06-glossary.md).

---

## E‑1 The dashboard

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑1.1 | M | As the user I open one page and see everything at once, so that a glance is enough. | AC1 The dashboard is one list of the open issues and pull requests of the configured repositories, a section per repository in configuration order, new items first — plus a single build-status indicator, builds having their own page (FR‑2.3 AC4). AC2 It renders from stored data only; opening it contacts no upstream service. AC3 The header carries the zorgscope logo and the time of the last successful *refresh run*. AC4 The logo shows whether a refresh is running: a quiet orbit at rest, a fast coloured one while a run is in flight, and a stopped amber one after a run has failed — never on a timer, and always with the same state said in words beside it. AC5 The list can be narrowed by repository, by kind (issue or pull request), by creation date and by a text search over title and description; the filter is carried in the URL, works without JavaScript, and never changes the new count in the tab title or the summary line. |
| FR‑1.2 | M | As the user I see immediately what arrived since my last visit. | AC1 Every item whose *first seen* time is later than the user's *last visit* time carries a `NEW` badge. AC2 Each repository section shows how many of its items are new. AC3 The browser tab title is prefixed with the total new count when it is greater than zero. |
| FR‑1.3 | M | As the user I can declare everything seen. | AC1 One "mark all seen" action sets the last-visit time to now. AC2 After it, no item carries `NEW` until something newer arrives. AC3 The action requires an authenticated session and works without JavaScript. |
| FR‑1.4 | M | As the user I notice when the data is stale or its source is failing. | AC1 The page states the age of its data. AC2 A source that failed on the last refresh shows a warning with the error text and the time of its last success. AC3 A failing source never blanks the list; the last known content stays visible. |
| FR‑1.5 | M | As the user I want the page to work in light and dark appearance. | AC1 The page follows the operating-system appearance with no flash of the wrong theme. AC2 Colour is never the only carrier of meaning. |
| FR‑1.6 | S | As the user I want the page to update while it is open. | AC1 The list refreshes its fragment by htmx polling at a configured interval, keeping the filter it was drawn with. AC2 Polling never counts as a visit for FR‑1.2. |
| FR‑1.7 | S | As the user I can stop zorgscope when I am finished with it, so that a Machine that scales to zero stops now rather than after its idle timeout. | AC1 A stop control in the header shuts the process down gracefully; it is a `POST`, requires an authenticated session, and works without JavaScript. AC2 `REFRESH_SECRET` never opens it — the scheduler's credential is not a shutdown switch. AC3 It refuses while a *refresh run* is in flight and says why; a run record abandoned by a dead process does not block it forever. AC4 The visitor is answered by this process before it goes, and told that opening the dashboard again starts it back up. AC5 The control is drawn only where it would work: to a signed-in visitor, in a deployment that can stop itself. Anywhere else there is no control, and the route says why if it is asked anyway. |

## E‑2 GitHub issues, pull requests and builds (G‑1)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑2.1 | M | As the user I see every open issue and pull request of the configured repositories. | AC1 For each configured repository all open issues and PRs are fetched with number, title, URL, author, `created_at`, `updated_at` and — for PRs — the draft flag. AC2 Repositories with more than one page of open items are fetched completely. AC3 Private repositories are included when the token grants access. |
| FR‑2.2 | M | As the user I see how old each item is and when it last moved. | AC1 Each item shows its age from `created_at` and the age of its last update. AC2 Items are sorted new first, then by last update descending. |
| FR‑2.3 | M | As the user I see whether each repository builds. | AC1 The latest completed GitHub Actions run on the default branch is shown per repository with its conclusion, workflow name and finishing time. AC2 A run in progress is shown as such next to the previous conclusion. AC3 A repository without workflows shows no build state rather than an error. AC4 The dashboard itself carries only a three-state indicator — green when every repository’s last completed run succeeded, red when at least one failed, amber otherwise — with the per-repository detail one click away, so that the front page stays about new and unhandled issues and pull requests. AC5 The details page shows each repository’s build badge from shields.io beside the stored state. The badge is fetched by the *refresh run* and stored with the build, so the page draws it from its own bytes and makes no request while it is being read; it is rendered as an image and never inlined. A run with no workflow file behind it, and a badge that did not arrive, both show no badge and say why, and neither ever fails a refresh. |
| FR‑2.4 | S | As the user I see mentions and review requests addressed to me anywhere on GitHub. | AC1 Notifications with reason `mention`, `review_requested` or `assign` for the configured login appear as items. AC2 They are deduplicated against items already present from the configured repositories. |

E‑3 (site statistics) and E‑4 (tasks) were retired on 2026-09-14; their ids are not reused.

## E‑5 Refresh and new-detection (QG‑1)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑5.1 | M | As the system I refresh all sources when an external scheduler asks me to. | AC1 `POST /api/refresh` with the correct bearer secret fetches every enabled source and stores the result. AC2 A wrong or missing secret returns 401 and fetches nothing. AC3 The response reports per source how many items were stored and which sources failed. AC4 One failing source never prevents the others from being stored. |
| FR‑5.2 | M | As the user I can trigger a refresh myself from the page. | AC1 An authenticated refresh action on the dashboard runs the same refresh. AC2 It is rejected while another refresh is in flight, rather than running twice. |
| FR‑5.3 | M | As the system I record when each item was first seen, so that "new" is well defined. | AC1 On first storage of an item its *first seen* time is the time of that refresh run. AC2 Later refreshes update the item's content but never its first-seen time. AC3 An item that disappears upstream and returns later is treated as new again. |
| FR‑5.4 | M | As the system I record every refresh run, so that the dashboard can be honest about freshness. | AC1 Each run stores its start time, duration, trigger (`cron`, `user`), per-source outcome and error text. AC2 The dashboard shows the time of the last run that succeeded for the GitHub source. |
| FR‑5.5 | M | As the operator I want a refresh interrupted mid-way to leave consistent data. | AC1 Each source commits its own transaction; a machine stopped between sources loses only the sources not yet fetched. AC2 A partial run is recorded as failed for the sources it did not reach. |

## E‑6 Notifications (G‑4)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑6.1 | S | As the user I am told in Slack when a new issue or pull request appears. | AC1 A refresh run posts one message per newly first-seen GitHub item to the configured Slack webhook. AC2 Each item is announced at most once, also across restarts and repeated runs. AC3 A Slack failure is recorded but never fails the refresh run. |
| FR‑6.2 | S | As the user I can be notified by email instead of, or in addition to, Slack. | AC1 The same notification content is deliverable by a second notifier without changing the refresh logic. |
| FR‑6.3 | W | Notifications about traffic changes. | No longer applicable: site statistics were retired on 2026-09-14 together with E‑3. |

## E‑7 Documentation in the product (G‑6)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑7.1 | M | As a reader I can read zorgscope's requirements, decisions and concepts in the running system. | AC1 `/docs` lists requirements, decisions and concepts. AC2 Each page renders the Markdown from the repository. AC3 The pages need no authentication. |
| FR‑7.2 | M | As a reader I always find my way from the dashboard to the documentation. | AC1 A footer on every page links to `/docs`. AC2 Links between documents resolve inside the rendered site. |

## E‑8 Configuration and access

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑8.1 | M | As the operator I configure what is watched in one YAML file. | AC1 The file lists GitHub repositories, the repository whose push access admits a visitor, the refresh interval and the notification settings. AC2 It contains no secret values. AC3 An invalid file aborts start-up with a message naming the offending field, rather than starting half-configured. |
| FR‑8.2 | M | As the operator I supply every secret through the environment. | AC1 GitHub token, the OAuth App's client id and secret, Slack webhook and the refresh secret come from environment variables (Fly secrets in production). AC2 A source whose secret is absent is disabled, and says so on the dashboard, instead of failing every refresh. AC3 No secret value is ever logged or rendered. |
| FR‑8.3 | M | As the user I sign in with my GitHub account, once per browser. | AC1 An unauthenticated browser request to the dashboard is redirected to a sign-in page, not answered with a bare 401. AC2 Signing in with GitHub through the registered OAuth App, with no scopes requested, issues a signed, HttpOnly, SameSite=Lax session cookie; the visitor's GitHub token is used for one permission check and never stored. AC3 Only a GitHub user with push or admin permission on the configured repository is admitted; anyone else is refused with a page that says so and receives no session. AC4 The cookie's signing key derives from the OAuth client secret, so rotating the secret invalidates every session. AC5 Refused sign-ins are rate-limited and logged without the code, the state or the token. |
| FR‑8.4 | S | As the operator I edit the configuration in the browser instead of in the file. | AC1 A configuration page reads and writes the same settings; secrets remain environment-only and are shown only as configured or missing. |

## E‑9 Operations (G‑5)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑9.1 | M | As the operator I run the whole system locally with Docker and make. | AC1 `make backend` starts the backend and a local libsql-server against `.env`. AC2 `make client` opens the browser at the local backend and reports clearly when nothing answers. AC3 `make check` needs only Docker and make and runs what CI runs. |
| FR‑9.2 | M | As the operator I develop without touching the real upstream services. | AC1 A fake-sources server serves GitHub responses — issues, pull requests, workflow runs, and the OAuth endpoints a sign-in needs — from fixtures. AC2 Pointing the API base URLs at it produces a fully populated dashboard. |
| FR‑9.3 | M | As the operator I deploy from CI, not from my machine. | AC1 `make check` validates `deploy/fly.toml` before a push reaches CI, so a broken deployment configuration fails on the operator's machine rather than halfway through a release; deploying itself is CI's job (FR‑9.5 AC2), and there is no deploy target to run by hand. |
| FR‑9.4 | M | As the operator I see structured logs and a health endpoint. | AC1 Logs are JSON via `log/slog`, level configurable, secrets redacted. AC2 `GET /healthz` answers without authentication and without touching upstreams. |
| FR‑9.5 | M | As the operator I want CI to check every push. | AC1 GitHub Actions runs lint, tests and the documentation lint. AC2 A push to `main` deploys to Fly. |

## Explicitly out of scope

News and RSS feeds, "unanswered" detection, per-item dismissals, daily snapshots, credential and TLS
expiry watching, passkey authentication, a native or Wails client, and a write-capable public JSON API.
Some of these existed in the previous requirement set and were removed on 2026-08-17.

Site statistics (Plausible) and task lists (Todoist), removed 2026-09-14 after a month of not being
looked at.
