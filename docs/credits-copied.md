# Third-party notices

pitwall is built on other people's open source work. This file lists the code
and assets pitwall copied or adapted, then the license of every Go module
linked into the `pitwall` binary. `scripts/notices.sh` regenerates it.

## Code and data adapted into pitwall

### tuios

<https://github.com/Gaurav-Gosain/tuios>, MIT License, Copyright (c) 2025
Gaurav Gosain. Read at commit `e1af811a824e706a915385ed6de118704a877096`.

pitwall started by studying tuios, a terminal window manager with agent
awareness, and took the parts a slim multiplexer needs. Each adapted block
carries a `// Adapted from tuios (MIT): <path>` comment:

| pitwall file | tuios source |
| --- | --- |
| `internal/pane/pane.go` | `internal/ptyspawn/spawn.go` (PTY spawn retry on EPERM) |
| `internal/store/store.go` | `internal/session/agent_resume.go` (agent resume commands) |
| `internal/daemon/detect.go` | `internal/session/agent_detect_linux.go` (finding an agent in a pane's process group) |
| `internal/agent/screen.go` | `internal/harness/manifests/claude-code.toml` (Claude Code screen patterns) |
| `internal/agent/testdata/tuios-*.txt` | `internal/session/testdata/` (screen fixtures; tuios derived some of them from [herdr](https://github.com/herdrdev/herdr)) |

```text
MIT License

Copyright (c) 2025 Gaurav Gosain

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### charmbracelet/x/vt (vendored and patched)

<https://github.com/charmbracelet/x>, MIT License, Copyright (c) Charmbracelet,
Inc. pitwall's terminal emulator is x/vt. A copy lives in `third_party/x-vt`
with one line changed to turn off alternate-screen scrollback; its license is
in `third_party/x-vt/LICENSE` and the change in `third_party/x-vt/PATCH.md`.

### Lucide icons

<https://lucide.dev>, ISC License, Copyright (c) 2026 Lucide Icons and
Contributors; some icons derive from Feather, MIT License, Copyright (c)
2013-2023 Cole Bemis. `internal/ui/sidebar/icons.go` embeds the SVG path data
of the icons aide uses, from lucide-react 1.23.0.

```text
ISC License

Copyright (c) 2026 Lucide Icons and Contributors

Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
```

```text
The MIT License (MIT) (for portions derived from Feather)

Copyright (c) 2013-2023 Cole Bemis

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### Geist font

<https://vercel.com/font>, SIL Open Font License 1.1, Copyright 2024 The Geist
Project Authors. `internal/ui/theme/fonts/Geist.ttf` is converted from the
latin Geist variable woff2; the full license is in
`internal/ui/theme/fonts/OFL.txt`.

### Theme palettes

The built-in themes in `internal/config/themes.go` copy color values from
[Tokyo Night](https://github.com/folke/tokyonight.nvim) (MIT, Folke Lemaitre),
[Catppuccin](https://github.com/catppuccin/palette) (MIT, Catppuccin) and,
for aide-light's state and ANSI colors, GitHub's
[Primer](https://github.com/primer/primitives) light palette (MIT, GitHub Inc.).

### Ideas and behavior, no code copied

- **aide** (Quantic Studios): the sidebar design, colors, agent states and
  navigation are ported from it.
- **[zj-radar](https://github.com/marktoda/zj-radar)** (MIT, Mark Toda): its
  hook adapters informed how Claude Code and Codex hook events map to states.
- **[zellij](https://zellij.dev)** (MIT): the Ctrl+T tab mode and the
  generated adjective-animal session names follow its lead.
- **[tmux](https://github.com/tmux/tmux)** (ISC): sessions, detach and attach.
- **[Ghostty](https://ghostty.org)** (MIT): rendering quality was checked
  against it, and the conventional keymap follows its defaults.

### Used at run time, not bundled

JetBrains Mono / JetBrainsMono Nerd Font (OFL 1.1) and Noto Color Emoji and
Noto CJK (OFL 1.1) are read from the system font directories when installed.
