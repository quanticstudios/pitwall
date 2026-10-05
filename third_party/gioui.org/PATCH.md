# Gio patch

This directory contains `gioui.org` at `v0.10.3`, commit
`6cc1cf3ea6cbf63fc4a9d6abc45edd4a25930b91` from
`https://git.sr.ht/~eliasnaur/gio`. The upstream license (Unlicense OR MIT) is
in `LICENSE`. Tests, testdata, `.builds` and the flake files are removed.

## Patch

Each change is marked `pitwall` in a comment:

- `app/os_wayland.go`, `window.present` (new): runs the swap with a frame
  callback on the commit it makes, and marks the frame in flight until the
  compositor calls back. A failed swap drops the request, so the window
  never waits on a callback that may not come.
- `app/os_wayland.go`, `window.draw`: while a frame is in flight it draws
  nothing; a sync draw (a configure, an output change) waits for the
  callback, then draws. The callback is no longer requested here.
- `window.present` also skips the swap when a frame is already in flight
  (one queued before the last presented); that frame is drawn again on the
  callback.
- `app/egl_wayland.go`, `Present`: goes through `window.present`.

The Vulkan path (`app/vulkan_wayland.go`) is upstream's. pitwall always builds
with `-tags=novulkan`, and skipping a Vulkan present after rendering would
leave its present semaphore signalled, so the patch does not touch it.

So a window presents at most one frame per frame callback. A frame that
skips Present (a surface that is out of date) asks for no callback, so it
leaves nothing in flight.

Upstream, a configure draws at once even while the window is hidden. A
compositor may send a hidden window no frame callbacks and may keep its
buffers; Hyprland does both. With swap interval 0, Mesa's eglSwapBuffers waits for a
free buffer, so after a few presents to a hidden window it blocks. The
window's thread then stops answering the compositor's pings, and Hyprland
shows "Application Not Responding". pitwall's stall dumps showed the main
goroutine in `eglSwapBuffers` every time, seconds after the window lost
focus.

## Why a copy

v0.10.3 is the latest Gio release (2026-10-05). It binds `xdg_wm_base` at
version 1, so it never learns that a window is suspended, and it gives the
program no say over when a sync draw presents.

## Updating

Copy the new release over this directory, remove the same files, apply the
changes again, and keep the `replace` in pitwall's `go.mod`.

## Verification

No test drives a Wayland compositor. On Hyprland: open pitwall, start an agent
or `yes`, move to another workspace for a minute, resize or refocus windows
there, come back. The window must not hang, and `pitwall logs` must show no
"window event stalled" line.
