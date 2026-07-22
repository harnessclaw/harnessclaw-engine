# Contributing to HarnessClaw Engine

Thanks for taking the time to contribute. This guide covers local setup, the
checks to run before review, and the conventions this repository follows.

## Prerequisites

- **Go 1.26+** — the module targets `go 1.26.1`
- _(optional)_ [golangci-lint](https://golangci-lint.run/) — needed for `make lint`
- _(optional)_ [ripgrep](https://github.com/BurntSushi/ripgrep) — runtime dependency of the built-in Grep tool

## Local setup

```bash
git clone https://github.com/harnessclaw/harnessclaw-engine.git
cd harnessclaw-engine

make build        # builds ./dist/harnessclaw-engine
make run          # runs with configs/config.yaml
```

`make run` frees the configured ports and prepares the runtime directory first,
so prefer it over invoking the binary by hand during development.

## Before opening a pull request

Run the same checks a reviewer will:

```bash
make fmt          # format
make tidy         # tidy go.mod
make lint         # golangci-lint
make test         # go test ./... -race -count=1
make vuln         # vulnerability scan
```

Integration tests talk to a real LLM API, so they sit behind a build tag and are
not part of `make test`. Run them when your change touches the query loop, a
provider, or the server bootstrap:

```bash
go test -tags=integration ./cmd/server/ -v
go test -tags=integration ./internal/provider/bifrost/ -v
```

A coverage report is available via `make test-cover`, which writes `coverage.html`.

## Architecture constraints

Dependencies flow in one direction:

```text
Channel -> Router -> Engine -> Provider / Tool
```

Keep it that way. A change that makes a lower layer import an upper one (for
example a provider reaching back into a channel) will be sent back, even if it
compiles - the acyclic layering is what keeps the query loop testable.

Common extension points:

| You want to... | Look at |
|---|---|
| Add a built-in tool | `internal/tools/` - implement the `Tool` interface in `tool.go`, then register it in `registry.go` |
| Add or change a skill | Skills are plain `SKILL.md` files; loading, frontmatter parsing and argument substitution live in `internal/skills/` |
| Change permission behaviour | `internal/tools/permission.go` and the permission pipeline |
| Add an LLM provider | `internal/provider/` |

Skills are read from the directories listed under the `skills` config key, in
order, with earlier directories winning on name conflicts. When that list is
empty the engine falls back to `~/.harnessclaw/workspace/skills/`.

## Commit messages

This repository uses [Conventional Commits](https://www.conventionalcommits.org/).
The authoritative rules - including changelog and release requirements - live in
[docs/release-rules.md](docs/release-rules.md). The essentials:

- Prefixes: `feat` / `fix` / `refactor` / `docs` / `chore` / `build` / `ci` / `test`, each with a scope, e.g. `fix(ws): ...`
- Imperative mood, short, no trailing period
- One kind of change per commit; do not mix unrelated work
- **Never add a `Co-Authored-By` trailer, including AI-generated attribution**
- If you notice a leaked credential in a diff, stop and flag it rather than committing

Branch names follow the same shape as the commit type, e.g. `feat/multi-agent-rework`
or `fix/cancel-and-content-fixes`.

## Changelog

Update `CHANGELOG.md` when your change:

- adds a user-visible capability,
- fixes something a user can notice,
- changes existing interaction, configuration or release behaviour, or
- affects install, update, packaging or upgrade.

Pure refactors with no behavioural change, test-only adjustments and internal
script reshuffles usually do not need an entry. When in doubt, add one. The file
follows [Keep a Changelog](https://keepachangelog.com/) and is written in English.

## Reporting bugs and asking questions

- 🐛 [Issues](https://github.com/harnessclaw/harnessclaw-engine/issues) - bugs and feature requests
- 💬 [Discussions](https://github.com/harnessclaw/harnessclaw-engine/discussions) - questions and ideas
- 👾 [Discord](https://discord.gg/SeseGE7ZUH)

For bug reports, include the engine version or commit, your Go version, the
relevant part of `configs/config.yaml` (with secrets removed) and the logs around
the failure.

## License

By contributing you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE).