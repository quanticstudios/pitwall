import React from 'react';
import {useCurrentFrame} from 'remotion';
import {ease, pick, smooth} from '../anim';
import {codexTests, prompt, rateLimit, thinking} from '../content';
import {dark} from '../theme';
import type {Node} from '../ui/Panes';
import {Caption, KeySeq, ModePill, Stage} from '../ui/Window';
import {App} from './common';
import {settledItems} from './Osc';

const KEYS: [string, string][] = [['N', 'new'], ['D/R', 'down/right'], ['X', 'close'], ['H/J/K/L', 'focus'], ['F', 'fullscreen'], ['P', 'next'], ['Esc', 'exit']];

/** PaneMode chains pane keys behind one prefix: split down, move right, fullscreen and back. */
export const PaneMode: React.FC<{dur: number}> = ({dur}) => {
  const f = useCurrentFrame();
  const th = dark;
  const on = f >= 24 && f < 304;
  const sd = ease(f, 74, 16, smooth);
  const left: Node = f < 74 ? {pane: 'r1'} : {dir: 'v', children: [{pane: 'r1'}, {pane: 'r3'}], ratios: [1 - 0.42 * sd, 0.42 * sd]};
  const root: Node = {dir: 'h', children: [left, {pane: 'r2'}], ratios: [0.54, 0.46]};
  const cur = pick<string>(f, [[0, 'r1'], [74, 'r3'], [134, 'r2']]);
  const zoom = f >= 184 && f < 254 ? 'r2' : undefined;
  const hot = pick<string | undefined>(f, [[0, undefined], [74, 'down/right'], [96, undefined], [134, 'focus'], [156, undefined], [184, 'fullscreen'], [206, undefined], [254, 'fullscreen'], [276, undefined], [304, 'exit']]);
  return (
    <Stage window={
      <App th={th} side={{items: settledItems(f), active: 'rate', frame: f}}
        panes={{
          root, zoom, zoomLabel: 'fullscreen',
          focused: on ? undefined : cur, lit: cur,
          panes: {
            r1: {lines: [...rateLimit, thinking(f, 'Wiring the router')]},
            r2: {lines: codexTests},
            r3: {lines: [prompt('~/src/acme-api', 'feat/rate-limits')], cursor: {}},
          },
        }}
        pill={on ? <ModePill th={th} name="PANE" keys={KEYS} hot={hot} /> : undefined}
      />
    }>
      <Caption t={f / 60} dur={dur / 60} text="Pane mode keeps your multiplexer muscle memory." sub="On a prefix you pick: split, move with h/j/k/l, go fullscreen. Keys chain until Esc." />
      <KeySeq f={f} events={[[20, ['Pane mode']], [70, ['D']], [130, ['L']], [180, ['F']], [250, ['F']], [300, ['Esc']]]} />
    </Stage>
  );
};
