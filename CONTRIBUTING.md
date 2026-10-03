# Contributing to pitwall

pitwall is alpha software. Bug reports, documentation fixes and focused code
changes are welcome. Discuss a large feature in an issue before building it.

## Report a bug

Search the [existing issues](https://github.com/quanticstudios/pitwall/issues)
first. Include your pitwall version, operating system, architecture, display
server, steps to reproduce, and expected and actual behavior. For an agent
status problem, include the agent version and whether hooks are installed.

Logs and screenshots can contain terminal contents, prompts, project paths
and credentials. Remove private data before posting. Report security problems
through the private channel in [SECURITY.md](SECURITY.md).

## Build and check a change

Fork the repository and clone your fork. Add the upstream repository, then
start a branch from current `main`:

```sh
git remote add upstream https://github.com/quanticstudios/pitwall.git
git fetch upstream
git switch -c fix/describe-the-change upstream/main
```

Use the Go version pinned in `go.mod`, currently Go 1.27.1. If you use mise,
run `mise install go`; `mise.toml` also selects Gio's OpenGL build. Contributors
can build and test on Linux, macOS or Windows. GitHub runs the platform checks.

### Linux

Install the native dependencies listed in
[README.md](README.md#build-from-source), then run:

```sh
gofmt -w cmd internal third_party/x-vt
mise exec -- go build ./cmd/pitwall
mise exec -- go vet ./...
mise exec -- go test -race -timeout 5m ./...
mise exec -- go -C third_party/x-vt vet ./...
mise exec -- go -C third_party/x-vt test -race -timeout 5m ./...
sh scripts/get_test.sh
```

### macOS

Install Git, Go 1.27.1 and the Xcode Command Line Tools. In your shell, run:

```sh
export GOFLAGS=-tags=novulkan
gofmt -w cmd internal third_party/x-vt
go build ./cmd/pitwall
go vet ./...
go test -timeout 3m ./internal/agent ./internal/pane ./internal/model ./internal/layout ./internal/input
go -C third_party/x-vt test -timeout 3m ./...
sh scripts/get_test.sh
```

### Windows

Install Git and Go 1.27.1. In PowerShell, run:

```powershell
$env:GOFLAGS = '-tags=novulkan'
gofmt -w cmd internal third_party/x-vt
go build ./cmd/pitwall
go vet ./...
go test -timeout 3m ./internal/agent ./internal/pane ./internal/model ./internal/layout ./internal/input
go -C third_party/x-vt test -timeout 3m ./...
```

`third_party/x-vt` is a separate Go module. Root tests do not run its tests.
On macOS and Windows, you can also run `go test -timeout 3m ./...` and report
any platform failures. The full suites on those platforms remain advisory in
CI. Run additional tests relevant to your change and report any skipped tests.

Keep a change focused on one problem. Add a regression test for a behavior
change. Use the standard library first; explain any new dependency. Keep
copied code's attribution and update [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)
when licenses or bundled dependencies change.

## Open a pull request

1. Push your branch to your fork and open a pull request against
   `quanticstudios/pitwall:main`.
2. Use a title that states the result, such as "Keep detached tabs after a
   daemon restart". Fill in the pull request template with the problem,
   behavior, tested OS versions and architectures, Go versions, and build/test
   commands and results. List untested platforms explicitly. Link a related
   issue if one exists.
3. Open a draft while the change is incomplete. Mark it ready when you have
   checked the change. Include screenshots for a visible UI change.
4. Reply to review comments and resolve discussions after addressing them.
   New commits dismiss an existing approval, so request another review.

@quanticstudios reviews contributions. Every contributor's pull request needs
@quanticstudios's approval, resolved review discussions, and the required CI
checks against current `main`. The owner can merge a pull request using the
owner exception described in [docs/maintainers.md](docs/maintainers.md).
GitHub blocks direct pushes, force pushes and deletion of `main` for everyone.
Maintainers squash merges and delete merged branches.

External contributors' workflows need maintainer approval before GitHub runs
them. CI builds and vets Linux, macOS and Windows. Linux runs the full suite
with the race detector. The full macOS and Windows suites are advisory while
those platforms remain experimental; their builds, vet checks, selected
platform-independent tests and installer checks must pass.

## Working with others

Discuss the code and behavior respectfully. Give reproduction steps and
specific feedback. Harassment, discriminatory comments and publishing
someone's private information are not acceptable. @quanticstudios moderates
issues and pull requests.

Contributions use the repository's [MIT license](LICENSE). Keep third-party
license notices intact.
