import React from 'react';
import {AbsoluteFill, useCurrentFrame} from 'remotion';
import {pick} from './anim';
import {codexFlaky, codexTests, rateLimit, stripeAfter, stripeAsk, thinking} from './content';
import {mainItems} from './data';
import {dark} from './theme';
import type {Line, Node} from './ui/Panes';
import {KeyCombo, Pointer, ZOOM} from './ui/Window';
import {patch, rateRoot, type PaneSet} from './scenes/Agents';
import {App} from './scenes/common';
import {path, press} from './scenes/SettingsScene';

// invariant: 720 frames at 60fps timing, a whole number of pulse (120), shimmer (180) and spinner (36) periods
export const HERO_FRAMES = 360;
const LOOP = 720;

const codexWorking: Line[] = [...codexTests.slice(0, -4), [{t: '◦ ', c: 'dim'}, {t: 'Working ', b: true}, {t: '(esc to interrupt)', c: 'dim'}]];

/** Hero is the README loop: agents change state, a pane rings, and the jump key walks the tabs that need you. */
export const Hero: React.FC = () => {
  const F = (useCurrentFrame() * 2) % LOOP;
  const th = dark;
  const active = pick<string>(F, [[0, 'rate'], [330, 'stripe'], [470, 'rate'], [540, 'flaky'], [606, 'rate']]);
  const r2Done = F >= 140 && F < 660;
  const items = patch(mainItems, {
    rate: {state: 'working', add: 214, del: 37, title: 'Add rate limiting to the public API', unseen: r2Done && F < 470 ? 'done' : undefined},
    flaky: F >= 60 && F < 680 ? {state: 'done', time: 'just now', unseen: F < 540 ? 'done' : undefined} : {},
    stripe: F < 220 ? {} : F < 400 ? {state: 'approval', time: 'just now', unseen: F < 330 ? 'approval' : undefined} : {state: 'working', time: 'just now'},
  });
  const rateFocus = F >= 470 && F < 646 ? 'r2' : 'r1';
  let panes: PaneSet;
  if (active === 'stripe') {
    panes = {root: {pane: 's1'} as Node, focused: 's1', panes: {s1: {lines: F < 400 ? stripeAsk : [...stripeAfter, thinking(F, 'Updating webhooks')]}}};
  } else if (active === 'flaky') {
    panes = {root: {pane: 'f1'} as Node, focused: 'f1', panes: {f1: {lines: codexFlaky, cursor: {}}}};
  } else {
    panes = {
      root: rateRoot, focused: rateFocus,
      panes: {r1: {lines: [...rateLimit, thinking(F, 'Wiring the router')]}, r2: {lines: r2Done ? codexTests : codexWorking}},
      rings: r2Done && F < 470 ? {r2: {state: 'done', since: F - 140}} : {},
    };
  }
  const p = path(F, [[560, 760, 760], [600, 140, 321], [622, 140, 321], [642, 520, 300], [690, 520, 300], [720, 760, 760]]);
  const showPointer = F >= 560 && F < 712;
  const keys: [number, string[]][] = [[326, ['Ctrl', 'Shift', 'U']], [396, ['1']], [466, ['Ctrl', 'Shift', 'U']], [536, ['Ctrl', 'Shift', 'U']]];
  const live = keys.filter(([at]) => F >= at).pop();
  return (
    <AbsoluteFill style={{background: th.bg}}>
      <div style={{position: 'absolute', left: 0, top: 0, width: 1280, height: 720, transformOrigin: '0 0', transform: `scale(${ZOOM})`}}>
        <App th={th} side={{items, active, frame: F}} panes={panes}
          overlay={showPointer ? <Pointer x={p.x} y={p.y} press={press(F, [600, 642])} /> : undefined} />
      </div>
      {live && <KeyCombo keys={live[1]} t={(F - live[0]) / 60} pos={{right: 28, bottom: 28}} size={0.9} />}
    </AbsoluteFill>
  );
};
