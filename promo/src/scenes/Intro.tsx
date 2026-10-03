import React from 'react';
import {AbsoluteFill, useCurrentFrame} from 'remotion';
import {ease, smooth} from '../anim';
import {Logo} from '../icons';
import {UI} from '../theme';

/** Intro is the logo, the name and one line on what pitwall is. */
export const Intro: React.FC<{dur: number}> = ({dur}) => {
  const f = useCurrentFrame();
  const a = ease(f, 6, 30, smooth);
  const b = ease(f, 26, 30, smooth);
  const c = ease(f, 52, 30, smooth);
  const out = 1 - ease(f, dur - 22, 22);
  return (
    // The background stays opaque and matches the next scene's; fading it too
    // flashed black and split the text into dark boxes mid-fade.
    <AbsoluteFill style={{background: 'linear-gradient(180deg, #0b0d11 0%, #050608 100%)', alignItems: 'center', justifyContent: 'center'}}>
      <div style={{display: 'flex', flexDirection: 'column', alignItems: 'center', opacity: out}}>
      <div style={{display: 'flex', alignItems: 'center', gap: 30}}>
        <div style={{opacity: a, scale: `${0.92 + 0.08 * a}`}}>
          <Logo size={132} />
        </div>
        <div style={{font: `600 104px ${UI}`, color: '#f2f3f5', letterSpacing: -3, opacity: b, translate: `${(1 - b) * -16}px 0`}}>pitwall</div>
      </div>
      <div style={{marginTop: 34, textAlign: 'center', opacity: c, translate: `0 ${(1 - c) * 10}px`}}>
        <div style={{font: `500 40px ${UI}`, color: '#f2f3f5', letterSpacing: -0.6}}>A terminal multiplexer for coding agents.</div>
        <div style={{font: `400 26px ${UI}`, color: '#8e939c', marginTop: 12}}>A native window with real type and motion, and the keys you already know.</div>
      </div>
      </div>
    </AbsoluteFill>
  );
};
