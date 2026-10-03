import React from 'react';
import {useCurrentFrame} from 'remotion';
import {ease, smooth, typed} from '../anim';
import {cmd, prompt} from '../content';
import {dark} from '../theme';
import type {Item} from '../model';
import type {Line, Node} from '../ui/Panes';
import {Caption, KeySeq, Stage} from '../ui/Window';
import {App} from './common';

/** Shell opens on a shell, splits right and down, then opens a second tab. */
export const Shell: React.FC<{dur: number}> = ({dur}) => {
  const f = useCurrentFrame();
  const th = dark;
  const show = ease(f, 0, 26, smooth);
  const cmd1 = 'git switch -c feat/rate-limits';
  const t1 = typed(cmd1, f, 34, 30);
  const switched = f >= 108;
  const branch = switched ? 'feat/rate-limits' : 'main';
  const p1: Line[] = switched
    ? [cmd('~/src/acme-api', 'main', cmd1), [{t: "Switched to a new branch 'feat/rate-limits'"}], prompt('~/src/acme-api', branch)]
    : [cmd('~/src/acme-api', 'main', t1)];

  const sr = ease(f, 140, 16, smooth);
  const sd = ease(f, 212, 16, smooth);
  const right: Node = f < 212 ? {pane: 'p2'} : {dir: 'v', children: [{pane: 'p2'}, {pane: 'p3'}], ratios: [1 - 0.5 * sd, 0.5 * sd]};
  const root: Node = f < 140 ? {pane: 'p1'} : {dir: 'h', children: [{pane: 'p1'}, right], ratios: [1 - 0.5 * sr, 0.5 * sr]};
  const focused = f < 140 ? 'p1' : f < 212 ? 'p2' : 'p3';
  const t3 = typed('npm test -- --watch', f, 250, 26);
  const p3: Line[] = [cmd('~/src/acme-api', branch, t3)];

  const newTab = f >= 318;
  const nt = ease(f, 318, 14, smooth);
  const shell = {id: 'shell', title: 'acme-api', branch, time: 'just now'};
  const items: Item[] = [{kind: 'tab', tab: shell}, ...(newTab ? [{kind: 'tab' as const, tab: {id: 'shell2', title: 'acme-api', branch, time: 'just now'}}] : [])];

  return (
    <Stage cam={{opacity: show, lift: (1 - show) * 24}} window={
      <App th={th}
        side={{items, active: newTab ? 'shell2' : 'shell', frame: f, tops: newTab ? {'s:shell2': 62 - (1 - nt) * 10} : undefined}}
        panes={newTab
          ? {root: {pane: 'q1'}, panes: {q1: {lines: [prompt('~/src/acme-api', branch)], cursor: {}}}, focused: 'q1'}
          : {root, focused, panes: {
            p1: {lines: p1, cursor: {}},
            p2: {lines: [prompt('~/src/acme-api', branch)], cursor: {}},
            p3: {lines: p3, cursor: {}},
          }}}
      />
    }>
      <Caption t={f / 60} dur={204 / 60} text="Opens straight into a shell, like tmux." sub="Every tab and pane is a real terminal, drawn with real fonts." />
      {f >= 204 && <Caption t={(f - 204) / 60} dur={(dur - 204) / 60} text="Splits and tabs are one keystroke away." sub="Or one click: the + in the sidebar, the gaps between panes to resize." />}
      <KeySeq f={f} events={[[136, ['Ctrl', 'Shift', 'O']], [208, ['Ctrl', 'Shift', 'E']], [314, ['Ctrl', 'Shift', 'T']]]} />
    </Stage>
  );
};
