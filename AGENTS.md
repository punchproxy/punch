# Repository Guidelines

## Project Structure & Module Organization

This is a Go module (`github.com/punchproxy/punch`) for a local proxy daemon and CLI. Entry points live under `cmd/`: `cmd/punchd` builds the daemon and `cmd/punchctl` builds the operator CLI. Core packages are in `internal/`: `api`, `config`, `dns`, `dnsrule`, `fakeip`, `relay`, `session`, and `tun` cover the API, SQLite config, DNS, relay selection, sessions, and platform networking. Tests sit beside the packages they cover as `*_test.go` files. Repository documentation starts in `README.md`.

## Build, Test, and Development Commands

- `go build ./...` builds every package and catches compile errors across both binaries.
- `go test ./...` runs the full test suite.
- `go build -o punchd ./cmd/punchd` builds the daemon binary.
- `go build -o punchctl ./cmd/punchctl` builds the CLI binary.
- `sudo ./punchd -data-dir ./data -debug` starts a local development daemon with an explicit data directory.

Use `./punchctl status` from a second terminal to inspect a running daemon.

## Coding Style & Naming Conventions

Use standard Go formatting. Run `gofmt` on changed Go files before committing; keep imports organized with the Go toolchain. Package names should be short, lowercase, and aligned with their directory purpose. Exported identifiers need clear doc comments when they form package API. Keep platform-specific code in files named with Go build suffixes such as `_darwin.go`, `_linux.go`, or `_windows.go`.

## Testing Guidelines

Tests use Go’s built-in `testing` package. Add focused `*_test.go` coverage beside the package you change, with names like `TestCacheStoresAnswer` or `TestConfigSetPersistsValue`. For network, DNS, relay, or platform-sensitive behavior, prefer deterministic fakes and table-driven cases over real external services. Run `go test ./...` before opening a PR.

## Commit & Pull Request Guidelines

The current history uses short, imperative, lowercase commit subjects such as `fix readme badges`. Keep commits scoped and describe behavior changes. Pull requests should include a concise summary, test results (`go test ./...`), and operational notes for DNS, TUN, route, or credential-related changes. Link issues when applicable and avoid committing real relay credentials, API tokens, local databases, or private subscription URLs.

## Security & Configuration Tips

Punch changes DNS, TUN, and routing behavior. Test networking changes in a controlled environment, use `-data-dir ./data` for local experiments, and document the OS when reporting platform-specific results.
