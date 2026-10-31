## What and why

<!-- One or two sentences. Link the issue: Fixes #123 -->

## How it was tested

<!-- Commands you ran, e.g. `make test`, `make itest`, `make e2e`. Paste failures you fixed, not just "tests pass". -->

## Checklist

- [ ] `make lint` and `make test` pass
- [ ] Touched Postgres code: `make itest` passes (15-18, superuser and NOSUPERUSER admin)
- [ ] A safety invariant (docs/safety.md) is affected: its `TestInvariant_*` test covers the change
- [ ] Docs updated (README, docs/config.md, docs/agents.md) if behaviour or config changed
- [ ] Go files stay under ~200 lines, snake_case names, comments explain why
- [ ] **AI-assisted?** If an AI tool wrote a meaningful part of this, say which parts, and confirm you have read and understood every line and ran the tests yourself (see CONTRIBUTING.md)
