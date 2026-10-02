# x/vt patch

This directory contains `github.com/charmbracelet/x/vt` at
`v0.0.0-20261001101533-953920dd3285`, commit
[`953920dd32852646690911d603874c7057482653`](https://github.com/charmbracelet/x/commit/953920dd32852646690911d603874c7057482653).
The upstream MIT license is in `LICENSE`.

## Patch

`NewEmulator` in `emulator.go` calls `t.scrs[1].SetScrollback(nil)` after
creating the alternate screen. A nil buffer disables history for scrolling
and screen erasure. Resize and reset preserve the nil buffer. The main screen
keeps its upstream behavior and public APIs remain unchanged.

Pitwall never displays alternate-screen history. Keeping full cells for its
10,000 invisible history lines retains about 134 MiB per pane at 120 columns.
Disabling that buffer retains zero history lines.

## Why option 3

On 2026-10-02, `go list -m -versions github.com/charmbracelet/x/vt` returns
no tagged versions. `go list -m github.com/charmbracelet/x/vt@latest` resolves
to the version above. The [latest commit API](https://api.github.com/repos/charmbracelet/x/commits?per_page=1)
returns the same commit. The [latest commit for vt](https://api.github.com/repos/charmbracelet/x/commits?path=vt&per_page=1)
is `a5dee49b28632257cd9a475e8ca36e98a62ff155`.

`Emulator.Scrollback`, `SetScrollbackSize`, and `ClearScrollback` only access
the main screen. `Screen.SetScrollback(nil)` can disable history, but
`Emulator` exposes neither screen through an accessor or callback. Its
`AltScreen` callback receives only a bool. Injecting ED 3 clears visible
cells, and `Scrollback.Clear` retains references in the backing array.
Options 1 and 2 cannot disable or safely drain alternate-screen history.

## Verification

From the pitwall root, run `mise exec -- go test ./internal/vt`.
`TestAltScreenMemory` writes 50,000 full-width scrolling lines on the alternate
screen and requires retained heap growth below 2 MiB after garbage collection.
The baseline excludes construction of the emulator, as in `TestHistoryMemory`.
