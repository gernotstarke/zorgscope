# Members: a personal scope for invited iSAQB people — design

Date: 2026-10-12. Status: implemented in 2.5.0. Reconciled with 2.4.1 (ADR‑0017 per-owner tokens, ADR‑0018 fixed radars) the same day.
Builds on the GitHub sign-in (ADR‑0009), the stateless reset (ADR‑0010), the per-site tiles
(`2026-09-15-per-site-tiles-design.md`), the radar (`2026-09-25-radar-design.md`) and the iSAQB group
(`2026-09-29-isaqb-group-design.md`). No database, no ticker, no inline script or style.

## 1. Goal

> Some people from the iSAQB saw my zorgscope, especially the radar, and were asking how we could
> enhance it so these members could also see their issues and PRs on a personalized radar. We would
> need a login with GitHub, and then a user-specific collection of iSAQB repos with the radar, the
> list, and the tile view. Initially upon first use people would need to configure what they see on
> the radar and on the tiles. We could limit that to a max number of let's say 12. They would not
> need the arc42 view, so the buttons to select certain groups would not be required.

## 2. Decisions taken in conversation (2026-10-12)

| # | Question | Decision |
|---|---|---|
| D1 | One deployment or two? | **One.** The existing zorgscope admits a second audience. Gernot's view does not change. A second Fly app and OAuth App were rejected as operations for no gain. |
| D2 | Who is admitted as a member? | **Write access on `gernotstarke/zorgscope-members`**, a public repository Gernot created for exactly this. Managing members is GitHub's collaborator page; nothing to deploy. Organisation membership (needs `read:org`), an allowlist in the config file and a CRUD page with a database were rejected. |
| D3 | Whose token fetches a member's repositories? | **The member's own OAuth token**, obtained at sign-in, unscoped. Each member has their own GitHub rate limit; Gernot's view keeps running on `GITHUB_TOKEN`. A pasted personal access token was rejected for usability. |
| D4 | Where does the token live? | **In the session cookie, encrypted.** Server memory is lost whenever the Machine stops. The cookie moves from signed to encrypted (§4). |
| D5 | Which repositories may be chosen? | **Public, non-archived repositories of one organisation** (`isaqb-org`, 44 today), at most **12**. Private repositories would need the `repo` scope and are out of scope. |
| D6 | Where does the selection live? | **In the session cookie.** It is the member's own preference, and it keeps ADR‑0010 intact. A new device or an expired session means choosing again. |
| D7 | What does a member see? | List, Sites, Radar, Contributors and Search over their selection. One tile and one radar sector per repository. No group switch, no Other sector, no Security tile, no Dependabot alerts. |
| D8 | Owner or member? | **Owner wins.** Push on `auth_repo` makes an owner session as today; otherwise push on the members repository makes a member session. Gernot must not add himself to the members repository; nothing about his own access changes. |
| D9 | Stolen cookies | Cannot be detected statelessly. Mitigated: the token is unscoped; **sign-out revokes the token at GitHub**; member sessions last **7 days** rather than 30; rotating the client secret ends every session. |
| D10 | Monitoring | **Log lines** — sign-in, page view, fetch and sign-out carry the login and role. A `/metrics` endpoint for Fly's Grafana is a follow-up, not part of this feature. |
| D11 | Colour | Member tiles and sectors take the **twelve plain hue keys** — the seven brand hues and the five oranges, `config.MemberHues` — in selection order. systems42's green and aim42's striped key are left out. Twelve plain hues is why 12 is the cap. |
| D12 | Email | Not collected. GitHub's profile is enough. |

## 3. Admission: owner and member

`GET /auth/callback` keeps its state check, its rate limit and its exchange. After the exchange it
asks GitHub, with the visitor's token:

1. `GET /repos/{auth_repo}` — push or admin: the visitor is an **owner**.
2. Otherwise, when `github.members_repo` is configured, `GET /repos/{members_repo}` — push or
   admin: the visitor is a **member**. The members repository is public, so an unscoped token reads
   the visitor's own permission on it exactly as it does on zorgscope (ADR‑0009). A collaborator must
   be invited with the **Write** role: Read is what everyone has on a public repository.
3. Otherwise the visitor is refused, as today. The refusal page names both repositories when members
   are configured: "This dashboard is for collaborators of gernotstarke/zorgscope or
   gernotstarke/zorgscope-members."
4. Admitted visitors only: `GET /user` for the login. A failure here refuses with "GitHub could not
   be asked who you are", so nobody is admitted without a login. The login is matched against the
   same pattern `github.owner` is.

An owner sign-in costs 2 requests, a member sign-in 3. `ports.ErrNoPermissionsBlock` keeps its
meaning on either repository. Refusals still log no login (the visitor did not choose to publish
one); an accepted sign-in now logs `login` and `role` (D10).

The `ports.AccessChecker` interface gains the repository as a parameter, so one adapter answers for
both repositories: `HasPushAccess(ctx, token, repo string) (bool, error)`. Two small interfaces join
it in `ports`: `IdentityReader.Login(ctx, token) (string, error)` and
`TokenRevoker.Revoke(ctx, token) error` (§9). `github.AccessChecker` implements all three; its
constructor loses the repository argument and gains the OAuth client id and secret (§9).

## 4. The session cookie, version 3

The cookie moves from "signed" to "encrypted and authenticated": AES‑256‑GCM with a random 12‑byte
nonce, under `key = SHA256("zorgscope-session-v3" + GITHUB_OAUTH_CLIENT_SECRET)`. The cookie value
is `base64url(nonce ‖ ciphertext ‖ tag)`. Rotating the client secret still changes the key and
therefore ends every session at once (FR‑8.3 AC4); the derivation property of ADR‑0007 and ADR‑0009
is unchanged. Every version‑2 cookie fails to decrypt and is refused, so everyone signs in once more
after the deploy — the same thing that happened at version 2.

The plaintext is compact JSON:

```json
{"exp":1760000000,"role":"member","login":"octocat","tok":"gho_…","repos":["isaqb-org/glossary"]}
```

* `exp` and `role` (`owner` | `member`) and `login` are always present.
* `tok` and `repos` are present for a member only. An owner's cookie carries no token, as today, and
  the owner's view runs on `GITHUB_TOKEN`.
* The session type gains `Role`, `Login`, `Token`, `Repos`. `decode` refuses a payload that fails
  to parse, names an unknown role, lacks a login, or — for a member — lacks a token; it also refuses
  more than `MaxMemberRepos` repositories or a repository not in `owner/name` form, so a cookie that
  somehow carries junk is a sign-in, not a 500.
* TTL: owner 30 days as today; member **7 days** (D9).
* Size: a token (about 40 bytes), a login (≤ 39) and 12 repositories (≤ 140 bytes each as GitHub
  limits them) encrypt to well under 2 KB, half a browser's 4 KB limit.

Nothing in the cookie is readable by the browser or anyone holding the cookie without the key: the
token is confidential, not merely tamper-proof. What a stolen member cookie leaks is stated plainly
in ADR‑0019: a 7‑day session and an unscoped token that can read public data and identify its
owner.

## 5. A scope per request

Every signed-in handler today reads `s.cfg.GitHub.Repos`, `s.cfg.GitHub.Sites`,
`s.cfg.GitHub.Owner` and `s.cache`. These four become a **scope** resolved from the session:

```go
type scope struct {
    GH     config.GitHub   // Repos, Sites, Owner — what siteSpecs, radarSpecs and chromeFor read
    Cache  *snapshot.Cache // where the items come from
    Member bool            // a member's view: no Security tile, no alerts, a Repositories link
    Login  string          // who is signed in, for the log and the page
}
```

* **Owner session:** `GH` is the configuration, `Cache` is the one cache main builds, `Member` is
  false. Nothing about the owner's pages changes — the same functions run on the same values.
* **Member session:** `GH.Repos` is the selection, `GH.Owner` the member's login (so the needs-you
  band means them, FR‑1.14), and `GH.Sites` one site per repository: `Name` is the part after the
  slash, `URL` is `https://github.com/<repo>`, `Repos` is that one repository, `Hue` is
  `config.MemberHues[i]` for the i‑th repository (D11), `NoAlerts` true, no tag, and `Group` is
  `member` — a family none of the fixed radars of ADR‑0018 names, so no radar is offered, every
  site is a sector and no switch is drawn (FR‑1.15); every repository is claimed, so no Other tile
  or sector. `Cache` is the member's own (§8).

`siteSpecs`, `radarSpecs`, `buildRadar`, `chromeFor`, `headerView`, the waiting page's repository
count and `domain.BuildDashboard`'s `Repos` all take the scope's values instead of the config's.
The functions themselves do not change; only what they are handed does. `handleRefresh`
invalidates the scope's cache.

A member with an **empty selection** has no scope to render: every session page except `/repos`
redirects to `/repos` (303), and a session fragment (`GET /items`) answers 204 with
`HX-Redirect: /repos`. `/settings`, `/theme`, `/logout` keep working. The owner is never redirected.

## 6. What a member sees

* **List, Search, Contributors, the top bar's counts, the needs-you band**: unchanged code over
  their items. "Needs you" means the member.
* **Sites**: one tile per chosen repository, named after the repository, linking to its GitHub page,
  in its hue; no Security tile (`sites.html` draws it only for the owner). FR‑1.8's "preceded by
  the Security tile" becomes "for the owner's scope".
* **Radar**: one sector per chosen repository in selection order; no switch, no condensed sector,
  no Other. Twelve sectors is the most a member's radar draws. The fixed radars of ADR‑0018 do
  not apply: the member family is in none of them.
* **Settings panel**: a "Repositories" link to `/repos` for members, beside the existing controls.
* **The wait page** counts the member's repositories.
* Refresh invalidates only the member's cache.
* The footer, the rainbow band, the theme and the landing preference work as for the owner.

## 7. The repository page, `/repos`

* `GET /repos` (session page). For an owner it redirects to `/` — the owner's repositories are the
  configuration's. For a member it lists the organisation's public, non-archived repositories, in
  the organisation list's order (GitHub returns them by name), each as a checkbox with the
  repository's name and its description, the chosen ones checked. A count line says "7 of 12
  chosen". "Signed in as @login" names the account, since this is the one page where it matters
  which account one is configuring. One Save button, a plain form, no JavaScript.
* `POST /repos` (session fragment auth, a plain form post). Fields: `repo`, repeated. The server
  keeps the submitted repositories that are in the organisation list, drops duplicates, orders them
  as the organisation list does, and refuses more than 12 by re-rendering the page with "Choose at
  most 12" and nothing saved. A valid selection re-mints the cookie with the same expiry, token and
  login and redirects to the member's landing view, or to `/repos` again with the panel's
  "saved" state when nothing was chosen (an empty selection is allowed and leads back here, §5).
  An owner's POST redirects to `/` and changes nothing.
* The organisation list comes from `GET /orgs/{org}/repos?type=public&per_page=100`, following
  `Link` pagination, archived repositories dropped, fetched with the member's token through a new
  `ports.RepoLister.PublicRepos(ctx, token, org) ([]ports.Repo, error)` where `Repo` is
  `{FullName, Description string}`. It is cached in memory for one hour, shared by every member: it
  is public data. When it cannot be fetched and nothing is cached, the page shows the fetch error's
  notice (scrubbed, §10) and the member's current selection as checked boxes, so they can still
  save a smaller selection or leave. A `POST` that cannot get the list refuses with the same notice
  and saves nothing.
* `MaxMemberRepos = 12` and `MemberHues` (the twelve plain keys) live in `config` beside `MaxRepos`
  and `HueKeys`, with a comment naming D11.

Because checkboxes submit in document order and the list is alphabetical, the selection — and with
it the hue assignment — is alphabetical. Adding a repository can shift the hues of those after it.
Accepted (D11).

## 8. Fetching for members

A member's items come from a `snapshot.Cache` of their own, with the fetching, waiting, partial
failure and TTL semantics of ADR‑0011 and ADR‑0013 unchanged — the package is not modified. The
web layer holds them in a `memberCaches` map keyed by login:

* An entry is `{fingerprint, cache, lastUsed}`; the fingerprint is `SHA256(token ‖ "\n" ‖ repos…)`.
  A session whose fingerprint differs from the entry's — a new token after re-signing in, or a new
  selection — replaces the entry. `Invalidate` on the old one is unnecessary: it is dropped.
* The cache's source is built by `Options.MemberSource func(token string, repos []string)
  ports.Source`, which main implements as `github.NewIssueFetcher` with that token, those
  repositories, all of them in `NoAlerts`, **no per-owner tokens** (ADR‑0017's `OwnerTokens` stay
  the owner's: a member's fetch must never carry a token that reads private repositories), and the
  same base URLs the owner's fetcher uses. No alert
  query, no REST probe: a member fetch costs at most 12 × 2 = **24 requests**, on the member's own
  limit (QS‑3.5 gains that sentence).
* The map is bounded at 64 entries; beyond that the least recently used entry is dropped, as the
  sign-in rate limiter bounds its buckets. A Machine that stops forgets them all, and the first
  page view after a wake pays one fetch, exactly as the owner's does.
* The `ttl` is `github.cache_ttl`, the same as the owner's.

The source is wrapped so that **every error it returns is scrubbed of the member's token** before
it leaves the wrapper, because the error text reaches both the log (main's `loggingSource`) and the
page (`errorNotice`). The wrapper is the one place the token could leak from a fetch, and it closes
that place for every consumer at once.

## 9. Sign-out and revocation

`POST /logout` for a member first asks GitHub to revoke the token —
`DELETE /applications/{client_id}/token` with the App's client id and secret as basic
authentication and `{"access_token": …}` as the body (`ports.TokenRevoker`) — then clears the
cookie and redirects to `/login` as today. A failed revocation is logged at Warn with the login and
no token, and the cookie is cleared regardless: the visitor asked to be signed out of this browser,
and that part never depends on GitHub. For an owner there is nothing to revoke. A stolen copy of a
member's cookie therefore dies the moment its owner signs out anywhere, and a member can also
revoke zorgscope under "Authorized OAuth Apps" in their GitHub settings.

## 10. Logging (D10)

Structured lines, all at Info unless noted, never with a token, code, state or secret (QS‑4.2,
QS‑4.3):

| Event | Fields |
|---|---|
| `sign-in accepted` | `ip`, `login`, `role` |
| `page view` | `login`, `role`, `path` — emitted by `requireSession` for `authSessionPage` routes only, so fragments and polls do not flood the log |
| `member fetch` | `login`, `repos` (count) — when a member's cache starts a fetch |
| `fetch failed` | as today, plus `login` for a member's fetch (empty for the owner's) |
| `sign-out` | `login`, `role`, `revoked` (bool); a failed revocation logs a Warn beside it |

`fly logs` answers "who used it, when" for as long as Fly keeps the lines. Counting is a
follow-up (D10).

## 11. Configuration

Two optional keys under `github`, both deployment facts (FR‑8.1):

```yaml
github:
  # Write access here admits a visitor as a member (FR-8.4): a personal scope over at most 12
  # public repositories of members_org, fetched with the member's own token. Leave both out and
  # nobody but the collaborators of auth_repo gets in.
  members_repo: gernotstarke/zorgscope-members
  members_org: isaqb-org
```

* Both or neither: one without the other is a load error naming the missing key.
* `members_repo` must be in `owner/name` form and must differ from `auth_repo`; `members_org` must
  be a GitHub login. The organisation is not verified to exist at load time: that would be a
  network call at start-up.
* `config.GitHub` gains `MembersRepo`, `MembersOrg string` and `MembersEnabled() bool`.
* The sign-in page's wording and the refusal message read these to name the repositories.

Nothing changes in the environment or the Fly secrets. The OAuth App stays scope-less.

## 12. Security

What changes, said plainly:

* FR‑8.3 AC2's "the visitor's GitHub token is used for one permission check and never stored" holds
  for the owner and is rewritten for the member: "a member's token is stored only inside the
  encrypted session cookie, is used only to fetch that member's own view and the organisation's
  repository list, and never reaches a log, a response body or an error text". The secret-canary
  tests of QS‑4.3 gain a member token: a member signs in with a canary token, views every page,
  triggers a failing fetch and signs out, and no response or log line contains it.
* The cookie is `HttpOnly`, `Secure`, `SameSite=Lax`, as today. AES‑GCM makes it confidential as
  well as authenticated; a bit-flipped cookie fails to open and is a sign-in, not a parse error.
* The token is unscoped. It reads public data and identifies the member; it cannot write, read a
  private repository or act on the member's behalf beyond that.
* The members repository is public, so its collaborator list is public. The names of the people who
  may use zorgscope are therefore public. Gernot accepts this (D2).
* `POST /repos` validates against the organisation list; a member cannot make zorgscope fetch an
  arbitrary repository, which keeps the request budget and the data within the organisation.
* Fetch errors, the organisation-list error and the revocation error are scrubbed of the member's
  token before anything else sees them (§8, §9).
* Rate limiting of sign-in is unchanged. `POST /repos` needs none: it costs at most one cached list
  and re-mints a cookie.

## 13. Requirements and documents touched

* **FR‑1.17** (new, S) — the member's scope: list, tiles, radar, contributors and search over the
  chosen repositories; one tile and one sector per repository in the 12 hues in selection order;
  no Security tile, no alerts, no group switch, no Other; the needs-you band means the member;
  an empty selection leads to `/repos`.
* **FR‑8.3** — the story reads "only a collaborator with push access, or an invited member, is
  admitted"; AC2's cookie clause becomes "an encrypted, HttpOnly, SameSite=Lax session cookie
  carrying expiry, role and login — and, for a member, their token and selection"; the "never
  stored" sentence is rewritten as in §12; AC3 names both repositories.
* **FR‑8.4** (new, M) — member admission: Write on `members_repo` admits; `GET /user` supplies the
  login; owner wins; member sessions last 7 days; sign-out revokes the token.
* **FR‑8.5** (new, M) — the repository page: the organisation's public repositories, at most 12,
  validated, kept in the cookie; the empty-selection redirect.
* **FR‑8.1** AC1 — the file also names the members repository and organisation.
* **FR‑1.8** — "preceded by the Security tile" applies to the owner's scope; a member's tiles are
  their repositories.
* **FR‑1.11** — the settings panel carries a Repositories link for a member.
* **QS‑3.5** — a sentence: a member's fetch is at most 24 requests on the member's own limit; the
  owner's budget (19 repositories, 38 requests) is unchanged.
* **QS‑4.3** — the canary set gains the member token.
* **S‑4** (new stakeholder) — invited members: a handful of iSAQB people with a personal view; they
  decide nothing about scope and hold no credential of the deployment.
* **ADR‑0019** — "Members: an allowlist by a second repository, a personal scope in an encrypted
  cookie, fetches on the member's own token" — records D1–D11 with the rejected options and the
  stolen-cookie statement.
* **docs/concepts/security-and-tokens.md** — the member token: where it lives, what it can do, how
  it ends. **docs/concepts/configuration.md** — the two keys.
* **PRODUCT.md** — Users: Gernot, plus a handful of invited members with a personal scope; "you"
  means the signed-in person. Positioning and Capabilities mention the member view.
* **README** — one paragraph on members and how to invite one; the changelog row **2.5.0**.
* `internal/version/version.go` — **2.5.0**.

## 14. Testing

* `internal/web/auth_test.go`: the v3 codec round-trips an owner and a member session; a v2 cookie,
  a bit-flipped value, a wrong key, an expired payload, an unknown role, a member without a token
  and 13 repositories are all refused; a member cookie is under 2 KB.
* Sign-in: owner admitted on `auth_repo`; member admitted on `members_repo` with role, login and
  token in the session; pull-only on both refused; `GET /user` failing refuses; members disabled in
  config means the members repository is never asked; the request count per sign-in.
* Scope: an owner request renders from the configuration and the owner cache; a member request
  renders from the selection, with tiles and sectors named and coloured as §5 says; an empty
  selection redirects every page and 204s the fragment; the owner is never redirected.
* `/repos`: the list, the checked state, the count line; saving 3 keeps 3 in list order; 13 is
  refused with the message; an unknown repository is dropped; an owner is redirected; the list
  is cached and refetched after an hour; a failing list shows the notice and the current selection.
* Member caches: two members fetch separately; a changed selection replaces the entry; the same
  session reuses it; the 65th login evicts the least recently used; a fetch error carrying the
  token comes out scrubbed.
* Sign-out: a member's token is revoked and the cookie cleared; a failed revocation still clears
  the cookie; an owner revokes nothing.
* QS‑4.3 canaries with a member token; the route-table tests pick up `/repos` automatically.
* The fake GitHub (`internal/fakesources`) gains `GET /user`, `GET /orgs/{org}/repos`,
  `DELETE /applications/{client_id}/token`, and a control that sets the permission per repository
  and the login, so `make dev` against the fake can sign in as a member.
* Contrast tests are untouched: the member hues are twelve of the existing keys.

## 15. Out of scope, named so nobody wonders

* A `/metrics` endpoint for Fly's Grafana (D10 follow-up).
* Private repositories, which need the `repo` scope or a GitHub App.
* A selection that follows a member across devices, which needs a database.
* Dependabot alerts, the Security tile and the needs-you security rules for members.
* The owner seeing the member view with his own account: the fake server is how to test it.
