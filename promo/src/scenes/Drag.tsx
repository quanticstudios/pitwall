// see: internal/ui/sidebar/drag.go — lift 120ms, slides 140ms, 300ms dwell on a group header

import React from 'react';
import {useCurrentFrame} from 'remotion';
import {ease, lerp, track, outCubic} from '../anim';
import {groups, tabs} from '../data';
import type {Item, Tab} from '../model';
import {dark} from '../theme';
import {place, type Elem} from '../ui/Sidebar';
import {Caption, Pointer, Stage} from '../ui/Window';
import {App} from './common';
import {settledItems} from './Osc';
import {rateRoot} from './Agents';
import {codexTests, rateLimit, thinking} from '../content';

const perf: Tab = {id: 'perf', title: 'Profile the checkout page', agent: 'claude', state: 'working', branch: 'perf/checkout', add: 57, del: 21, time: '2m ago'};

const base = (f: number) => {
  const s = settledItems(f);
  const t = (id: string) => {
    for (const it of s) {
      if (it.kind === 'tab' && it.tab.id === id) return it.tab;
      if (it.kind === 'group') for (const x of it.tabs) if (x.id === id) return x;
    }
    return tabs.home;
  };
  return {docs: t('docs'), ml: t('ml'), rate: t('rate'), flaky: t('flaky'), stripe: t('stripe'), dev: t('dev')};
};

const tops = (e: Elem[]) => Object.fromEntries(e.map((x) => [x.key, x.top]));

const mixTops = (a: Record<string, number>, b: Record<string, number>, t: number) =>
  Object.fromEntries(Object.keys(b).map((k) => [k, lerp(a[k] ?? b[k], b[k], t)]));

const HEAD = 56;

/** Drag drops a loose tab into a group, then moves a whole group above the loose tabs. */
export const Drag: React.FC<{dur: number}> = ({dur}) => {
  const f = useCurrentFrame();
  const th = dark;
  const b = base(f);
  const A: Item[] = [
    {kind: 'tab', tab: b.docs}, {kind: 'tab', tab: perf}, {kind: 'tab', tab: b.ml},
    {kind: 'group', group: groups.api, tabs: [b.rate, b.flaky]},
    {kind: 'group', group: groups.billing, tabs: [b.stripe]},
    {kind: 'group', group: groups.web, tabs: [b.dev]},
  ];
  const A1 = A.filter((it) => !(it.kind === 'tab' && it.tab.id === 'perf'));
  const B: Item[] = A1.map((it) => (it.kind === 'group' && it.group.id === 'web' ? {...it, tabs: [b.dev, perf]} : it));
  const B1 = B.filter((it) => !(it.kind === 'group' && it.group.id === 'billing'));
  const C: Item[] = [B.find((it) => it.kind === 'group' && it.group.id === 'billing')!, ...B1];
  const L0 = tops(place(A));
  const L1 = tops(place(A1));
  const L2 = tops(place(B));
  const L2b = tops(place(B1));
  const L3 = tops(place(C));

  let items = A;
  let t: Record<string, number> = {};
  let hidden: string[] = [];
  let ghost;
  let drop: string | undefined;
  let dropAlpha = 1;
  let hover: string | undefined;
  let px: number;
  let py: number;
  let grab = false;
  let press = 0;

  if (f < 230) {
    const path = track(f, [[20, 0], [70, 1]], outCubic);
    const down = track(f, [[85, 0], [150, 1]]);
    px = f < 85 ? lerp(760, 150, path) : lerp(150, 160, down);
    py = f < 85 ? lerp(420, 146, path) : lerp(146, HEAD + 447, down);
    press = f >= 75 && f < 82 ? Math.sin(((f - 75) / 7) * Math.PI) : 0;
    if (f >= 70 && f < 75) hover = 's:perf';
    if (f >= 75 && f < 194) {
      grab = f < 185;
      hidden = ['s:perf'];
      t = mixTops(L0, L1, ease(f, 95, 8));
      if (f >= 168) t = mixTops(L1, L2, ease(f, 168, 8));
      const lift = f < 185 ? ease(f, 75, 7) : 1 - ease(f, 185, 8);
      const gy = f < 185 ? py - HEAD - 28 : lerp(py - HEAD - 28, L2['s:perf'], ease(f, 185, 8));
      ghost = {key: 's:perf', y: gy, lift};
      if (f >= 168 && f < 186) {
        drop = 'web';
        dropAlpha = ease(f, 168, 4);
      }
    } else if (f >= 194) {
      items = B;
    }
  } else {
    items = B;
    const path = track(f, [[240, 0], [285, 1]], outCubic);
    const up = track(f, [[305, 0], [375, 1]]);
    px = f < 305 ? lerp(160, 120, path) : lerp(120, 132, up);
    py = f < 305 ? lerp(HEAD + 447, HEAD + 330, path) : lerp(HEAD + 330, HEAD + 14, up);
    press = f >= 295 && f < 302 ? Math.sin(((f - 295) / 7) * Math.PI) : 0;
    if (f >= 285 && f < 295) hover = 'g:billing';
    if (f >= 295 && f < 404) {
      grab = f < 395;
      hidden = ['g:billing', 's:stripe'];
      t = mixTops(L2, L2b, ease(f, 302, 8));
      if (f >= 352) t = mixTops(L2b, L3, ease(f, 352, 8));
      const lift = f < 395 ? ease(f, 295, 7) : 1 - ease(f, 395, 8);
      const head = L2['g:billing'] + 13;
      const gy0 = py - HEAD - 20;
      const gy = f < 395 ? (f < 300 ? head : gy0) : lerp(gy0, 0, ease(f, 395, 8));
      ghost = {key: 'g:billing', y: gy, lift};
    } else if (f >= 404) {
      items = C;
    }
  }

  const s = 1.35;
  const fy = track(f, [[0, 330], [200, 330], [260, 110]]);
  return (
    <Stage cam={{s, fx: 0, fy}} window={
      <App th={th}
        side={{items, active: 'rate', frame: f, tops: t, hidden, ghost, dropGroup: drop, dropAlpha, hover}}
        panes={{root: rateRoot, focused: 'r1', panes: {r1: {lines: [...rateLimit, thinking(f, 'Wiring the router')]}, r2: {lines: codexTests}}}}
        overlay={<Pointer x={px} y={py} press={press} scale={s} grab={grab} />}
      />
    }>
      <Caption t={f / 60} dur={dur / 60} text="Drag tabs into groups, and groups above loose tabs." sub="Rows slide apart to show where it lands. Right-click a tab to group everything in its folder." />
    </Stage>
  );
};
