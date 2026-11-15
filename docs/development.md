# Development

`mise.toml` sets `GOFLAGS=-tags=novulkan` so Gio builds with OpenGL and no
Vulkan headers. Use mise so every command gets it:

```sh
mise exec -- go build ./cmd/pitwall
mise exec -- go vet ./...
mise exec -- go test -race ./...
```

## pitwall-core

pitwall-core is `cmd/pitwall` built with the `nogui` tag: the daemon and
every command but the window, with no cgo, for servers, containers and arm64
boards.

```sh
CGO_ENABLED=0 mise exec -- go build -tags nogui -o pitwall-core ./cmd/pitwall
```

- Files that need the window carry `//go:build !nogui`. The window's entry
  points live in `cmd/pitwall/window.go`, and `cmd/pitwall/nogui.go` stands
  in for them in the core.
- Code the CLI and the window share goes in untagged files, such as
  `cmd/pitwall/join.go`.
- The core refuses the commands that need a window before it sends the
  daemon anything: `session new` without `-d`, and `attach` or
  `session attach` when no window shows the session. `web open` prints the
  link. `update` refuses, since the release it would install is the desktop
  binary.
- `pitwall-core --version` prints `pitwall <version> (core)`. The daemon's
  build string has no flavor, so a window treats a daemon pitwall-core
  started as any other.
- `scripts/core-imports.sh` fails when the core imports a Gio package other
  than the pure-Go ones `internal/config`, `internal/input` and
  `internal/remote` use, or any `internal/ui` package. CI's `core` job runs
  it, builds the core for amd64 and arm64 without cgo, and runs the
  `cmd/pitwall` and `internal/update` tests in the core build.
- `GOFLAGS` from `mise.toml` replaces a `GOFLAGS` set outside it, so pass
  `-tags nogui` on the command line, or set `GOFLAGS` inside `mise exec`.

Releases use patch versioning; see [CHANGELOG.md](../CHANGELOG.md).
