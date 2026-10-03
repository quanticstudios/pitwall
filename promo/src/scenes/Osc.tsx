import React from 'react';
import {useCurrentFrame} from 'remotion';
import {testRun, viteDev} from '../content';
import {mainItems} from '../data';
import {dark} from '../theme';
import {Caption, Mono, Stage, Toast} from '../ui/Window';
import {patch, upto} from './Agents';
import {App} from './common';

export const settledItems = (f: number) =>
  patch(mainItems, {
    rate: {state: 'working', add: 214, del: 37},
    flaky: {state: 'done', time: '1m ago'},
    ml: {state: 'plan', time: '1m ago'},
    stripe: {state: 'working', time: '30s ago'},
    docs: {add: 96 + Math.floor(f / 40), del: 4},
  });

/** Osc shows a plain shell command asking for attention with OSC 9. */
export const Osc: React.FC<{dur: number}> = ({dur}) => {
  const f = useCurrentFrame();
  const th = dark;
  const done = f >= 130;
  const items = patch(settledItems(f), {dev: done ? {state: 'input', pill: undefined, unseen: 'input', time: 'just now'} : {}});
  return (
    <Stage window={
      <App th={th} side={{items, active: 'dev', frame: f}}
        panes={{
          root: {dir: 'v', children: [{pane: 'd1'}, {pane: 'd2'}], ratios: [0.42, 0.58]},
          focused: 'd1',
          panes: {d1: {lines: viteDev('~/src/web-app', 'main'), cursor: {}}, d2: {lines: upto(testRun('~/src/web-app', 'main'), f, 10, 6.5, 1)}},
          rings: done ? {d2: {state: 'input', since: f - 130}} : {},
        }}
      />
    }>
      <Toast title="web-app / npm run dev" body="Input: tests passed" t={(f - 136) / 60} />
      <Caption t={f / 60} dur={dur / 60} text="Any program can ask for your attention, no hooks needed." sub={<><Mono>printf '\e]9;tests passed\a'</Mono>{'  '}OSC 9, 777 and kitty's 99 all work.</>} />
    </Stage>
  );
};
