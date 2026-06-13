# Contributing

Thanks for your interest in improving `ccview`. This document describes how to
build, test, and submit changes.

## Prerequisites

- Go 1.24 or newer.
- (Optional) [golangci-lint](https://golangci-lint.run) v2 for linting.
- (Optional) [GoReleaser](https://goreleaser.com) v2 for release builds.

## Getting started

```sh
git clone https://github.com/merlindeep/claude-cost-viewer.git
cd claude-cost-viewer
make build
./bin/ccview --help
```

You can run the tool without a real account using the mock mode:

```sh
cat > /tmp/usage.json <<'JSON'
{"five_hour":{"utilization":42,"resets_at":"2026-12-31T23:59:00Z"}}
JSON
CCVIEW_MOCK_FILE=/tmp/usage.json CCVIEW_MOCK_PLAN=max_20x ./bin/ccview --once
```

## Development workflow

```sh
make check        # gofmt + go vet + tests (run this before pushing)
make test-race    # tests under the race detector
make cover        # coverage profile and total
make lint         # golangci-lint
```

Please make sure:

- new behaviour is covered by tests (the suite aims for complete coverage of
  application logic);
- code is `gofmt`-clean and passes `go vet` and `golangci-lint`;
- exported identifiers have doc comments.

## Code style

- All code comments and documentation are written in **English**.
- The core packages (`usage`, `auth`, `client`, `render`) must stay
  dependency-free (standard library only). Third-party dependencies belong in
  the edge packages (`cli`, `tui`).

## Commit messages

This project uses [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add oneline output mode
fix: handle missing reset timestamps
docs: clarify rate-limit guidance
test: cover backoff edge cases
```

The release changelog is generated from commit messages, so clear, typed
commits are appreciated.

## Pull requests

1. Fork and create a topic branch.
2. Make your change with tests and documentation.
3. Run `make check` and `golangci-lint run`.
4. Open a PR using the provided template and link any related issues.

## Reporting bugs and requesting features

Use the issue templates. For anything security-related, please follow
[SECURITY.md](SECURITY.md) instead of opening a public issue.
