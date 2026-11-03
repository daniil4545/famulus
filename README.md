# famulus

Label a GitHub issue `agent`, get a pull request from a coding agent, and review it like a
colleague's: your comments go back to the same agent session. Runs locally in Docker with
OpenCode, Claude Code or Codex on your own subscriptions.

## Stage

MVP for one owner. Polling, the three agents, questions on the issue, the review loop and a
GitHub App bot work; the owner merges by hand. Design and scenarios: `docs/spec.md`.

## How it works

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

1. Create a GitHub App: permissions Contents, Issues and Pull requests read and write,
   Metadata read; webhook off. Generate a private key and install the App on the
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

Upgrading from agentyard: rename the variables (`envkey mv AGENTYARD_APP_ID FAMULUS_APP_ID`, the
same for `AGENTYARD_APP_KEY_B64`) and `agentyard.toml` to `famulus.toml`, then copy the volumes
before the first start, or the runner restarts every queued issue:
`docker run --rm -v agentyard_work:/from:ro -v famulus_work:/to alpine cp -a /from/. /to/`,
and the same for `home`.

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
| rules for agents developing famulus | `AGENTS.md` |
| what changed | `git log` |
