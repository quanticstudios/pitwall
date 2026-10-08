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

## Patch: soft wraps for reflow

pitwall rewraps the main screen and its history when a pane changes width,
which needs to know which rows ended at the right margin rather than at a
line break. Upstream does not record that.

- `Screen` keeps a `wrapped` flag per row. `handleGrapheme` in `utf8.go` sets
  it when autowrap moves to the next row. Erasing or filling a row through
  its last column clears it, and line insertion, deletion and scrolling move
  the flags with their rows. `Screen.Wrapped` and `Screen.Line` read them.
- `Scrollback` stores the flag beside each line (`Scrollback.Wrapped`). A
  wrapped line keeps its trailing blanks, which are text that goes on in the
  next line. `Push` behaves as before and pushes an unwrapped line.
- A wide character that does not fit before the right margin wraps whole and
  leaves a zero cell behind, as in xterm. One that ends in the last column
  leaves the cursor there waiting to wrap. Upstream wrote a cut-off blank in
  the first case and let the next character overwrite the wide character's
  right half in the second.
- `Emulator.MainLines` returns the main screen's rows, flags and cursors, and
  `Emulator.ResizeMain` resizes with the main screen's rows replaced. The
  rewrapping itself lives in pitwall's `internal/vt/reflow.go`.

`TestSoftWrapFlags` in `wrap_test.go` covers the flags, and
`internal/vt/reflow_test.go` covers the reflow.

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
