# CLAUDE.md

Start by reading `docs/2026-10-01-02-agent-handoff.md`. It says what this repo is, what is proven, and what to do next.

Rules:
- Work on a branch and open a PR. Never push to `main`. Make sure the PR is mergeable.
- New plan/spec docs go in `docs/` named `YYYY-MM-DD-NN-slug.md`, with the file's own name on the first line.
- Build everything with `CGO_ENABLED=0`. That's the whole point.
