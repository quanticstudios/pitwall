// see: internal/ui/app/app.go — layoutPanes, paneChrome and attention.go set these sizes

import React from 'react';
import {mix, stateColor, MONO, UI, type State, type Theme, claudeOrange} from '../theme';

export type Seg = {t: string; c?: string; b?: boolean; bg?: string; dim?: boolean};
export type Line = Seg[] | string;

export const CELL_W = 7.8;
export const CELL_H = 17;
const PAD = 12;

const color = (th: Theme, c: string | undefined): string => {
  if (!c) return th.termFg;
  if (c.startsWith('#')) return c;
  const ansi: Record<string, number> = {k: 0, r: 1, g: 2, y: 3, b: 4, m: 5, c: 6, w: 7, br: 8};
  if (c in ansi) return th.ansi[ansi[c]];
  if (c === 'or') return claudeOrange;
  if (c === 'dim') return mix(th.termBg, th.termFg, 0.5);
  return th.termFg;
};

export type Cursor = {row?: number; col?: number; hidden?: boolean};

/** Terminal draws lines in the pane's grid, keeping the last rows that fit. */
export const Terminal: React.FC<{th: Theme; lines: Line[]; w: number; h: number; focused: boolean; cursor?: Cursor}> = ({th, lines, w, h, focused, cursor}) => {
  const rows = Math.max(1, Math.floor((h - 2 * PAD) / CELL_H));
  const cols = Math.max(1, Math.floor((w - 2 * PAD) / CELL_W));
  const segs = (l: Line): Seg[] => (typeof l === 'string' ? [{t: l}] : l);
  const wrapped: Seg[][] = [];
  for (const l of lines) {
    let row: Seg[] = [];
    let n = 0;
    for (const sg of segs(l)) {
      let chars = [...sg.t];
      while (n + chars.length > cols) {
        row.push({...sg, t: chars.slice(0, cols - n).join('')});
        wrapped.push(row);
        chars = chars.slice(cols - n);
        row = [];
        n = 0;
      }
      if (chars.length) row.push({...sg, t: chars.join('')});
      n += chars.length;
    }
    wrapped.push(row);
  }
  const first = Math.max(0, wrapped.length - rows);
  const shown = wrapped.slice(first);
  const last = shown.length - 1;
  const curRow = cursor?.row !== undefined ? cursor.row - first : last;
  const curCol = cursor?.col ?? (shown[last] ?? []).reduce((n, s) => n + [...s.t].length, 0);
  return (
    <div style={{position: 'absolute', left: PAD, top: PAD, width: w - 2 * PAD, height: h - 2 * PAD, overflow: 'hidden', font: `400 13px/${CELL_H}px ${MONO}`, fontVariantLigatures: 'none', color: th.termFg, whiteSpace: 'pre'}}>
      {shown.map((l, i) => (
        <div key={i} style={{height: CELL_H}}>
          {l.map((s, j) => (
            <span key={j} style={{color: color(th, s.c), fontWeight: s.b ? 700 : 400, background: s.bg ? color(th, s.bg) : undefined, opacity: s.dim ? 0.55 : 1}}>{s.t}</span>
          ))}
        </div>
      ))}
      {cursor && !cursor.hidden && (
        <div style={{position: 'absolute', left: curCol * CELL_W, top: curRow * CELL_H, width: CELL_W, height: CELL_H, boxSizing: 'border-box', background: focused ? th.cursor : undefined, border: focused ? undefined : `1px solid ${th.cursor}`}} />
      )}
    </div>
  );
};

export type Node = {pane: string} | {dir: 'h' | 'v'; children: Node[]; ratios?: number[]};
type Rect = {x: number; y: number; w: number; h: number};

/** Rects walks the split tree like walkSplits: children share the space by ratio, gap pixels apart. */
export const rects = (n: Node, r: Rect, gap: number, out: Record<string, Rect> = {}): Record<string, Rect> => {
  if ('pane' in n) {
    out[n.pane] = r;
    return out;
  }
  const total = n.dir === 'h' ? r.w : r.h;
  const avail = Math.max(0, total - gap * (n.children.length - 1));
  let cum = 0;
  let start = 0;
  n.children.forEach((c, i) => {
    cum += n.ratios?.[i] ?? 1 / n.children.length;
    const end = i === n.children.length - 1 ? avail : avail * cum;
    const cr = n.dir === 'h' ? {x: r.x + start + i * gap, y: r.y, w: end - start, h: r.h} : {x: r.x, y: r.y + start + i * gap, w: r.w, h: end - start};
    rects(c, cr, gap, out);
    start = end;
  });
  return out;
};

export type PaneContent = {lines: Line[]; cursor?: Cursor};
export type Ring = {state: State; since: number};

/** RingWidth is ringWidth: 2px steady, swelling to 4px and back twice over the first 1.2s. */
export const ringWidth = (since: number) => (since < 0 || since >= 72 ? 2 : 2 + 2 * Math.sin((Math.PI * (since % 36)) / 36));

export type PaneAreaProps = {
  th: Theme;
  w: number;
  h: number;
  root: Node;
  panes: Record<string, PaneContent>;
  focused?: string;
  lit?: string;
  rings?: Record<string, Ring>;
  zoom?: string;
  zoomLabel?: string;
  margin?: number;
  gap?: number;
};

/** PaneArea is layoutPanes: the surface canvas with one framed terminal per pane. */
export const PaneArea: React.FC<PaneAreaProps> = ({th, w, h, root, panes, focused, lit, rings = {}, zoom, zoomLabel, margin = 4, gap = 4}) => {
  const tree: Node = zoom ? {pane: zoom} : root;
  const rs = rects(tree, {x: margin, y: margin, w: w - 2 * margin, h: h - 2 * margin}, gap);
  return (
    <div style={{position: 'absolute', inset: 0, background: th.surface, overflow: 'hidden'}}>
      {Object.entries(rs).map(([id, r]) => {
        const on = id === (lit ?? focused);
        const ring = rings[id];
        const rw = ring ? ringWidth(ring.since) : 0;
        return (
          <div key={id} style={{position: 'absolute', left: r.x, top: r.y, width: r.w, height: r.h, borderRadius: 10, background: on ? mix(th.termBg, th.primary, 0.75) : mix(th.termBg, th.termFg, 0.08), overflow: 'hidden'}}>
            <div style={{position: 'absolute', inset: 1, borderRadius: 9, background: th.termBg, overflow: 'hidden'}}>
              {panes[id] && <Terminal th={th} lines={panes[id].lines} cursor={panes[id].cursor} w={r.w - 2} h={r.h - 2} focused={id === focused} />}
            </div>
            {ring && <div style={{position: 'absolute', inset: 0, borderRadius: 10, boxShadow: `inset 0 0 0 ${rw}px ${stateColor(th, ring.state)}`}} />}
            {zoom === id && zoomLabel && (
              <div style={{position: 'absolute', right: 8, top: 6, padding: '3px 8px', borderRadius: 10, background: mix(th.termBg, th.primary, 0.1), boxShadow: `inset 0 0 0 1px ${mix(th.termBg, th.primary, 0.45)}`, font: `400 11px ${UI}`, color: mix(th.muted, th.primary, 0.5)}}>{zoomLabel}</div>
            )}
          </div>
        );
      })}
    </div>
  );
};
