import React from 'react';
import {AbsoluteFill, useCurrentFrame} from 'remotion';
import {ease, smooth, typed} from '../anim';
import {claudeHeader, cmd, codexHeader, codexTests, lsOutput, rateLimit, thinking} from '../content';
import {Logo} from '../icons';
import {dark, MONO, UI} from '../theme';
import {Terminal, type Line} from '../ui/Panes';
import {Caption, Mono, Stage} from '../ui/Window';
import {patch, rateRoot} from './Agents';
import {App} from './common';
import {settledItems} from './Osc';

const ShellCard: React.FC<{lines: Line[]; opacity: number}> = ({lines, opacity}) => (
  <AbsoluteFill style={{alignItems: 'center', justifyContent: 'center', opacity}}>
    <div style={{width: 1240, height: 400, borderRadius: 14, background: '#0b0c0f', boxShadow: '0 0 0 1px rgba(255,255,255,.1), 0 30px 80px rgba(0,0,0,.6)', overflow: 'hidden', position: 'relative'}}>
      <div style={{height: 40, display: 'flex', alignItems: 'center', justifyContent: 'center', font: `500 15px ${UI}`, color: '#8e939c', borderBottom: '1px solid rgba(255,255,255,.07)'}}>dev@studio: ~</div>
      <div style={{position: 'absolute', left: 14, top: 52, width: 1212, height: 340, zoom: 1.5}}>
        <Terminal th={dark} lines={lines} w={808} h={226} focused cursor={{}} />
      </div>
    </div>
  </AbsoluteFill>
);

/** Survive closes the window while tabs keep running, reopens it, then shows agents resuming after a reboot. */
export const Survive: React.FC<{dur: number}> = ({dur}) => {
  const f = useCurrentFrame();
  const th = dark;
  const close = ease(f, 24, 16);
  const back = ease(f, 180, 20, smooth);
  const reboot = ease(f, 228, 14);
  const up = ease(f, 258, 20, smooth);
  const winOpacity = f < 180 ? 1 - close : f < 228 ? back : f < 258 ? 1 - reboot : up;
  const lift = f < 180 ? close * 14 : f < 228 ? (1 - back) * 14 : f < 258 ? 0 : (1 - up) * 14;
  const cardIn = ease(f, 44, 14) * (1 - ease(f, 172, 14));
  const ls = f < 92 ? [cmd('~', undefined, typed('pitwall ls', f, 58, 18))] : [cmd('~', undefined, 'pitwall ls'), ...lsOutput, cmd('~', undefined, typed('pitwall', f, 140, 18))];

  const resumed = f >= 258;
  const left: Line[] = resumed
    ? [cmd('~/src/acme-api', undefined, 'claude --resume 4f1c9e2a'), ...claudeHeader('acme-api'), [{t: '> ', c: 'dim'}, {t: 'Add rate limiting to the public API'}], '', [{t: '● ', c: 'w'}, {t: 'Picking up where we left off: the router is wired,'}], '  the 429 response still needs a Retry-After header.', '', thinking(f, 'Adding Retry-After')]
    : [...rateLimit, thinking(f, 'Wiring the router')];
  const right: Line[] = resumed
    ? [cmd('~/src/acme-api', undefined, 'codex resume 7b20d4e1'), '', ...codexHeader, ...codexTests.slice(3, 9)]
    : codexTests;
  const items = patch(settledItems(f), resumed ? {rate: {state: f < 290 ? 'connecting' : 'working'}, stripe: {state: f < 300 ? 'connecting' : 'working'}, docs: {state: f < 280 ? 'connecting' : 'working'}, dev: {state: undefined, pill: undefined, title: 'web-app', time: 'just now'}} : {});
  return (
    <Stage cam={{opacity: winOpacity, lift}} window={
      <App th={th} side={{items, active: 'rate', frame: f}} panes={{root: rateRoot, focused: 'r1', panes: {r1: {lines: left}, r2: {lines: right}}}} />
    }>
      {cardIn > 0 && <ShellCard lines={ls} opacity={cardIn} />}
      <Caption t={f / 60} dur={222 / 60} text="Close the window. Everything keeps running." sub={<>A daemon owns the terminals; <Mono color="#f2f3f5">pitwall ls</Mono> lists them from any shell.</>} />
      {f >= 222 && <Caption t={(f - 222) / 60} dur={(dur - 222) / 60} text="After a reboot, tabs come back and agents resume." sub={<>Same folders, same groups. Panes run <Mono color="#f2f3f5">claude --resume</Mono> or <Mono color="#f2f3f5">codex resume</Mono>.</>} />}
    </Stage>
  );
};

/** Outro is the logo, the install line and where to find the code. */
export const Outro: React.FC<{dur: number}> = () => {
  const f = useCurrentFrame();
  const a = ease(f, 0, 30, smooth);
  const b = ease(f, 18, 30, smooth);
  const c = ease(f, 36, 30, smooth);
  return (
    <AbsoluteFill style={{background: 'linear-gradient(180deg, #0b0d11 0%, #050608 100%)', alignItems: 'center', justifyContent: 'center'}}>
      <div style={{display: 'flex', alignItems: 'center', gap: 22, opacity: a, translate: `0 ${(1 - a) * 12}px`}}>
        <Logo size={92} />
        <div style={{font: `600 76px ${UI}`, color: '#f2f3f5', letterSpacing: -2}}>pitwall</div>
      </div>
      <div style={{marginTop: 46, opacity: b, translate: `0 ${(1 - b) * 12}px`, padding: '20px 28px', borderRadius: 12, background: '#0f1115', boxShadow: '0 0 0 1px rgba(255,255,255,.1)', font: `400 22px ${MONO}`, color: '#f2f3f5', whiteSpace: 'nowrap'}}>
        <span style={{color: '#59d499'}}>$ </span>curl -fsSL https://raw.githubusercontent.com/quanticstudios/pitwall/main/scripts/get.sh | sh
      </div>
      <div style={{marginTop: 14, opacity: b, font: `400 17px ${MONO}`, color: '#8e939c'}}>
        Windows, in PowerShell: irm https://raw.githubusercontent.com/quanticstudios/pitwall/main/scripts/get.ps1 | iex
      </div>
      <div style={{marginTop: 34, textAlign: 'center', opacity: c}}>
        <div style={{font: `500 30px ${UI}`, color: '#f2f3f5'}}>Linux, macOS and Windows. Open source, MIT.</div>
        <div style={{font: `400 26px ${UI}`, color: '#8e939c', marginTop: 12}}>github.com/quanticstudios/pitwall</div>
      </div>
    </AbsoluteFill>
  );
};
