import React from 'react';
import {AbsoluteFill} from 'remotion';
import {Logo} from '../icons';
import {mix, UI, MONO, dark, type Theme} from '../theme';
import {SIDEBAR_W} from './Sidebar';

export const WIN_W = 1280;
export const WIN_H = 720;
export const ZOOM = 1.25;
export const ORIGIN = {x: (1920 - WIN_W * ZOOM) / 2, y: 40};

export type Cam = {s?: number; fx?: number; fy?: number; dx?: number; dy?: number; opacity?: number; lift?: number};

/** Stage is the dark backdrop with the window placed by the camera, captions and overlays on top. */
export const Stage: React.FC<{cam?: Cam; window: React.ReactNode; children?: React.ReactNode; bg?: string}> = ({cam = {}, window, children, bg}) => {
  const {s = 1, fx = WIN_W / 2, fy = WIN_H / 2, dx = 0, dy = 0, opacity = 1, lift = 0} = cam;
  return (
    <AbsoluteFill style={{background: bg ?? 'linear-gradient(180deg, #0b0d11 0%, #050608 100%)', overflow: 'hidden'}}>
      <div style={{position: 'absolute', left: ORIGIN.x, top: ORIGIN.y, width: WIN_W, height: WIN_H, transformOrigin: '0 0', transform: `translate(${dx}px, ${dy + lift}px) scale(${ZOOM})`, opacity}}>
        <div style={{position: 'absolute', inset: 0, transformOrigin: `${fx}px ${fy}px`, transform: `scale(${s})`}}>{window}</div>
      </div>
      <div style={{position: 'absolute', left: 0, right: 0, bottom: 0, height: 260, background: 'linear-gradient(180deg, rgba(5,6,8,0) 0%, rgba(5,6,8,.92) 55%, #050608 100%)', opacity: Math.min(1, Math.max(0, (s - 1) * 4)), pointerEvents: 'none'}} />
      {children}
    </AbsoluteFill>
  );
};

/** Win is the app window: sidebar on the left, pane area or settings on the right. */
export const Win: React.FC<{th: Theme; sidebar: React.ReactNode; children: React.ReactNode; pill?: React.ReactNode; overlay?: React.ReactNode; sidebarShown?: number}> = ({th, sidebar, children, pill, overlay, sidebarShown = 1}) => {
  const left = (SIDEBAR_W + 1) * sidebarShown;
  return (
    <div style={{position: 'absolute', inset: 0, borderRadius: 12, overflow: 'hidden', background: th.bg, boxShadow: '0 0 0 1px rgba(255,255,255,.09), 0 30px 80px rgba(0,0,0,.55), 0 8px 24px rgba(0,0,0,.35)'}}>
      <div style={{position: 'absolute', left, top: 0, right: 0, bottom: 0}}>{children}</div>
      {pill && <div style={{position: 'absolute', left: left + 12, bottom: 12}}>{pill}</div>}
      <div style={{position: 'absolute', left: left - SIDEBAR_W - 1, top: 0, bottom: 0, width: SIDEBAR_W + 1}}>{sidebar}</div>
      {overlay}
    </div>
  );
};

/** Keycap is the app's keycap from dialogs.go, used in the mode pill. */
export const AppKeycap: React.FC<{th: Theme; k: string}> = ({th, k}) => (
  <div style={{height: 20, minWidth: 20, padding: '0 6px', boxSizing: 'border-box', borderRadius: 4, border: `1px solid ${th.border}`, background: `linear-gradient(${th.elevated}, ${th.surface})`, display: 'grid', placeItems: 'center', font: `400 12px ${UI}`, color: mix(th.muted, th.fg, 0.54)}}>{k}</div>
);

/** ModePill is the PANE or TAB chip with the keys the mode takes. */
export const ModePill: React.FC<{th: Theme; name: string; keys: [string, string][]; hot?: string}> = ({th, name, keys, hot}) => (
  <div style={{padding: 6, borderRadius: 20, background: th.surface, boxShadow: `inset 0 0 0 1px ${mix(th.surface, th.fg, 0.14)}`, display: 'flex', alignItems: 'center', height: 28}}>
    <div style={{height: 28, padding: '0 8px', borderRadius: 14, background: mix(th.bg, th.primary, 0.16), display: 'grid', placeItems: 'center', font: `600 11px ${UI}`, color: th.primary}}>{name}</div>
    <div style={{width: 10}} />
    {keys.map(([k, label]) => (
      <div key={label} style={{display: 'flex', alignItems: 'center', gap: 5, marginRight: 12}}>
        <div style={{borderRadius: 5, boxShadow: hot === label ? `0 0 0 2px ${th.primary}` : undefined}}><AppKeycap th={th} k={k} /></div>
        <span style={{font: `400 12px ${UI}`, color: hot === label ? th.fg : th.muted}}>{label}</span>
      </div>
    ))}
  </div>
);

/** Pointer is a plain arrow cursor drawn at window coordinates, its size kept by 1/scale. */
export const Pointer: React.FC<{x: number; y: number; press?: number; scale?: number; grab?: boolean}> = ({x, y, press = 0, scale = 1, grab}) => (
  <div style={{position: 'absolute', left: x, top: y, transformOrigin: '0 0', transform: `scale(${(1 - 0.12 * press) / scale})`, pointerEvents: 'none', zIndex: 50}}>
    {press > 0 && <div style={{position: 'absolute', left: -14, top: -14, width: 28, height: 28, borderRadius: 14, background: `rgba(255,255,255,${0.16 * press})`}} />}
    {grab ? (
      <svg width="20" height="20" viewBox="0 0 24 24" style={{position: 'absolute', left: -8, top: -6}}>
        <path d="M8 11V6.5a1.5 1.5 0 0 1 3 0V10m0-1.5a1.5 1.5 0 0 1 3 0V10m0-.5a1.5 1.5 0 0 1 3 0V11m0-.5a1.5 1.5 0 0 1 3 0V15a6 6 0 0 1-6 6h-1.5a6 6 0 0 1-4.8-2.4L5 15.5a1.5 1.5 0 0 1 2.4-1.8L8 14.5V11" fill="#fff" stroke="#000" strokeWidth="1.3" strokeLinejoin="round" />
      </svg>
    ) : (
      <svg width="18" height="22" viewBox="0 0 18 22" style={{position: 'absolute', left: -1, top: -1, filter: 'drop-shadow(0 1px 2px rgba(0,0,0,.45))'}}>
        <path d="M1.5 1.5v16.2l4.1-3.9 2.7 6.3 2.9-1.2-2.7-6.2h5.8z" fill="#fff" stroke="#111" strokeWidth="1.2" strokeLinejoin="round" />
      </svg>
    )}
  </div>
);

/** KeyCombo is the on-screen keypress: small caps that pop in when a shortcut fires and fade after. */
export const KeyCombo: React.FC<{keys: string[]; t: number; label?: string; pos?: React.CSSProperties; size?: number}> = ({keys, t, label, pos, size = 1}) => {
  if (t < 0 || t > 1.05) return null;
  const inT = Math.min(1, t / 0.12);
  const out = t > 0.8 ? 1 - (t - 0.8) / 0.25 : 1;
  const press = t < 0.25 ? Math.sin((t / 0.25) * Math.PI) : 0;
  return (
    <div style={{position: 'absolute', right: ORIGIN.x, bottom: 46, ...pos, zoom: size, display: 'flex', alignItems: 'center', gap: 10, opacity: inT * out, translate: `0 ${(1 - inT) * 8}px`}}>
      {label && <span style={{font: `500 20px ${UI}`, color: '#8e939c', marginRight: 6}}>{label}</span>}
      {keys.map((k, i) => (
        <React.Fragment key={i}>
          {i > 0 && <span style={{font: `500 20px ${UI}`, color: '#5c616b'}}>+</span>}
          <div style={{height: 46, minWidth: 46, padding: '0 14px', boxSizing: 'border-box', borderRadius: 9, display: 'grid', placeItems: 'center', font: `500 21px ${UI}`, color: '#f2f3f5', background: 'linear-gradient(#262a32, #16181d)', boxShadow: `inset 0 1px 0 rgba(255,255,255,.08), 0 0 0 1px rgba(255,255,255,${0.12 + 0.2 * press}), 0 ${3 - 2 * press}px 0 #0a0b0e`, translate: `0 ${2 * press}px`}}>{k}</div>
        </React.Fragment>
      ))}
    </div>
  );
};

/** Caption is the line of copy under the window, left-aligned with it. */
export const Caption: React.FC<{text: React.ReactNode; sub?: React.ReactNode; t: number; dur: number}> = ({text, sub, t, dur}) => {
  const a = Math.min(1, Math.max(0, t / 0.35)) * Math.min(1, Math.max(0, (dur - t) / 0.3));
  return (
    <div style={{position: 'absolute', left: ORIGIN.x, bottom: 40, opacity: a, translate: `0 ${(1 - Math.min(1, t / 0.35)) * 10}px`, maxWidth: 1100}}>
      <div style={{font: `500 34px/1.2 ${UI}`, color: '#f2f3f5', letterSpacing: -0.4}}>{text}</div>
      {sub && <div style={{font: `400 21px/1.3 ${UI}`, color: '#8e939c', marginTop: 8}}>{sub}</div>}
    </div>
  );
};

export const Mono: React.FC<{children: React.ReactNode; color?: string}> = ({children, color = '#59d499'}) => (
  <span style={{font: `400 0.86em ${MONO}`, color, background: 'rgba(255,255,255,.06)', padding: '2px 8px', borderRadius: 6}}>{children}</span>
);

/** Toast is a generic desktop notification card, top right of the frame. */
export const Toast: React.FC<{title: string; body: string; t: number; color?: string}> = ({title, body, t, color = dark.yellow}) => {
  if (t < 0 || t > 3.2) return null;
  const inT = Math.min(1, t / 0.3);
  const e = 1 - Math.pow(1 - inT, 3);
  const out = t > 2.8 ? 1 - (t - 2.8) / 0.4 : 1;
  return (
    <div style={{position: 'absolute', right: 56, top: 64, width: 400, padding: '16px 18px', borderRadius: 14, background: 'rgba(24,26,31,.96)', boxShadow: '0 0 0 1px rgba(255,255,255,.1), 0 18px 50px rgba(0,0,0,.55)', display: 'flex', gap: 14, opacity: out * e, translate: `${(1 - e) * 40}px 0`}}>
      <Logo size={40} />
      <div style={{minWidth: 0}}>
        <div style={{display: 'flex', alignItems: 'center', gap: 8, font: `500 14px ${UI}`, color: '#8e939c'}}>
          pitwall <span style={{width: 7, height: 7, borderRadius: 4, background: color}} />
        </div>
        <div style={{font: `600 17px/1.3 ${UI}`, color: '#f2f3f5', marginTop: 4, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis'}}>{title}</div>
        <div style={{font: `400 16px/1.3 ${UI}`, color: '#c4c8cf', marginTop: 2}}>{body}</div>
      </div>
    </div>
  );
};

/** KeySeq shows the latest of several keypresses, each event a start frame and its keys. */
export const KeySeq: React.FC<{events: [number, string[]][]; f: number}> = ({events, f}) => {
  const live = events.filter(([at]) => f >= at).pop();
  return live ? <KeyCombo keys={live[1]} t={(f - live[0]) / 60} /> : null;
};
