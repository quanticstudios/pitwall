# gioui.org/x patch

This directory holds two packages of `gioui.org/x` at `v0.10.3`, as the
Go module proxy serves it: `richtext` and `styledtext`, which the markdown
reader draws inline text with, on the desktop and in the phone app, with
upstream's `LICENSE` (Unlicense OR MIT) and `go.mod`. The other packages
and the tests are left out. `go.mod` at the pitwall root replaces
`gioui.org/x` with this directory.

## Patch

The change is marked `pitwall` in a comment:

- `styledtext/styledtext.go`, `Layout`: spans of one line share its
  baseline. Upstream drew each span from the line's top, so a span in a
  font with a smaller ascent, such as inline code, rode above the text.
