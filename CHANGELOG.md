# Changelog

## [Unreleased]

## [0.1.0] - 2026-11-06

First release. famulus guards pull requests; the coding runner is frozen.

### Added

- Guard on repositories with `review = true`: one check `famulus` and one review per head SHA of a non-draft PR by the owner (#14, #16).
- Code checks before the model: the review waits for green CI of the head and the base; a PR that changes gate files without the label `gate-change` gets REQUEST_CHANGES with no model run (#14).
- Agent review: read-only Claude in a detached worktree of the head, against the linked issue, the PR text and `rules/REVIEW.md`; `must` findings request changes, findings in the diff become line comments (#15, #16, #19).
- `review_daily_limit` model runs per UTC day, guarded PRs on the panel (#17).
- Failures as a GitHub issue `famulus: failure` in `alert_repo`, closed on recovery; `/healthz` and the pass health on the panel (#12).
- CI: `check`, `image` and the required `gate` on every PR and push to `main` (#10).
- Coding runner (frozen): issues labeled `agent` go to OpenCode, Claude Code or Codex, the bot opens a PR and passes the owner's notes to the same session; pause on used-up plans, nudges, `daily_limit` (#1, #3, #6).

### Fixed

- Agents and git hooks in the container cannot read the App key from the runner's environment (#23).

### Known issues

- An expired or invalid App key also breaks the failure issue, so such an outage is seen only in the log and the panel.
- A git hook an agent plants in a shared clone runs with the installation token on the next push.
