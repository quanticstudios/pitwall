import React from 'react';
import {useCurrentFrame} from 'remotion';
import {track} from '../anim';
import {codexTests, rateLimit, stripeAfter, stripeAsk, thinking} from '../content';
import {mainItems} from '../data';
import {withTab, type Item, type Tab} from '../model';
import {dark} from '../theme';
import type {Line, Node} from '../ui/Panes';
import {Caption, KeySeq, Stage, Toast} from '../ui/Window';
import {App} from './common';
import type {PaneAreaProps} from '../ui/Panes';

export type PaneSet = Omit<PaneAreaProps, 'th' | 'w' | 'h'>;

export const patch = (items: Item[], changes: Record<string, Partial<Tab>>) =>
  Object.entries(changes).reduce((acc, [id, p]) => withTab(acc, id, p), items);

/** Upto reveals the first n lines of lines, n growing by perSec from start. */
export const upto = (lines: Line[], f: number, start: number, perSec: number, min = 0) =>
  lines.slice(0, Math.max(min, Math.min(lines.length, min + Math.floor(((f - start) / 60) * perSec))));

export const rateRoot: Node = {dir: 'h', children: [{pane: 'r1'}, {pane: 'r2'}], ratios: [0.54, 0.46]};

/** Agents fills the sidebar with Claude Code and Codex tabs whose states change live. */
export const Agents: React.FC<{dur: number}> = ({dur}) => {
  const f = useCurrentFrame();
  const th = dark;
  const items = patch(mainItems, {
    rate: {title: f < 96 ? 'claude' : 'Add rate limiting to the public API', add: Math.round(track(f, [[60, 0], [520, 214]])), del: Math.round(track(f, [[60, 0], [520, 37]]))},
    docs: {state: f < 70 ? 'connecting' : 'working'},
    flaky: f < 300 ? {} : {state: 'done', time: 'just now'},
    ml: f < 380 ? {state: 'working'} : {state: 'plan', time: 'just now'},
  });
  const left = [...upto(rateLimit, f, 20, 3.2, 6), thinking(f, f < 300 ? 'Writing middleware' : 'Wiring the router')];
  const right = upto(codexTests, f, 40, 2.6, 4);
  const s = track(f, [[120, 1], [190, 1.5], [470, 1.5], [540, 1]]);
  const fy = track(f, [[190, 250], [440, 430]]);
  return (
    <Stage cam={{s, fx: 0, fy}} window={
      <App th={th}
        side={{items, active: 'rate', frame: f}}
        panes={{root: rateRoot, focused: 'r1', panes: {r1: {lines: left}, r2: {lines: right}}}}
      />
    }>
      <Caption t={f / 60} dur={dur / 60} text="Claude Code and Codex report their state in the sidebar." sub="Working, Input, Approval, Plan, Done, Error. Tabs title themselves after the work." />
      <KeySeq f={f} events={[[264, ['Ctrl', 'Shift', 'U']], [326, ['1']], [414, ['Ctrl', 'Shift', 'U']]]} />
    </Stage>
  );
};

/** Attention rings a pane that needs you, marks rows in other tabs, and jumps there. */
export const Attention: React.FC<{dur: number}> = ({dur}) => {
  const f = useCurrentFrame();
  const th = dark;
  const onStripe = f >= 270 && f < 420;
  const items = patch(mainItems, {
    rate: {state: 'working', unseen: f >= 60 && f < 420 ? 'done' : undefined, add: 214, del: 37},
    flaky: {state: 'done', time: '20s ago'},
    ml: {state: 'plan', time: '30s ago'},
    stripe: f < 150 ? {} : f < 330 ? {state: 'approval', time: 'just now', unseen: f < 270 ? 'approval' : undefined} : {state: 'working', time: 'just now'},
  });
  const left = [...rateLimit, thinking(f, 'Wiring the router')];
  const right = f < 60 ? codexTests.slice(0, -4) : codexTests;
  const stripeLines = f < 330 ? stripeAsk : [...stripeAfter, thinking(f, 'Updating webhooks')];
  const panes: PaneSet = onStripe
    ? {root: {pane: 's1'} as Node, focused: 's1', panes: {s1: {lines: stripeLines}}}
    : {root: rateRoot, focused: f < 420 ? 'r1' : 'r2', panes: {r1: {lines: left}, r2: {lines: right}}, rings: f >= 60 && f < 420 ? {r2: {state: 'done' as const, since: f - 60}} : {}};
  return (
    <Stage window={<App th={th} side={{items, active: onStripe ? 'stripe' : 'rate', frame: f}} panes={panes} />}>
      <Toast title="billing / Migrate billing to Stripe v3" body="Agent Approval" t={(f - 156) / 60} />
      <Caption t={f / 60} dur={262 / 60} text="When an agent needs you, its pane rings and its row lights up." sub="Amber for input and approval, green for done, purple for a plan, red for an error." />
      {f >= 262 && <Caption t={(f - 262) / 60} dur={(dur - 262) / 60} text="Ctrl+Shift+U jumps to the newest tab that needs you." sub="Press it again for the next one. The mark clears once you look." />}
    </Stage>
  );
};

