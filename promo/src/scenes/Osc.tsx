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

/** Osc shows a plain shell command asking for attention with pitwall notify. */
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
      <Toast title="web-app / npm run dev" body="tests passed" t={(f - 136) / 60} />
      <Caption t={f / 60} dur={dur / 60} text="Get pinged when any command finishes." sub={<>Add <Mono>pitwall notify</Mono> to a script, or use a tool that sends terminal notifications.</>} />
    </Stage>
  );
};
