# 4. Functional requirements

Epics (E‑x) with user stories (FR‑x) and acceptance criteria (AC) a test can check.
Priority: **M** must (v1), **S** should (v2), **W** won't.

"The user" is always Gernot (S‑1). Terms in *italics* are defined in the [glossary](06-glossary.md).

---

## E‑1 The dashboard

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑1.1 | M | As the user I open one page and see everything at once, so that a glance is enough. | AC1 The dashboard is a grid of *tiles*: GitHub items, site statistics, tasks — plus a single build-status indicator, builds having their own page (FR‑2.3 AC4). AC2 It renders from stored data only; opening it contacts no upstream service. AC3 The header carries the zorgscope logo and the time of the last successful *refresh run*. AC4 The logo shows whether a refresh is running: a quiet orbit at rest, a fast coloured one while a run is in flight, and a stopped amber one after a run has failed — never on a timer, and always with the same state said in words beside it. |
| FR‑1.2 | M | As the user I see immediately what arrived since my last visit. | AC1 Every item whose *first seen* time is later than the user's *last visit* time carries a `NEW` badge. AC2 Each tile shows how many of its items are new. AC3 The browser tab title is prefixed with the total new count when it is greater than zero. |
| FR‑1.3 | M | As the user I can declare everything seen. | AC1 One "mark all seen" action sets the last-visit time to now. AC2 After it, no item carries `NEW` until something newer arrives. AC3 The action requires an authenticated session and works without JavaScript. |
| FR‑1.4 | M | As the user I notice when a tile's data is stale or its source is failing. | AC1 Each tile states the age of its data. AC2 A source that failed on the last refresh shows a warning with the error text and the time of its last success. AC3 A failing source never blanks the tile; the last known content stays visible. |
| FR‑1.5 | M | As the user I want the page to work in light and dark appearance. | AC1 The page follows the operating-system appearance with no flash of the wrong theme. AC2 Colour is never the only carrier of meaning. |
| FR‑1.6 | S | As the user I want the page to update while it is open. | AC1 Tiles refresh their fragment by htmx polling at a configured interval. AC2 Polling never counts as a visit for FR‑1.2. |
| FR‑1.7 | S | As the user I can stop zorgscope when I am finished with it, so that a Machine that scales to zero stops now rather than after its idle timeout. | AC1 A stop control in the header shuts the process down gracefully; it is a `POST`, requires an authenticated session, and works without JavaScript. AC2 `REFRESH_SECRET` never opens it — the scheduler's credential is not a shutdown switch. AC3 It refuses while a *refresh run* is in flight and says why; a run record abandoned by a dead process does not block it forever. AC4 The visitor is answered by this process before it goes, and told that opening the dashboard again starts it back up. AC5 The control is drawn only where it would work: to a signed-in visitor, in a deployment that can stop itself. Anywhere else there is no control, and the route says why if it is asked anyway. |

## E‑2 GitHub issues, pull requests and builds (G‑1)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑2.1 | M | As the user I see every open issue and pull request of the configured repositories. | AC1 For each configured repository all open issues and PRs are fetched with number, title, URL, author, `created_at`, `updated_at` and — for PRs — the draft flag. AC2 Repositories with more than one page of open items are fetched completely. AC3 Private repositories are included when the token grants access. |
| FR‑2.2 | M | As the user I see how old each item is and when it last moved. | AC1 Each item shows its age from `created_at` and the age of its last update. AC2 Items are sorted new first, then by last update descending. |
| FR‑2.3 | M | As the user I see whether each repository builds. | AC1 The latest completed GitHub Actions run on the default branch is shown per repository with its conclusion, workflow name and finishing time. AC2 A run in progress is shown as such next to the previous conclusion. AC3 A repository without workflows shows no build state rather than an error. AC4 The dashboard itself carries only a three-state indicator — green when every repository’s last completed run succeeded, red when at least one failed, amber otherwise — with the per-repository detail one click away, so that the front page stays about new and unhandled issues and pull requests. AC5 The details page shows each repository’s build badge from shields.io beside the stored state, and says so — the badge is fetched by the browser when the page is read. A run with no workflow file behind it offers no badge and says why. |
| FR‑2.4 | S | As the user I see mentions and review requests addressed to me anywhere on GitHub. | AC1 Notifications with reason `mention`, `review_requested` or `assign` for the configured login appear as items. AC2 They are deduplicated against items already present from the configured repositories. |

## E‑3 Site statistics (G‑2)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑3.1 | M | As the user I see how the configured sites are visited. | AC1 Per site: visitors and pageviews for the last 7 and the last 30 days. AC2 Each figure carries its change against the preceding period of equal length, as a percentage with direction. AC3 Sites are listed in configuration order. |
| FR‑3.2 | W | Top pages, sparklines, per-source breakdowns. | Deliberately out of scope; Plausible's own dashboard is one click away. |

## E‑4 Tasks (G‑3)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑4.1 | M | As the user I see the Todoist tasks that need attention today. | AC1 Tasks that are overdue or due today are shown with content, project, due date and priority. AC2 Overdue tasks are distinguished from those due today and listed first. AC3 Tasks due later, and tasks without a due date, are not shown. |
| FR‑4.2 | W | Completing or rescheduling tasks from zorgscope. | Out of scope; zorgscope only reads. |

## E‑5 Refresh and new-detection (QG‑1)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑5.1 | M | As the system I refresh all sources when an external scheduler asks me to. | AC1 `POST /api/refresh` with the correct bearer secret fetches every enabled source and stores the result. AC2 A wrong or missing secret returns 401 and fetches nothing. AC3 The response reports per source how many items were stored and which sources failed. AC4 One failing source never prevents the others from being stored. |
| FR‑5.2 | M | As the user I can trigger a refresh myself from the page. | AC1 An authenticated refresh action on the dashboard runs the same refresh. AC2 It is rejected while another refresh is in flight, rather than running twice. |
| FR‑5.3 | M | As the system I record when each item was first seen, so that "new" is well defined. | AC1 On first storage of an item its *first seen* time is the time of that refresh run. AC2 Later refreshes update the item's content but never its first-seen time. AC3 An item that disappears upstream and returns later is treated as new again. |
| FR‑5.4 | M | As the system I record every refresh run, so that the dashboard can be honest about freshness. | AC1 Each run stores its start time, duration, trigger (`cron`, `user`), per-source outcome and error text. AC2 The dashboard shows the time of the last run that succeeded for the tile's source. |
| FR‑5.5 | M | As the operator I want a refresh interrupted mid-way to leave consistent data. | AC1 Each source commits its own transaction; a machine stopped between sources loses only the sources not yet fetched. AC2 A partial run is recorded as failed for the sources it did not reach. |

## E‑6 Notifications (G‑4)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑6.1 | S | As the user I am told in Slack when a new issue or pull request appears. | AC1 A refresh run posts one message per newly first-seen GitHub item to the configured Slack webhook. AC2 Each item is announced at most once, also across restarts and repeated runs. AC3 A Slack failure is recorded but never fails the refresh run. |
| FR‑6.2 | S | As the user I can be notified by email instead of, or in addition to, Slack. | AC1 The same notification content is deliverable by a second notifier without changing the refresh logic. |
| FR‑6.3 | W | Notifications about traffic changes. | Deferred; revisit once the statistics have been watched for a while. |

## E‑7 Documentation in the product (G‑6)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑7.1 | M | As a reader I can read zorgscope's requirements, decisions and concepts in the running system. | AC1 `/docs` lists requirements, decisions and concepts. AC2 Each page renders the Markdown from the repository. AC3 The pages need no authentication. |
| FR‑7.2 | M | As a reader I always find my way from the dashboard to the documentation. | AC1 A footer on every page links to `/docs`. AC2 Links between documents resolve inside the rendered site. |

## E‑8 Configuration and access

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑8.1 | M | As the operator I configure what is watched in one YAML file. | AC1 The file lists GitHub repositories and login, Plausible sites, Todoist filter, the refresh interval and the notification settings. AC2 It contains no secret values. AC3 An invalid file aborts start-up with a message naming the offending field, rather than starting half-configured. |
| FR‑8.2 | M | As the operator I supply every secret through the environment. | AC1 GitHub token, Plausible key, Todoist token, Slack webhook, the refresh secret and the session token come from environment variables (Fly secrets in production). AC2 A source whose secret is absent is disabled, and says so on the dashboard, instead of failing every refresh. AC3 No secret value is ever logged or rendered. |
| FR‑8.3 | M | As the user I sign in once per browser. | AC1 An unauthenticated browser request to the dashboard is redirected to a sign-in page, not answered with a bare 401. AC2 Submitting the configured token issues a signed, HttpOnly, SameSite=Lax session cookie; the token itself is never stored in the browser. AC3 The cookie's signing key derives from the token, so changing the token invalidates every session. AC4 Failed sign-ins are rate-limited and logged without the submitted value. |
| FR‑8.4 | S | As the operator I edit the configuration in the browser instead of in the file. | AC1 A configuration page reads and writes the same settings; secrets remain environment-only and are shown only as configured or missing. |

## E‑9 Operations (G‑5)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑9.1 | M | As the operator I run the whole system locally with Docker and make. | AC1 `make backend` starts the backend and a local libsql-server against `.env`. AC2 `make client` opens the browser at the local backend and reports clearly when nothing answers. AC3 `make test`, `make lint`, `make check` need only Docker and make. |
| FR‑9.2 | M | As the operator I develop without touching the real upstream services. | AC1 A fake-sources server serves GitHub, Plausible and Todoist responses from fixtures. AC2 Pointing the API base URLs at it produces a fully populated dashboard. |
| FR‑9.3 | M | As the operator I deploy and inspect production from make. | AC1 `make fly-deploy`, `fly-status`, `fly-logs`, `fly-secrets`, `fly-ssh` work through the containerised flyctl. AC2 `make db-shell` opens a SQL shell against the local database and `make db-migrate` applies migrations. |
| FR‑9.4 | M | As the operator I see structured logs and a health endpoint. | AC1 Logs are JSON via `log/slog`, level configurable, secrets redacted. AC2 `GET /healthz` answers without authentication and without touching upstreams. |
| FR‑9.5 | M | As the operator I want CI to check every push. | AC1 GitHub Actions runs lint, tests and the documentation lint. AC2 A push to `main` deploys to Fly. |

## Explicitly out of scope

News and RSS feeds, "unanswered" detection, per-item dismissals, daily snapshots, credential and TLS
expiry watching, passkey authentication, a native or Wails client, and a write-capable public JSON API.
Some of these existed in the previous requirement set and were removed on 2026-08-17.
