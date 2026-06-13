# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Console monitor for Claude usage limits backed by the OAuth usage endpoint.
- Output modes: `compact` (default), `table`, `json`, and `oneline`.
- Interactive `tui` dashboard (Bubble Tea) with manual refresh and quit keys.
- Configurable poll interval (`--interval`, default `1m`) with a legacy
  positional `interval-seconds` form, plus `--once` for a single snapshot.
- OAuth token resolution from `CLAUDE_CODE_OAUTH_TOKEN`, the macOS Keychain, and
  `~/.claude/.credentials.json`.
- Plan classification (Free, Pro, Max 5x/20x, Team, Enterprise) with adaptive
  rendering of whichever windows the endpoint returns.
- Exponential backoff on `HTTP 429`, and a warning when the interval is below
  the recommended floor.
- `--debug` diagnostics, `version` subcommand, and shell completions via Cobra.
- Full Go project packaging: Makefile, golangci-lint config, GitHub Actions CI,
  and a GoReleaser configuration (Homebrew formula prepared but not yet
  published).

[Unreleased]: https://github.com/merlindeep/claude-cost-viewer/commits/main
