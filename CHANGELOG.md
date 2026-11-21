# Changelog

## Unreleased

- Add `file.WalkGitProjects` and `file.EnclosingGitProject` for finding git projects
- Add `http.ServeLocalListener` for serving locally on a listener the caller opened
- Add `temporal` package for building Temporal client options with token credentials
- Add `text` package for line diffs, with context hunks and unified output
- `desktop.Launch` now takes its arguments as a slice, followed by options to connect the program's standard streams

## v0.0.2 - 2026-11-21

- Add `desktop` package for launching programs, opening URLs, the clipboard and per-user directories
- Add `file` package for atomic file writes
- Add HTTP serve and graceful shutdown helpers, including a local mode with a Host check
- Add `ui` package with relative time formatting
- `tailscale.NewServer` accepts a logger, and logs with `component=tailscale` instead of `source=tailscale` by default

## v0.0.1 - 2026-11-21

_Initial version, covering some OIDC functionality, Gio utilities, IRC formatting and tailscale serving_