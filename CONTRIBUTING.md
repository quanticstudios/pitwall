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
[docs/install.md](docs/install.md#build-from-source), then run:

```sh
gofmt -w cmd internal third_party/x-vt
mise exec -- go build ./cmd/pitwall
mise exec -- go vet ./...
mise exec -- go test -race -timeout 5m ./...
mise exec -- go -C third_party/x-vt vet ./...
mise exec -- go -C third_party/x-vt test -race -timeout 5m ./...
sh scripts/get_test.sh
sh scripts/release_test.sh
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
go test -timeout 5m ./...
go -C third_party/x-vt test -timeout 3m ./...
```

Some tests run commands through Git's `sh`, so run them from a shell that
has Git for Windows' `usr\bin` on PATH, such as Git Bash.

`third_party/x-vt` is a separate Go module. Root tests do not run its tests.
On macOS, you can also run `go test -timeout 3m ./...` and report any
platform failures. The full suite there remains advisory in CI. Run additional tests relevant to your change and report any skipped tests.

Keep a change focused on one problem. Add a regression test for a behavior
change. Use the standard library first; explain any new dependency. Keep
copied code's attribution and update [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)
when licenses or bundled dependencies change.

Describe a change users will notice in a line under `## Unreleased` at the
top of [CHANGELOG.md](CHANGELOG.md). Add the heading above the newest version
if it is missing. CI fails a pull request that changes `cmd/` or `internal/`
without such a line. For a change users will not notice, such as a refactor or
a test, ask a maintainer to add the `no-changelog` label, then re-run the
check. Maintainers release with `scripts/release.sh`, as described at the top
of CHANGELOG.md.

### Visual checks

`go run ./cmd/pwdemo-sidebar` draws the sidebar against a fake state with
every agent state. Its flags force states that are hard to reach by clicking:
`-update <label>` shows the footer's update button, `-host <name>` names an
ssh host in the header as `pitwall --host` does, `-shared divider` or
`-shared sections` draws a shared session's machine lines, `-numbers` shows
the goto_tab digits, `-active <tab id>` picks the active tab, `-expand-all` opens
every group, `-unseen` marks needs-you activity unseen and `-size WxH` sets the
window size in dp. Run `go run ./cmd/pwdemo-sidebar -h` for the list. In an
`agent-ws` display, Gio repaints hover only after the second pointer move, so
move the pointer twice before a hover screenshot.

`cmd/pwbench` measures the real window and daemon with fixed Go fixtures.
Build both commands from the checkout to measure, then use a private display:

```sh
env -u PITWALL_SOCKET -u PITWALL_PANE -u PITWALL_ATTACH GOFLAGS=-tags=novulkan mise exec -- go build -o /tmp/pitwall-bench ./cmd/pitwall
env -u PITWALL_SOCKET -u PITWALL_PANE -u PITWALL_ATTACH GOFLAGS=-tags=novulkan mise exec -- go build -o /tmp/pwbench ./cmd/pwbench
agent-ws run draw-bench --wait -- /tmp/pwbench -pitwall /tmp/pitwall-bench -workload spinners -seconds 10
agent-ws down draw-bench
/tmp/pwbench compare /tmp/pwbench-results-a/result.json /tmp/pwbench-results-b/result.json
```

Repeat with `-workload titles`, `flood` and `scroll`. `spinners` changes only
a title's spinner suffix at 4 Hz. `titles` opens eight tabs with one pane
each, alternating fake Claude and Codex working states, and title suffixes
at 10 Hz. `flood` redraws a colored terminal dashboard at 60 Hz. `scroll`
fills 10,000 lines and moves the view three lines at 60 Hz, reversing every
120 steps. The fixtures need no agent installation or network access.

The command prints a fresh results directory containing `result.json`,
`frames.json` and `process.log`. It uses temporary HOME and XDG directories,
then closes its panes, window and daemon and removes those directories.
It leaves the user's daemon, config and agent skills alone. Ctrl+C also
cleans up the benchmark processes. Remove the results directory when done.

Frame counts and nearest-rank p50, p95 and maximum times cover the requested
seconds starting with the first frame. Work timings include layout and ops
submission, measured before Gio acknowledges the frame or presents it.
`wait_p50_ms`, `wait_p95_ms` and `wait_max_ms` separately record the time from
submission until Gio's `Frame` call returns. On X11 this includes presentation.
Event waits and GPU completion timestamps are not frame work. The recorded scale is Gio's
pixels per dp. CPU seconds include user and system time for each process's
whole lifetime, including startup and shutdown, and exclude fixture CPU.
Compare requires matching workloads, durations, scales, timing formats and vsync settings.

The benchmark window requests swap interval 0 with `vblank_mode=0` for Mesa
and `__GL_SYNC_TO_VBLANK=0` for NVIDIA. Hidden rootful Xwayland windows can
otherwise block in presentation for about one second per frame. The fixtures
and scroll requests provide the 60 Hz workload rate. Ordinary windows keep
their vsync settings. Results record `vsync = "off"` and
`timing = "layout-submit-v1"`, so comparison rejects the wall-time format.

An unfocused or hidden window may draw less. Keep focus, visibility, display
size, scale and rendering backend the same between runs. The private X11
display does not measure performance under a Wayland compositor. The frame
hook stays off unless `pwbench` sets `PITWALL_DRAW_BENCH` and
`PITWALL_DRAW_BENCH_DURATION` for its window.

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
with the race detector, and Windows runs it without. The full macOS suite is
advisory while that platform remains experimental; its build, vet check,
selected platform-independent tests and installer check must pass.

## Working with others

Discuss the code and behavior respectfully. Give reproduction steps and
specific feedback. Harassment, discriminatory comments and publishing
someone's private information are not acceptable. @quanticstudios moderates
issues and pull requests.

Contributions use the repository's [MIT license](LICENSE). Keep third-party
license notices intact.
