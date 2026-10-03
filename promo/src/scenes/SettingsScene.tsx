import React from 'react';
import {useCurrentFrame} from 'remotion';
import {ease, inOut, track} from '../anim';
import {codexTests, rateLimit, thinking} from '../content';
import {blend, themes} from '../theme';
import {ConfigFile, Settings, type Shortcut} from '../ui/Settings';
import {Caption, KeySeq, Pointer, Stage} from '../ui/Window';
import {rateRoot} from './Agents';
import {App} from './common';
import {settledItems} from './Osc';

/** Path moves the pointer through [frame, x, y] keys, easing between them. */
export const path = (f: number, keys: [number, number, number][]) => {
  let i = 0;
  while (i < keys.length - 1 && f > keys[i + 1][0]) i++;
  const [f0, x0, y0] = keys[i];
  const [f1, x1, y1] = keys[Math.min(i + 1, keys.length - 1)];
  const t = f1 === f0 ? 1 : Math.min(1, Math.max(0, (f - f0) / (f1 - f0)));
  const e = inOut(t);
  return {x: x0 + (x1 - x0) * e, y: y0 + (y1 - y0) * e};
};

/** Press is the click pulse of the latest click at or before f. */
export const press = (f: number, clicks: number[]) => {
  const c = clicks.filter((x) => f >= x).pop();
  return c !== undefined && f - c < 8 ? Math.sin(((f - c) / 8) * Math.PI) : 0;
};

const CLICK_THEME: [number, string][] = [[0, 'aide-dark'], [96, 'tokyo-night'], [166, 'catppuccin-mocha'], [236, 'aide-light'], [306, 'aide-dark']];

/** SettingsScene opens the settings page, switches themes, steps the text size and swaps a conflicting shortcut. */
export const SettingsScene: React.FC<{dur: number}> = ({dur}) => {
  const f = useCurrentFrame();
  let i = 0;
  while (i < CLICK_THEME.length - 1 && f >= CLICK_THEME[i + 1][0]) i++;
  const from = themes[CLICK_THEME[Math.max(0, i - 1)][1]];
  const to = themes[CLICK_THEME[i][1]];
  const th0 = i === 0 ? to : blend(from, to, ease(f, CLICK_THEME[i][0], 8));
  const uiSize = f >= 404 ? 14 : 13;
  const th = {...th0, ui: uiSize / 13};
  const open = f >= 18;
  const page = f >= 470 ? 'shortcuts' : 'appearance';
  const scroll = page === 'appearance' ? track(f, [[340, 0], [378, 250]]) : track(f, [[470, 0], [480, 0], [505, 40]]);
  const swapped = f >= 628;
  const base: Shortcut[] = [
    {label: 'New tab below this one, in its folder', name: 'new_tab', chords: ['Ctrl+Shift+T']},
    {label: 'Split the pane to the right', name: 'split_right', chords: [swapped ? 'Ctrl+Shift+B' : 'Ctrl+Shift+O']},
    {label: 'Show or hide the sidebar', name: 'toggle_sidebar', chords: [swapped ? 'Ctrl+Shift+O' : 'Ctrl+Shift+B']},
    {label: 'Go to the tab that needs you, newest first', name: 'jump_attention', chords: ['Ctrl+Shift+U']},
    {label: 'Show or hide the settings page', name: 'open_settings', chords: ['Ctrl+,']},
  ];
  const recording = f >= 522 && f < 628 ? 'toggle_sidebar' : undefined;
  const conflict = f >= 562 && f < 628 ? {chord: 'Ctrl+Shift+O', other: 'Split the pane to the right', gives: 'Ctrl+Shift+B', swapHover: f >= 604} : undefined;
  const flashT = swapped ? 1 - ease(f, 628, 40) : 0;

  const p = path(f, [
    [0, 760, 520], [60, 760, 520], [90, 1124, 236], [150, 1124, 236], [160, 670, 412], [220, 670, 412], [230, 897, 236], [290, 897, 236], [300, 670, 236],
    [340, 670, 236], [380, 1218, 418], [440, 1218, 418], [462, 380, 151], [500, 380, 151], [516, 1152, 368], [580, 1152, 368], [600, 1194, 414], [700, 1194, 414],
  ]);
  const clicks = [96, 166, 236, 306, 404, 470, 522, 628];

  const cfgLines = [
    {t: '# pitwall: edited by hand and by the settings page'},
    {t: '[theme]'},
    {t: `name = "${CLICK_THEME[i][1]}"`, flash: i > 0 ? 1 - ease(f, CLICK_THEME[i][0], 40) : 0},
    {t: ''},
    {t: '[font]'},
    {t: `ui_size = ${uiSize}  # kept when the app edits this file`, flash: f >= 404 ? 1 - ease(f, 404, 40) : 0},
    {t: ''},
    {t: '[keys]'},
    {t: `toggle_sidebar = "${swapped ? 'Ctrl+Shift+O' : 'Ctrl+Shift+B'}"`, flash: flashT},
    {t: `split_right = "${swapped ? 'Ctrl+Shift+B' : 'Ctrl+Shift+O'}"`, flash: flashT},
  ];
  const cfgIn = ease(f, 40, 20);
  return (
    <Stage window={
      <App th={th}
        side={{items: settledItems(f), active: 'rate', frame: f, settingsOn: open}}
        panes={open ? undefined : {root: rateRoot, focused: 'r1', panes: {r1: {lines: [...rateLimit, thinking(f, 'Wiring the router')]}, r2: {lines: codexTests}}}}
        content={open ? (
          <Settings th={th} w={991} h={720} page={page} themeName={CLICK_THEME[i][1]} uiSize={uiSize} stepPress={press(f, [404])} scroll={scroll}
            shortcuts={base} recording={recording} conflict={conflict} flash={{toggle_sidebar: flashT, split_right: flashT}} />
        ) : undefined}
        overlay={f >= 50 ? <Pointer x={p.x} y={p.y} press={press(f, clicks)} /> : undefined}
      />
    }>
      <div style={{position: 'absolute', right: 40, top: 600, opacity: cfgIn, translate: `${(1 - cfgIn) * 30}px 0`}}>
        <ConfigFile th={themes['aide-dark']} w={540} lines={cfgLines} />
      </div>
      <Caption t={f / 60} dur={336 / 60} text="Pick a theme and the whole window follows, live." sub="aide-dark, aide-light, tokyo-night, catppuccin-mocha, or your own file." />
      {f >= 336 && <Caption t={(f - 336) / 60} dur={(dur - 336) / 60} text="Every change writes one line of config.toml and keeps your comments." sub="The shortcut recorder catches conflicts and offers a swap." />}
      <KeySeq f={f} events={[[12, ['Ctrl', ',']], [560, ['Ctrl', 'Shift', 'O']]]} />
    </Stage>
  );
};

