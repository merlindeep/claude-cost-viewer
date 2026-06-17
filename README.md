# ccview

`ccview` is a compact console monitor for **Claude usage limits**. It shows the
same utilization you see in the `/usage` view of Claude Code and in the desktop
app's "Your usage limits" panel — straight from your terminal, refreshed on an
interval.

```text
Claude usage  Max 20x
  Session   ████████░░░░░░░░░░░░  42%   resets in 2h 22m
  Weekly    ███░░░░░░░░░░░░░░░░░  13%   resets Sat 00:00
    opus    ██████████████████░░  90%   resets Sat 00:00
  Extra     ███░░░░░░░░░░░░░░░░░  16%   $3.20 / $20.00

updated 12:00:00 · every 1m0s · Ctrl+C to quit
```

It is intentionally small, dependency-light at its core, and fully tested.

---

## Table of contents

- [What it shows](#what-it-shows)
- [How costs are fetched](#how-costs-are-fetched)
- [Requirements](#requirements)
- [Installation](#installation)
- [Usage](#usage)
- [Output modes](#output-modes)
- [Configuration](#configuration)
- [Rate limiting](#rate-limiting)
- [Project layout](#project-layout)
- [Development](#development)
- [Disclaimer](#disclaimer)
- [License](#license)

---

## What it shows

Claude's usage endpoint reports a different set of windows depending on your
subscription plan and how the account is connected. `ccview` renders whatever
the endpoint returns, so the output adapts automatically:

| Window      | Meaning                                   | Typical plans                |
| ----------- | ----------------------------------------- | ---------------------------- |
| **Session** | Rolling 5-hour window                     | Free, Pro, Max, Team         |
| **Weekly**  | 7-day window                              | Pro, Max, Team               |
| **Sonnet**  | Per-model 7-day window (shown when used)  | Max                          |
| **Opus**    | Per-model 7-day window (shown when used)  | Max                          |
| **Extra**   | Pay-as-you-go extra-usage credits         | Plans with extra usage on    |

Each row shows a colour-coded bar (green `< 50%`, yellow `< 85%`, red `>= 85%`),
the utilization percentage, and when the window resets. Per-model windows are
hidden when they are at 0% unless you pass `--all`.

## How costs are fetched

The data comes from the same undocumented endpoint the official client uses:

```
GET https://api.anthropic.com/api/oauth/usage
```

This endpoint is the source of truth for **utilization percentages** — it is
what powers `/usage` in Claude Code. (Tools that read local logs can only sum
tokens or estimated dollar cost; they do not know your plan's limits.)

The request must look like Claude Code or the endpoint returns `HTTP 429`
immediately. `ccview` therefore sends:

- `Authorization: Bearer <oauth-token>`
- `anthropic-beta: oauth-2025-04-20`
- `User-Agent: claude-code/<version>` (auto-detected, see [Configuration](#configuration))

The OAuth token is **read, never written**, from the following sources in
priority order:

1. the `CLAUDE_CODE_OAUTH_TOKEN` environment variable (a bare token);
2. the macOS Keychain entry `Claude Code-credentials`;
3. `~/.claude/.credentials.json`.

If you have run Claude Code at least once, the token is already in place.

If a token expires mid-session, `ccview` does not stop or print a raw error: it
re-reads the chain above — picking up the token Claude Code refreshes in the
background — and retries once. Only if that still fails does it print a short,
actionable message asking you to refresh the token and try again.

By default `ccview` never refreshes the token itself; it relies on Claude Code
to do so. If you would rather it nudge Claude Code automatically, pass
`--auto-reload-expired-token`: when the stored token has expired, `ccview` runs
Claude Code once (`claude -p --model haiku …` by default) to renew it, then
re-reads the refreshed token and continues. This spends a little quota, so it is
opt-in and limited to **at most one attempt every five minutes**, and it works
in every watch mode, including the TUI. Override the command it runs with
`CCVIEW_RELOAD_CMD` (for example, a script that performs a quota-free refresh).
`ccview` still never reads or writes the token itself — it only invokes the
helper and re-reads the credential chain above.

## Requirements

`ccview` needs an OAuth token issued to **Claude Code (the CLI)**. The simplest
way to get one is to install Claude Code and sign in once:

```sh
brew install --cask claude-code   # or: npm i -g @anthropic-ai/claude-code
claude                            # complete the login once
ccview                            # the token is now found
```

Claude Code and the desktop app share the same subscription, so the usage
numbers match.

> **The Claude desktop app on its own is not enough.** It signs in as an
> encrypted web session (Electron `safeStorage`), not as a reusable OAuth bearer
> token that `ccview` can read — so on a machine with only the desktop app
> installed, no token is found. Install the Claude Code CLI (above), or export a
> token into `CLAUDE_CODE_OAUTH_TOKEN`.

Not sure which source is being picked up? Run `ccview --debug`: it prints a
per-source breakdown (environment, Keychain, credentials file) so you can see
exactly what was found and what was missing.

## Installation

### Homebrew (macOS and Linux)

```sh
brew install merlindeep/tap/ccview
```

This taps `merlindeep/homebrew-tap` and installs the latest release; upgrade
later with `brew upgrade ccview`.

### Pre-built binaries

Download an archive for your platform from the
[Releases](https://github.com/merlindeep/claude-cost-viewer/releases) page,
extract it, and move `ccview` onto your `PATH`.

### With `go install` (Go 1.24+)

```sh
go install github.com/merlindeep/claude-cost-viewer/cmd/ccview@latest
```

This puts a `ccview` binary in `$(go env GOPATH)/bin`.

### From source

```sh
git clone https://github.com/merlindeep/claude-cost-viewer.git
cd claude-cost-viewer
make build      # produces ./bin/ccview
# or:
make install    # installs into $GOBIN
```

## Usage

```text
ccview [interval-seconds] [flags]
ccview [command]
```

Running `ccview` with no arguments refreshes a compact dashboard once a minute —
the default, familiar view. Press `Ctrl+C` to quit.

### Flags

| Flag                          | Default   | Description                                                  |
| ----------------------------- | --------- | ------------------------------------------------------------ |
| `-i`, `--interval`            | `1m`      | Refresh interval as a Go duration (`30s`, `1m`, `2m`).       |
| `--once`                      | `false`   | Fetch a single snapshot and exit.                            |
| `-m`, `--mode`                | `compact` | Output mode: `compact`, `table`, `json`, `oneline`, `tui`.   |
| `--json`                      | `false`   | Shortcut for `--mode json`.                                  |
| `-a`, `--all`                 | `false`   | Show per-model windows even when at 0%.                      |
| `--auto-reload-expired-token` | `false`   | Opt-in: renew an expired token by invoking Claude Code.      |
| `-d`, `--debug`               | `false`   | Print diagnostics (token source, HTTP status, raw response). |
| `--no-color`                  | `false`   | Disable ANSI colour (also honours the `NO_COLOR` env var).   |
| `-h`, `--help`                |           | Help for any command.                                        |
| `-v`, `--version`             |           | Print the version.                                           |

### Commands

- `ccview version` — print detailed build information.
- `ccview completion <shell>` — generate a shell completion script
  (`bash`, `zsh`, `fish`, `powershell`).
- `ccview help [command]` — help for any command.

### Examples

```sh
ccview                      # compact dashboard, refresh every minute
ccview --interval 2m        # refresh every two minutes
ccview 30                   # legacy positional form: every 30 seconds
ccview --once               # one snapshot, then exit (great for scripts)
ccview --mode table         # detailed table view
ccview --json --once        # machine-readable snapshot
ccview --mode oneline       # single line for status bars
ccview --mode tui           # interactive full-screen dashboard
ccview --debug              # diagnose token / endpoint problems
```

## Output modes

**compact** (default) — the at-a-glance bar view shown at the top of this
README. Intended for the long-running watch loop.

**table** — an aligned table with every available column:

```text
Plan: Max 20x

METER    USAGE                   %  RESETS     DETAIL
Session  ████████░░░░░░░░░░░░  42%  in 2h 22m
Weekly   ███░░░░░░░░░░░░░░░░░  13%  Sat 00:00
Opus     ██████████████████░░  90%  Sat 00:00
Extra    ███░░░░░░░░░░░░░░░░░  16%  —          $3.20 / $20.00
```

**json** — stable, machine-readable output for scripting and status bars. Reset
times are emitted both as RFC3339 timestamps and as seconds remaining:

```json
{
  "plan": "Max 20x",
  "generated_at": "2026-06-13T12:00:00Z",
  "meters": [
    {
      "key": "five_hour",
      "label": "Session",
      "kind": "session",
      "percent": 42,
      "resets_at": "2026-06-13T14:22:00Z",
      "resets_in_seconds": 8520
    }
  ]
}
```

**oneline** — one terse line, ideal for tmux / sketchybar / polybar:

```text
Claude 5h:42% 7d:13% opus:90% extra:16%
```

**tui** — an interactive full-screen dashboard (built with Bubble Tea). Keys:
`r` to refresh now, `q` / `Ctrl+C` to quit.

## Configuration

Environment variables:

| Variable                  | Purpose                                                             |
| ------------------------- | ------------------------------------------------------------------- |
| `CLAUDE_CODE_OAUTH_TOKEN` | Provide the OAuth token directly (highest priority).                |
| `CLAUDE_CODE_VERSION`     | Override the version used in the `User-Agent` header.               |
| `NO_COLOR`                | If set, disables ANSI colour (in addition to `--no-color`).         |
| `CCVIEW_MOCK_FILE`        | Render a usage payload from a JSON file instead of calling the API. |
| `CCVIEW_MOCK_PLAN`        | Plan label used together with `CCVIEW_MOCK_FILE`.                   |
| `CCVIEW_RELOAD_CMD`       | Command used by `--auto-reload-expired-token` to renew the token.   |

If `CLAUDE_CODE_VERSION` is not set, `ccview` runs `claude --version` to detect
the installed Claude Code version, falling back to a built-in default.

Colour is enabled automatically only when output is a terminal; when piped or
redirected, output is plain text. `json` output never contains colour.

## Rate limiting

The usage endpoint is aggressively rate-limited. The recommended minimum
interval is **60 seconds** (the default). `ccview` allows shorter intervals but
prints a warning, and it backs off exponentially (up to 15 minutes) whenever the
endpoint returns `HTTP 429`.

## Project layout

```text
.
├── cmd/ccview            # main package (thin entry point)
├── internal/
│   ├── auth              # OAuth token resolution (env / Keychain / file)
│   ├── buildinfo         # version metadata injected at build time
│   ├── cli               # Cobra commands + watch/debug/TUI run logic
│   ├── client            # HTTP client for the usage endpoint + backoff
│   ├── render            # compact / table / json / oneline renderers
│   ├── tui               # interactive Bubble Tea dashboard
│   └── usage             # usage model, plan classification, meter adapter
├── .github/workflows     # CI and release pipelines
├── .goreleaser.yaml      # release + Homebrew packaging
└── Makefile
```

The core packages (`usage`, `auth`, `client`, `render`) depend only on the
standard library; third-party dependencies (Cobra, Bubble Tea, lipgloss) live at
the edges (`cli`, `tui`).

## Development

```sh
make check        # gofmt, go vet, and tests
make test-race    # tests with the race detector
make cover        # coverage profile + total
make cover-html   # HTML coverage report
make lint         # golangci-lint (must be installed)
make snapshot     # local GoReleaser build, no publishing
make help         # list all targets
```

The test suite covers all application logic; the only intentionally uncovered
code is the `os.Exit` entry-point wrappers.

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/)
(`feat:`, `fix:`, `docs:`, …). See [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

**Use at your own risk.** `ccview` relies on an **undocumented** Anthropic
endpoint that may change or break at any time without notice. This project is
not affiliated with, endorsed by, or supported by Anthropic. It only ever reads
your local OAuth token and sends it to Anthropic's own endpoint; it never stores
or transmits it anywhere else. The software is provided "AS IS", without warranty
of any kind — see [LICENSE](LICENSE).

## License

Released under the [BSD Zero Clause License (0BSD)](LICENSE) — the most permissive
option available, imposing no conditions on use, modification, or distribution.
