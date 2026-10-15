# Handover: members (2.5.0), branch `multi-user`

Written 2026-10-12 at the end of the implementation session. This page says where the work stands,
what Gernot still has to do, and what was deliberately left open. The spec, the plan and the ADR
hold the design; this page holds only what they cannot know.

## Where the work is

| What | Where |
|------|-------|
| Branch | `multi-user` (pushed; tracks `origin/multi-user`). Renamed from `members` on 2026-10-12 |
| Pull request | [#3](https://github.com/gernotstarke/zorgscope/pull/3) against `main`. #2 was the same commits under the old branch name, closed |
| Head | `b84e7a4`, 24 commits over `origin/main` (`014fc8b`, 2.4.1) |
| Version | 2.5.0 (`internal/version/version.go`, README changelog) |
| Spec | [`docs/superpowers/specs/2026-10-12-members-design.md`](../specs/2026-10-12-members-design.md), status "implemented in 2.5.0" |
| Plan | [`docs/superpowers/plans/2026-10-12-members.md`](../plans/2026-10-12-members.md), nine tasks, all done |
| Decision | [ADR‑0019](../../decisions/0019-members-allowlist-repository-personal-scope.md) |
| Requirements | FR‑1.17, FR‑8.4, FR‑8.5 in [`04-functional-requirements.md`](../../requirements/04-functional-requirements.md); FR‑8.3 amended |
| Session ledger | `.superpowers/sdd/2026-10-12-members/progress.md` in the local checkout only (git-ignored); every review finding and ruling, with commit ranges |

The local `main` is four documentation commits ahead of `origin/main` (spec, plan, reconciliation,
`.worktrees` ignore line). All four are in the pull request. After a merge commit, `git pull` on
`main` fast-forwards; after a squash merge, reset `main` to `origin/main`.

## What was built, in one paragraph

A visitor with write access on the public repository `gernotstarke/zorgscope-members` signs in as a
*member*: the same GitHub sign-in, then a repository page where they choose up to 12 public,
non-archived repositories of `isaqb-org`, then the List, the tiles and the Radar over exactly those,
fetched with their own OAuth token. Push access on `gernotstarke/zorgscope` still makes the *owner*;
the owner's pages are unchanged. Nothing is stored server-side: the member's token and selection
travel in the encrypted session cookie (version 3, AES‑GCM, under 4 KB). Member sessions last 7 days,
the owner's 30. Sign-out revokes the member's token at GitHub. Every sign-in, page view, member fetch
and sign-out writes one log line with login and role; that is the whole monitoring.

## Verification record

* `go test ./...` green at `b84e7a4`; `make check` green except the `fly.toml` validation step, which
  needs a `fly auth login` on this machine (the session had expired). The branch does not touch
  `fly.toml`.
* A curl smoke test against the fake GitHub (`cmd/fakesources`) passed: member sign-in, repository
  page with 14 boxes, save three, tiles in three member hues and no Security tile, Radar with three
  sectors and no switch, sign-out revoking the token at the fake, log lines carrying login and role.
* Each task was reviewed on its own; a whole-branch review found two defects (a redirect loop for a
  stale member cookie after members are switched off; a livelock of the member cache when one member
  is signed in on two devices) and three lesser ones, all fixed in the last nine commits and
  re-reviewed clean.
* Not done: a by-eye check in a browser against the real GitHub with a second account. The owner's
  own account can only ever see the owner view.

## What Gernot has to do

1. **Replace the local `GITHUB_TOKEN`.** The one in `.env` (fine-grained, last written 2026-09-17) is
   refused by GitHub itself with 401 "Bad credentials", which is what `make dev` showed on 2026-10-12.
   This has nothing to do with the branch: the container that showed it ran 2.4.1, and a direct call
   to GitHub with the token fails the same way. Check
   [github.com/settings/personal-access-tokens](https://github.com/settings/personal-access-tokens)
   for the expiry, regenerate, paste into `.env`. If the Fly secret is the same token, rotate it there
   too (`fly secrets set GITHUB_TOKEN=… -a zorgscope`). The members feature needs no new scope and no
   new token.
2. **Test the branch locally.** `make dev` from this checkout (it is on `multi-user` and has the
   `.env`). Stop any running 2.4.1 container first, both bind port 8080. Sign in once more: the cookie
   format changed. The recipe for a machine without a `.env`, against the fake GitHub, is in the pull
   request body.
3. **Invite the members** on
   [zorgscope-members → Settings → Collaborators](https://github.com/gernotstarke/zorgscope-members/settings/access).
   On a personal repository a collaborator has write access, which is what admits them. Each invitee
   must accept the invitation. Do **not** add yourself: push access on `gernotstarke/zorgscope` wins
   and makes you the owner regardless.
4. **Merge and deploy.** `make deploy` after the merge. Everyone, you included, signs in once more
   because every 2.4.1 cookie is refused by 2.5.0.
5. **Optional:** `fly auth login`, then `make check` to close the one unverified step.

## Known limitations, accepted on 2026-10-12

These were raised in review and accepted as documented behaviour rather than defects. Each is
recorded with its reasoning in the ledger and, where visible to a user, in ADR‑0019 or the README.

* Removing a member on GitHub takes effect at their next sign-in; an existing session lasts up to
  7 days. Rotating the OAuth client secret ends every session at once.
* A member whose token GitHub has revoked sees "GitHub unreachable … 401" rather than a sign-in
  prompt. The same wording appears for an expired `GITHUB_TOKEN` on the owner's side (see above).
* `POST /repos` is protected against cross-site requests by `SameSite=Lax` only, the same posture as
  `/refresh` and `/settings`.
* The warm-start copy in `localStorage` is per browser, shared by whoever signs in there; `/login`
  clears it.
* Legacy GitHub logins ending in a hyphen are refused (fails closed).
* Changing `members_org` leaves old selections in place until a member saves again.
* Tokens of expired member sessions are not revoked at GitHub (nobody is signed in to revoke them).

## Open items, not built

* **`/metrics` for Fly Grafana.** Named as the follow-up to log-line monitoring; nothing started.
* **A sign-in prompt for a 401 from GitHub** instead of "GitHub unreachable", for both roles.
* Small things from the ledger, none blocking: the organisation-list mutex is held across the
  upstream fetch (members queue during an outage); the 1 MB response-body limit is tight for a
  100-repository page; the secret sweep asserts only absence, never that the error notice rendered;
  `requireSignOut` logs the clearing of an invalid cookie as `sign-out role=none`; test-coverage gaps
  listed per task in the ledger; `members_repo == auth_repo` is compared case-sensitively.

## How to pick this up with Claude

The ledger's first line names the plan file; its last line says the branch is complete at `b84e7a4`.
A new session that reads the spec, ADR‑0019 and this page has everything the implementation session
had, except the review transcripts, which the ledger summarises.
