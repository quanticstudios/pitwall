# pitwall promo

The promo video and the README hero loop, made with
[Remotion](https://www.remotion.dev). The pitwall window is rebuilt in React
from the app's own sources, so every frame is drawn, not recorded:

- colors from `internal/config/themes.go`, mixing as in `internal/ui/theme`
- sizes and state colors from `internal/ui/sidebar/sidebar.go` and
  `internal/ui/app`
- Lucide, Claude and OpenAI paths from `internal/ui/sidebar/icons.go` and
  `agent.go` (copied into `src/iconData.ts`), the logo from
  `packaging/pitwall.svg`

The people, folders and projects on screen are made up: user `dev`, host
`studio`, projects `acme-api`, `billing`, `web-app`, `docs-site`,
`ml-pipeline`.

## Preview

Needs Node.js and npm (made with Node 26 and npm 11) and, for the WebP, ffmpeg.

```sh
cd promo
npm ci
npx remotion studio
```

Studio opens in the browser with two compositions: `Promo` (1920x1080,
60 fps, 72 s) and `Hero` (1600x900, 30 fps, a 12 s loop).

## Render

```sh
npm run render       # out/pitwall-promo.mp4, H.264, yuv420p, CRF 16

# README hero: the loop as WebP, and a still for social previews
npx remotion render Hero out/hero.mp4 --codec=h264 --crf=8 --pixel-format=yuv444p
ffmpeg -i out/hero.mp4 -c:v libwebp_anim -q:v 82 -compression_level 6 -loop 0 ../docs/media/pitwall-hero.webp
npx remotion still Hero ../docs/media/pitwall-hero.png --frame=130
```

The music track is not in the repository. Put it at
`public/music/track.mp3` (git-ignored) before rendering, and only publish
the video with a track you have the rights to. `src/Promo.tsx` lines the
scene cuts up with that track's bars: the first drop (61.72 s) lands on the
Agents scene at 10 s, the second drop on Settings, the breakdown on the
outro. Another track needs `MUSIC_START` and the scene lengths recomputed
from its beats. Render with audio:
`npx remotion render Promo out/pitwall-promo.mp4 --codec=h264 --pixel-format=yuv420p --crf=16 --audio-codec=aac --audio-bitrate=320k`

## Layout

| Path                | What                                                         |
| ------------------- | ------------------------------------------------------------ |
| `src/Promo.tsx`     | Scene order and lengths                                      |
| `src/scenes/`       | One file per scene, all timing in frames at 60 fps           |
| `src/Hero.tsx`      | The README loop                                              |
| `src/ui/`           | The window: sidebar, panes, settings page, overlays          |
| `src/content.ts`    | What the terminals show                                      |
| `src/data.ts`       | The made-up tabs and groups                                  |
| `public/fonts/`     | Geist and JetBrains Mono, with their OFL licenses            |

## Licenses

- Geist (`public/fonts/Geist.ttf`, the same latin subset pitwall embeds) and
  JetBrains Mono 2.304 (`public/fonts/JetBrainsMono-*.woff2`) are under the SIL
  Open Font License 1.1; the license texts sit next to the fonts.
- Lucide icons are ISC, the Claude and OpenAI marks come from Simple Icons
  (CC0), as in the app.
- Remotion is not MIT. Its license (`node_modules/remotion/LICENSE.md`, also at
  [remotion.dev/license](https://www.remotion.dev/license)) lets you use it
  for free, commercially too, if you are an individual, a for-profit
  organization with up to 3 employees, a non-profit, or evaluating it without
  commercial use yet. Other for-profit organizations need a Company License
  from [remotion.pro](https://www.remotion.pro/license). Editing or
  re-rendering this project falls under those terms.
