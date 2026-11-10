# Principles of the owner's projects

Every project of this owner follows these rules. A project's own docs (spec, architecture,
roadmap) add to them and win on conflict. A `must` about design names the rule it breaks,
like `P3`, and the place in the diff.

P1. Simplest working. Before new code, in this order: is it needed at all, is it in the
    codebase, the stdlib, the platform, the dependencies, would one line do. Own code last.
    Not: a hand-written parser for a format the stdlib reads.

P2. Nothing in reserve. Functions, interfaces, options, layers and abstractions appear for a
    real second implementation or a test of an outer boundary, not ahead.
    Not: an interface with one implementation "for later"; a config key nobody sets.

P3. One process. A new service, daemon, broker, cache or database needs a measured problem
    with numbers. PostgreSQL is the database and the queue until then.
    Not: a queue for work one goroutine handles.

P4. Only the task. The change touches what its purpose needs: no refactor on the side, no
    copy of an existing helper. A new external dependency needs the owner's consent in the
    issue. Not: a rename across a neighbour module inside a bug fix.

P5. Clean code by the language standard (Effective Go, PEP 8). One simple model of state and
    flow; cases it explains get no own mechanism. A file name is its area; packages by
    domain, not by layer. A wrapper that only passes a call on is not needed. A duplicate is
    cheaper than a wrong abstraction. What a linter checks is not a finding here.

P6. Borders stay strict. Simplicity never cuts security, validation at trust boundaries or
    data-loss handling. External data is parsed into types once at the entry. An outward
    action is idempotent. Deterministic code decides; an LLM gives signals and text. An
    error is handled or returned with context, never swallowed or replaced by a silent default.
