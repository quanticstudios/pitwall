import React from 'react';
import {icons} from './iconData';
import {claudeOrange} from './theme';

const extra: Record<string, string> = {
  search: 'M21 21l-4.34-4.34M3 11a8 8 0 1 0 16 0a8 8 0 1 0-16 0',
  x: 'M18 6 6 18M6 6l12 12',
  minus: 'M5 12h14',
  rotate: 'M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8M3 3v5h5',
};

const path = (name: string) => icons[name] ?? icons['p:' + name] ?? extra[name];

/** Icon strokes a 24x24 Lucide path at size px with stroke width 2, as drawIcon does. */
export const Icon: React.FC<{name: string; size: number; color: string; rotate?: number; style?: React.CSSProperties}> = ({name, size, color, rotate, style}) => (
  <svg width={size} height={size} viewBox="0 0 24 24" style={{display: 'block', flex: 'none', rotate: rotate ? `${rotate}rad` : undefined, ...style}}>
    <path d={path(name)} fill="none" stroke={color} strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);

export type Agent = 'claude' | 'codex';

/** AgentMark is the Claude starburst in its orange, or the OpenAI knot in the text color. */
export const AgentMark: React.FC<{agent: Agent; size: number; fg: string}> = ({agent, size, fg}) => (
  <svg width={size} height={size} viewBox="0 0 24 24" style={{display: 'block', flex: 'none'}}>
    <path d={agent === 'claude' ? icons.icClaude : icons.icOpenAI} fill={agent === 'claude' ? claudeOrange : fg} />
  </svg>
);

/** Logo is packaging/pitwall.svg; bare drops the tile, as the sidebar header does. */
export const Logo: React.FC<{size: number; bare?: boolean; gap?: string; colors?: [string, string, string]}> = ({size, bare, gap = '#0d1016', colors = ['#57c1ff', '#ffc533', '#59d499']}) => {
  const vb = bare ? '73.5 53.5 115.5 149' : '0 0 256 256';
  const w = bare ? (size * 115.5) / 149 : size;
  const id = bare ? 'pwb' : 'pwt';
  return (
    <svg width={w} height={size} viewBox={vb} style={{display: 'block', flex: 'none'}}>
      <defs>
        <linearGradient id={`${id}bg`} x1="0" y1="0" x2="0" y2="1"><stop offset="0" stopColor="#181c24" /><stop offset="1" stopColor="#07080a" /></linearGradient>
        <linearGradient id={`${id}bowl`} x1="0" y1="0" x2="1" y2="1"><stop offset="0" stopColor="#ffffff" /><stop offset="1" stopColor="#b9c6da" /></linearGradient>
      </defs>
      {!bare && <rect x="8" y="8" width="240" height="240" rx="56" fill={`url(#${id}bg)`} />}
      {!bare && <rect x="8.5" y="8.5" width="239" height="239" rx="55.5" fill="none" stroke="#fff" strokeOpacity=".1" />}
      <path d="M91 70h50a34 34 0 0 1 0 68h-50" fill="none" stroke={`url(#${id}bowl)`} strokeWidth="28" strokeLinecap="round" strokeLinejoin="round" />
      <g stroke={gap} strokeWidth="5">
        <rect x="76" y="56" width="30" height="44" rx="15" fill={colors[0]} />
        <rect x="76" y="106" width="30" height="44" rx="15" fill={colors[1]} />
        <rect x="76" y="156" width="30" height="44" rx="15" fill={colors[2]} />
      </g>
    </svg>
  );
};
