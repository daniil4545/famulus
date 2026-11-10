# famulus

A guard for your pull requests. A GitHub App bot checks every open PR from outside it:
CI of the head and the base, CI files the PR changes, then a read-only Claude review against
the linked issue and the project rules. It posts one check `famulus` and one review per head
SHA and never merges. Runs locally in Docker on your own subscription.

## Stage

0.1.0 for one owner. The guard is in use; the coding runner below works but is frozen.
Design and scenarios: `docs/spec.md`.

## Guard

Set `review = true` on a repository in `famulus.toml`. The check `famulus` on a PR shows:

| Check | Meaning | Your move |
|---|---|---|
| Waiting for CI | CI of the head runs | none |
| Waiting for green CI / `main` is red | CI of the head or of the base failed | fix CI |
| CI did not run | no CI check started on the commit in two hours | rerun CI or push a new commit |
| Gate files changed | the PR changes workflows, linter configs or `Makefile` | read it and merge it by hand |
| Approved / Changes requested | the agent review; findings are line comments | fix the `must` findings |
| Agent review paused | the Claude plan is used up | none, it resumes itself |
| Review failed / Publishing the review failed | a bad answer twice, or GitHub refused the review | push a new commit to retry |
| Daily review limit reached | `review_daily_limit` model runs done today | none, it runs the next UTC day |

## Coding runner (frozen)

1. You open an issue and add the label `agent` (or `agent:claude`, `agent:codex` next to it).
2. Within a minute the runner checks out `agent/<N>-<slug>` and starts the agent.
3. The agent commits; the runner pushes and opens a PR that `Closes #N`.
4. You comment or review the PR. The runner passes your notes to the same session; the agent
   pushes fixes and replies.
5. If the issue lacks something, the agent asks on the issue (`agent:blocked`) and continues
   after your answer.

Issue labels show the state:

| Label | Meaning | Your move |
|---|---|---|
| `agent` alone | queued; at most 5 new issues start per UTC day (`daily_limit`) | none |
| `agent:working` | the agent runs | none |
| `agent:pr` | PR open | review it |
| `agent:blocked` | the agent asked a question on the issue | answer in a comment |
| `agent:paused` | the agent's plan or free tier ran out; retry time is in a comment | none, it retries itself |
| `agent:failed` | the run broke; the cause is in a comment | remove the label to retry |
| `agent:done` | PR merged or closed | none |

## Quick start

Requirements: Docker, a GitHub App, `envkey` or another way to pass environment variables.

1. Create a GitHub App: permissions Contents, Issues, Pull requests and Checks read and write,
   Commit statuses and Metadata read; webhook off. Generate a private key and install the App on the
   repositories to watch.
2. Put its id and key into `.env`:
   `envkey set --public FAMULUS_APP_ID <id>` and
   `base64 -i <key>.pem | envkey set FAMULUS_APP_KEY_B64`.
3. `cp famulus.example.toml famulus.toml` and list your repositories.
4. `envkey run -- docker compose up -d --build`
5. Open http://127.0.0.1:8080: the admin panel shows the last pass, tasks with their state
   and PR, paused agents and the latest log lines. It refreshes every 10 seconds.

Optional logins:

- Claude Code: `claude setup-token`, then `envkey set CLAUDE_CODE_OAUTH_TOKEN`.
- Codex: `docker compose exec famulus codex login --device-auth` once; the login stays in
  the `home` volume.
- OpenCode free models need no login.

Tests and linters: `make check` (needs Go, git and golangci-lint).

## Configuration

| Variable | Required | Meaning |
|---|---|---|
| `FAMULUS_APP_ID` | yes | GitHub App id |
| `FAMULUS_APP_KEY_B64` | yes | base64 of the App private key PEM |
| `CLAUDE_CODE_OAUTH_TOKEN` | for Claude | subscription token from `claude setup-token` |

Everything else is in `famulus.toml`, see `famulus.example.toml`.

## Documents

| Question | Where |
|---|---|
| model, states, scenarios, decisions | `docs/spec.md` |
| rules every agent run starts with | `rules/AGENTS.md` |
| review checklist and answer format | `rules/REVIEW.md` |
| owner's design rules in every review | `rules/PRINCIPLES.md` |
| rules for agents developing famulus | `AGENTS.md` |
| what changed | `git log` |
