# Security Policy

## How `ccview` handles your credentials

`ccview` is designed to be conservative with your Claude OAuth token:

- The token is **read only**. The tool never writes, refreshes, or modifies any
  credential store.
- The token is sent **only** to Anthropic's own usage endpoint
  (`https://api.anthropic.com/api/oauth/usage`) over HTTPS, in the
  `Authorization` header. It is never sent anywhere else.
- The token is **never logged**. The `--debug` view prints only a masked form
  (first 6 and last 4 characters).
- There is no telemetry and no network access other than the single usage
  request described above.

Because the project depends on an **undocumented** endpoint, its behaviour may
change without notice if Anthropic changes that endpoint. See the disclaimer in
the [README](README.md).

## Supported versions

This project is pre-1.0. Security fixes are applied to the latest released
version and `main`.

## Reporting a vulnerability

Please report suspected vulnerabilities **privately**, not via a public issue:

- Preferred: open a [GitHub Security Advisory](https://github.com/merlindeep/claude-cost-viewer/security/advisories/new).
- Alternatively, contact the maintainer through the address on their GitHub
  profile.

Please include steps to reproduce and any relevant environment details. You can
expect an initial response within a reasonable time frame, and coordinated
disclosure once a fix is available.
