# Prior art: cloud coding agents from issue to pull request

Checked on 2026-10-27. Two passes: a survey of products and projects, then a code reading of
the three closest open runners. Full reports with quotes:
`~/Dev/knowledge/inbox/cloud-coding-agents-2026-10-27-2102.md` and
`~/Dev/knowledge/inbox/cloud-coding-agents-l3-2026-10-27-2236.md`.

## Contents

1. Verdict
2. Hosted products
3. Open runners
4. Code reading: propr, kelos, optio
5. What to take
6. Subscriptions in automation
7. Open questions

## 1. Verdict

Open runners with the same loop exist (rechecked 2026-10-28, see `spec.md` section 1).
None is worth forking: no candidate combines one small binary, label polling without a public
URL, a bot identity, both subscriptions and review fixes in the same agent session. optio resumes
both CLIs only on its local runs; AgentConnect resumes the author session but needs a webhook
and four services. famulus stays; single mechanisms are
borrowed. Confidence: medium, because `codex exec resume` has not been run live yet.

## 2. Hosted products

| Product | Trigger | Review loop | Environment |
|---|---|---|---|
| GitHub Copilot cloud agent | assign issue, `@copilot` | via `@copilot` mentions | GitHub Actions, session up to 59 min |
| Claude Code on the web | manual, Auto-fix on a PR | CI failures and review comments | cloud VM |
| Codex cloud | web, mobile, desktop | not found | cloud container |
| Jules | label `jules` | not confirmed | cloud VM, plan approved first |

They are closed and run only inside the vendor's platform. Mechanisms worth copying:

- Setup before the agent starts, then a cache: Claude setup script under 5 min, cache about
  7 days; Codex snapshots the prepared filesystem; Copilot uses `copilot-setup-steps.yml`.
- Secrets stay outside the sandbox: a proxy adds the key only for allowed hosts (Claude, Codex).
- Every commit links to the session: Copilot adds a log link, Claude adds a
  `Claude-Session: <url>` trailer.

## 3. Open runners

| Runner | License, language | Trigger | Review loop | Runtime |
|---|---|---|---|---|
| integry/propr | Apache-2.0, TS, 22 stars | label | `/fix`, new session | Docker per run, Redis |
| jonwiggins/optio | MIT, TS, 1076 stars | label `optio`, 14 trigger kinds | CI and review | K8s or own machines, Postgres, Redis |
| cyrusagents/cyrus | Apache-2.0, TS, 875 stars | issue assignment | updates in issue | worktree, own GitHub App, Linear first |
| kelos-dev/kelos | Apache-2.0, Go, 348 stars | label via webhook or polling | new Task per round | Kubernetes 1.28+ |
| openai/symphony | Apache-2.0, Elixir | polls Linear | human review, rework | workspace per task, Codex |
| symphony-go | Apache-2.0, Go, 0 stars | label `symphony:ready` | not checked | subprocess |
| mraza007/baton | MIT, Python | label polling | none, retries only | worktree, Claude only |

Dropped: open-swe (needs LangSmith), claude-code-action and codex-action (an Action per
repository), agent-orchestrator (tasks started by a person), nightshift (picks its own tasks),
claude-hub (inactive since 2025-10), Sweep (changed product).

## 4. Code reading: propr, kelos, optio

Revisions: propr `19cee49`, kelos `1485a51`, optio `f2c557d`.

| Criterion | famulus | propr | kelos | optio |
|---|---|---|---|---|
| Runtime | Docker, worktree | Docker per run, SQLite, Redis | K8s Job, cert-manager | pod per repo, Postgres, Redis |
| Polling, no public URL | 60 s, GitHub App | 60 s, needs relay or own App | 5 min | 60 s |
| Claude subscription | setup-token | mounts `~/.claude` | OAuth token from Secret | OAuth token |
| Codex subscription | login in a volume | mounts `~/.codex` | `auth.json`, refresher every 6 h | app-server only |
| Same session on review | yes, both CLIs | no | no, new Task or Session | Claude only, `CHANGES_REQUESTED` only |
| Pause on limit | yes | yes, delayed retry | no | no, shows limits |
| Reacts to CI failure | no | `/ultrafix` (docs, code not read) | not confirmed | yes, polls every 30 s |
| Size | small | about 720k lines | about 70k lines Go | API about 97k lines |

Findings:

- propr starts a new session for each run and each PR comment:
  `claude -p --no-session-persistence`, `codex exec --ephemeral`
  (`packages/core/src/agents/impl/utils/dockerArgsBuilder.ts:162-163`,
  `src/jobs/prCommentAgentUtils.ts:329-344`). It has no PAT mode
  (`packages/shared/src/githubAuthMode.ts:12`).
- optio resumes Claude with `--resume <id>` after CI failures and reviews; a merge conflict
  starts a fresh session (`apps/api/src/services/reconcile-executor.ts:405-437,553-585`). Only
  `CHANGES_REQUESTED` reviews trigger it; plain and inline comments are ignored
  (`apps/api/src/workers/pr-watcher-worker.ts:36-47`). Codex runs a fresh `codex exec`.
- kelos resumes only inside a live Session (Claude `--resume`, Codex `thread/resume` over the
  app-server, `internal/sessionruntime/codex.go:228-240`). The review loop creates a new Task
  (`cmd/kelos-spawner/main.go:340-357`).
- Each project is effectively one author; most commits in the last 90 days come from a bot.

## 5. What to take

| Feature | Priority | Donor | file:lines@rev | How | License |
|---|---|---|---|---|---|
| React to a failed CI check on the PR | high | optio | `apps/api/src/workers/pr-watcher-worker.ts:22-50,136-141@f2c557d` | adapt: poll check status, resume with the CI log | MIT |
| Aggregated PR review state | medium | kelos | `internal/source/github_pr.go:190-238,505-525@1485a51` | port `aggregatePullRequestReviewState` | Apache-2.0 |
| Merge conflict starts a fresh session | medium | optio | `apps/api/src/services/reconcile-executor.ts:553-585@f2c557d` | idea | MIT |
| Delayed retry at the limit reset time | medium | propr | `src/jobs/errorHandlers.ts:60-210@19cee49` | idea, compare with our pause | Apache-2.0 |
| Codex limits from rollout files | medium | optio | `apps/cli/src/local/codex-limits.ts:7-110@f2c557d` | adapt for the panel | MIT |
| Codex OAuth refresh | low | kelos | `internal/codexauth/refresher.go:136-245@1485a51` | port without client-go, only if the login leaves the volume | Apache-2.0 |
| Container per run with mounted logins | low | propr | `packages/core/src/agents/impl/utils/dockerArgsBuilder.ts:130-175@19cee49` | idea | Apache-2.0 |
| Network limits for the agent | low | propr | `docs/docs/features/execution-safety.md:112-148@19cee49` | idea | Apache-2.0 |
| Session link in commits | low | Claude web, Copilot | vendor docs | idea: trailer with run id and log | n/a |

Copied code keeps its Apache-2.0 or MIT notice.

## 6. Subscriptions in automation

- Anthropic: OAuth is for subscription purchasers; routing requests through Free, Pro or Max
  credentials on behalf of other users is forbidden; hosted sandboxes need the Commercial
  Terms; advertised limits assume ordinary individual use
  (https://code.claude.com/docs/en/legal-and-compliance). One owner on own repositories is
  neither allowed nor forbidden explicitly.
- `claude setup-token` gives a one-year token; `--bare` does not read it. An open report shows
  401 with such a token (issue 84903, 2026-08).
- OpenAI: API keys are the default for automation; ChatGPT login in CI is "advanced" and
  "Do not use this workflow for public or open-source repositories"
  (https://developers.openai.com/codex/noninteractive). tix is public.

## 7. Open questions

- `codex exec resume` keeps the context in a container: not run live by anyone, famulus
  included.
- Codex limit handling in propr and its `/ultrafix` code: not read.
- Whether optio's Codex app-server works on a ChatGPT subscription: not checked.
- symphony-go, openai/symphony and cyrus: not read in code.
