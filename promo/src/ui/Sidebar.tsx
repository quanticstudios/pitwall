// see: internal/ui/sidebar/sidebar.go — every size and color here is ported from it

import React from 'react';
import {AgentMark, Icon, Logo} from '../icons';
import {FPS, pulse} from '../anim';
import {pillText, type Group, type Item, type Tab} from '../model';
import {mix, stateColor, UI, MONO, type Theme} from '../theme';

export const SIDEBAR_W = 288;
export const ROW_H = 56;
const HEAD_H = 56;
const GROUP_H = 40;

const projectColors: Record<string, string> = {
  red: '#fb2c36', orange: '#ff6900', yellow: '#fdc700', lime: '#7ccf00', emerald: '#00bc7d', teal: '#00bba7',
  cyan: '#00b8db', sky: '#00a6f4', indigo: '#615fff', violet: '#a684ff', purple: '#ad46ff', fuchsia: '#e12afb',
  pink: '#f6339a', rose: '#ff2056',
};
const projectColor = (th: Theme, id: string) =>
  id === 'blue' ? th.blue : id === 'green' ? th.green : id === 'amber' ? th.yellow : projectColors[id] ?? th.muted;

export type Elem = {key: string; kind: 's' | 'g'; top: number; head: number; tab?: Tab; group?: Group; groupId?: string; count?: number};

/** Place lays the tree out like Sidebar.place: runs of rows 2px apart with 4px ends, groups behind a 13px separator. */
export const place = (items: Item[]): Elem[] => {
  const out: Elem[] = [];
  let y = 0;
  let run = false;
  const row = (tab: Tab, groupId?: string) => {
    y += run ? 2 : 4;
    out.push({key: 's:' + tab.id, kind: 's', top: y, head: y, tab, groupId});
    y += ROW_H;
    run = true;
  };
  const endRun = () => {
    if (run) {
      y += 4;
      run = false;
    }
  };
  items.forEach((it, i) => {
    if (it.kind === 'tab') return row(it.tab);
    endRun();
    const top = y;
    if (i > 0) y += 13;
    out.push({key: 'g:' + it.group.id, kind: 'g', top, head: y, group: it.group, count: it.tabs.filter((t) => t.unseen).length});
    y += GROUP_H;
    if (!it.collapsed) {
      it.tabs.forEach((t) => row(t, it.group.id));
      if (it.tabs.length === 0) y += 8;
      endRun();
    }
  });
  return out;
};

const rowBase = (th: Theme, t: Tab, active: boolean, hovered: boolean) => {
  let base = th.sidebar;
  if (t.state === 'error') base = mix(base, th.red, 0.03);
  if (t.state === 'approval' || t.state === 'input') base = mix(base, th.yellow, 0.03);
  if (t.state === 'plan') base = mix(base, th.purple, 0.15);
  if (active) base = mix(base, th.primary, 0.12);
  else if (!t.state && hovered) base = th.surface2;
  return base;
};

const ellipsis: React.CSSProperties = {whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', minWidth: 0};

const StateIcon: React.FC<{th: Theme; t: Tab; active: boolean; base: string; frame: number}> = ({th, t, active, base, frame}) => {
  if (t.agent) {
    return (
      <div style={{width: 12, height: 12, position: 'relative', flex: 'none'}}>
        <div style={{position: 'absolute', left: -1, top: -1}}>
          <AgentMark agent={t.agent} size={14} fg={th.fg} />
        </div>
      </div>
    );
  }
  if (!t.state) return <Icon name={t.branch ? 'icGitBranch' : 'icTerminal'} size={12} color={active ? th.primary : th.muted} />;
  switch (t.state) {
    case 'error': return <Icon name="icCircleAlert" size={12} color={th.red} />;
    case 'approval': return <Icon name="icCircleAlert" size={12} color={th.yellow} />;
    case 'input': return <Icon name="icCircleHelp" size={12} color={th.yellow} />;
    case 'done': return <Icon name="icCircleCheck" size={12} color={th.green} />;
    case 'plan': return <Icon name="icCircleCheck" size={12} color={th.purple} />;
    case 'running': return <Icon name="icTerminal" size={12} color={th.green} />;
    case 'connecting': return <Icon name="icLoader" size={12} color={th.blue} rotate={((frame / FPS) % 1) * Math.PI * 2} />;
  }
  return (
    <div style={{width: 12, height: 12, display: 'grid', placeItems: 'center', flex: 'none'}}>
      <div style={{width: 6, height: 6, borderRadius: 3, background: mix(base, th.blue, pulse(frame))}} />
    </div>
  );
};

const Pill: React.FC<{th: Theme; t: Tab; base: string; frame: number}> = ({th, t, base, frame}) => {
  const col = stateColor(th, t.state!);
  const bg = mix(base, col, t.state === 'plan' ? 0.15 : 0.14);
  const pulses = t.state === 'working' || t.state === 'connecting';
  return (
    <div style={{height: 16.5, padding: '0 7px', borderRadius: 9, background: bg, display: 'flex', alignItems: 'center', gap: 4, flex: 'none'}}>
      {pulses && <div style={{width: 6, height: 6, borderRadius: 3, background: mix(bg, col, pulse(frame))}} />}
      <span style={{font: `600 ${10 * (th.ui ?? 1)}px ${UI}`, color: col, whiteSpace: 'nowrap'}}>{pillText(t)}</span>
    </div>
  );
};

// why: .sidebar-working-shimmer, a 200%-wide white sweep on a 3s ease-in-out loop
const Shimmer: React.FC<{w: number; frame: number}> = ({w, frame}) => {
  const p = (frame / FPS / 3) % 1;
  const e = p * p * (3 - 2 * p);
  const c = (((w * (3 - 4 * e)) % (2 * w)) + 2 * w) % (2 * w);
  const g = 'linear-gradient(90deg, rgba(255,255,255,0) 0%, rgba(255,255,255,.02) 40%, rgba(255,255,255,.04) 50%, rgba(255,255,255,.02) 60%, rgba(255,255,255,0) 100%)';
  return (
    <>
      {[c - 2 * w, c, c + 2 * w].map((cx, i) => (
        <div key={i} style={{position: 'absolute', top: 0, bottom: 0, left: cx - w, width: 2 * w, background: g}} />
      ))}
    </>
  );
};

export type RowProps = {th: Theme; t: Tab; active?: boolean; hovered?: boolean; selected?: boolean; ghost?: boolean; frame: number; w?: number};

/** Row is workspaceRow: mark, title and pill, then branch, diff stats and time. */
export const Row: React.FC<RowProps> = ({th, t, active = false, hovered = false, selected = false, ghost = false, frame, w = SIDEBAR_W - 1}) => {
  const unseen = ghost ? undefined : t.unseen;
  let base = rowBase(th, t, active, hovered);
  if (selected && !active) base = mix(base, th.primary, 0.07);
  if (unseen) base = mix(base, stateColor(th, unseen), 0.1);
  if (ghost) base = th.surface2;
  const nameCol = active || ghost || unseen || t.state === 'approval' || t.state === 'input' || t.state === 'error' ? th.fg : mix(base, th.fg, 0.95);
  const muted = mix(base, th.muted, 0.7);
  const quiet = mix(base, th.muted, 0.45);
  const inRepo = !!t.branch;
  return (
    <div style={{position: 'relative', width: w, height: ROW_H, borderRadius: 8, background: ghost || base === th.sidebar ? undefined : base, overflow: 'hidden'}}>
      {!ghost && (t.state === 'working' || t.state === 'connecting') && <Shimmer w={w} frame={frame} />}
      {selected && <div style={{position: 'absolute', inset: 1, borderRadius: 7, border: `1px solid ${mix(base, th.primary, 0.55)}`}} />}
      {unseen && <div style={{position: 'absolute', left: 2, top: 10, width: 3, height: ROW_H - 20, borderRadius: 1.5, background: stateColor(th, unseen)}} />}
      <div style={{position: 'absolute', left: 12, top: 8, width: w - 52, height: 19.5, display: 'flex', alignItems: 'center', gap: 8}}>
        <StateIcon th={th} t={t} active={active} base={base} frame={frame} />
        <span style={{font: `600 ${13 * (th.ui ?? 1)}px ${UI}`, color: nameCol, flex: '0 1 auto', ...ellipsis}}>{t.title}</span>
        <div style={{flex: 1, minWidth: 0}} />
        {unseen && <div style={{width: 6, height: 6, borderRadius: 3, background: stateColor(th, unseen), flex: 'none'}} />}
        {t.state && <Pill th={th} t={t} base={base} frame={frame} />}
      </div>
      <div style={{position: 'absolute', left: 32, top: 31.5, width: w - 72, height: 16.5, display: 'flex', alignItems: 'center', gap: 6}}>
        <span style={{font: `400 11px ${MONO}`, color: muted, flex: '0 1 auto', ...ellipsis}}>{inRepo ? t.branch : t.path}</span>
        {inRepo && (t.add || t.del) ? (
          <span style={{display: 'flex', gap: 4, flex: 'none', font: `600 ${10 * (th.ui ?? 1)}px ${UI}`}}>
            <span style={{color: th.green}}>+{t.add}</span>
            <span style={{color: th.red}}>-{t.del}</span>
          </span>
        ) : null}
        <div style={{flex: 1}} />
        <span style={{font: `400 ${11 * (th.ui ?? 1)}px ${UI}`, color: quiet, flex: 'none'}}>{t.time}</span>
      </div>
      {hovered && !ghost && (
        <div style={{position: 'absolute', right: 4, top: 6, display: 'flex', gap: 2}}>
          <div style={{width: 24, height: 24, display: 'grid', placeItems: 'center'}}><Icon name="icPlus" size={14} color={th.muted} /></div>
          <div style={{width: 24, height: 24, display: 'grid', placeItems: 'center'}}><Icon name="icEllipsis" size={16} color={th.muted} /></div>
        </div>
      )}
    </div>
  );
};

/** GroupHeader is projectHeader: the group's icon and name, an attention count and its plus button. */
export const GroupHeader: React.FC<{th: Theme; g: Group; count: number; activeGroup?: boolean; hovered?: boolean; ghost?: boolean; w?: number}> = ({th, g, count, activeGroup, hovered, ghost, w = SIDEBAR_W - 1}) => {
  const bg = hovered ? th.surface2 : th.sidebar;
  const nameCol = activeGroup || ghost ? th.fg : mix(bg, th.fg, 0.8);
  return (
    <div style={{position: 'relative', width: w, height: GROUP_H, borderRadius: 8, background: hovered ? th.surface2 : undefined}}>
      <div style={{position: 'absolute', left: 14, top: 0, height: GROUP_H, width: w - 86, display: 'flex', alignItems: 'center', gap: 8}}>
        <Icon name={g.icon} size={14} color={projectColor(th, g.color)} />
        <span style={{font: `600 ${14 * (th.ui ?? 1)}px ${UI}`, color: nameCol, ...ellipsis}}>{g.name}</span>
        {count > 0 && (
          <div style={{width: 16, height: 16, borderRadius: 8, background: mix(bg, th.yellow, 0.14), display: 'grid', placeItems: 'center', flex: 'none'}}>
            <span style={{font: `600 ${9 * (th.ui ?? 1)}px ${UI}`, color: th.yellow}}>{count}</span>
          </div>
        )}
      </div>
      {!ghost && (
        <div style={{position: 'absolute', left: w - 60, top: 8, width: 24, height: 24, display: 'grid', placeItems: 'center'}}>
          <Icon name="icPlus" size={14} color={th.muted} />
        </div>
      )}
      {hovered && !ghost && (
        <div style={{position: 'absolute', left: w - 32, top: 8, width: 24, height: 24, display: 'grid', placeItems: 'center'}}>
          <Icon name="icEllipsis" size={16} color={th.muted} />
        </div>
      )}
    </div>
  );
};

export type Ghost = {key: string; y: number; lift: number; badge?: number};

export type SidebarProps = {
  th: Theme;
  items: Item[];
  active?: string;
  frame: number;
  tops?: Record<string, number>;
  hidden?: string[];
  ghost?: Ghost;
  dropGroup?: string;
  dropAlpha?: number;
  hover?: string;
  selected?: string[];
  settingsOn?: boolean;
  height: number;
};

/** Sidebar draws the header, the tree from items (each element at tops[key] when given) and the footer. */
export const Sidebar: React.FC<SidebarProps> = ({th, items, active, frame, tops = {}, hidden = [], ghost, dropGroup, dropAlpha = 1, hover, selected = [], settingsOn, height}) => {
  const elems = place(items);
  const w = SIDEBAR_W - 1;
  const activeGroup = elems.find((e) => e.tab?.id === active)?.groupId;
  const ghostElem = ghost && elems.find((e) => e.key === ghost.key);
  return (
    <div style={{position: 'absolute', left: 0, top: 0, width: SIDEBAR_W, height, background: th.sidebar, overflow: 'hidden'}}>
      <div style={{position: 'absolute', left: 0, top: 0, width: w, height: HEAD_H, borderBottom: `1px solid ${th.border}`, boxSizing: 'border-box'}}>
        <div style={{position: 'absolute', left: 18, right: 18, top: 0, height: HEAD_H - 1, display: 'flex', alignItems: 'center', gap: 8}}>
          <div style={{width: 22, display: 'flex', justifyContent: 'center'}}>
            <Logo size={22} bare gap={th.sidebar} colors={[th.blue, th.yellow, th.green]} />
          </div>
          <span style={{font: `600 ${14 * (th.ui ?? 1)}px ${UI}`, color: th.fg, flex: 1}}>pitwall</span>
          <div style={{width: 28, height: 28, display: 'grid', placeItems: 'center'}}><Icon name="icPlus" size={16} color={th.muted} /></div>
        </div>
      </div>
      <div style={{position: 'absolute', left: 0, top: HEAD_H, width: w, height: height - HEAD_H - 45, overflow: 'hidden'}}>
        {elems.map((e) => {
          if (hidden.includes(e.key)) return null;
          const y = tops[e.key] ?? e.top;
          if (e.kind === 'g') {
            return (
              <div key={e.key} style={{position: 'absolute', left: 0, top: y, width: w}}>
                {e.head > e.top && <div style={{position: 'absolute', left: 0, top: 6, width: w, height: 1, background: mix(th.sidebar, th.border, 0.6)}} />}
                <div style={{position: 'absolute', left: 0, top: e.head - e.top}}>
                  <GroupHeader th={th} g={e.group!} count={e.count!} activeGroup={activeGroup === e.group!.id} hovered={hover === e.key} />
                </div>
              </div>
            );
          }
          return (
            <div key={e.key} style={{position: 'absolute', left: 0, top: y}}>
              <Row th={th} t={e.tab!} active={e.tab!.id === active} hovered={hover === e.key} selected={selected.includes(e.tab!.id)} frame={frame} />
            </div>
          );
        })}
        {dropGroup && (() => {
          const g = elems.find((e) => e.key === 'g:' + dropGroup);
          if (!g) return null;
          const y = (tops[g.key] ?? g.top) + g.head - g.top;
          return (
            <div style={{position: 'absolute', left: 4, top: y, width: w - 8, height: GROUP_H, borderRadius: 8, opacity: dropAlpha, background: mix(th.sidebar, th.primary, 0.14), boxShadow: `inset 0 0 0 1.5px ${mix(th.sidebar, th.primary, 0.7)}`}}>
              <div style={{position: 'absolute', left: 10, top: 0}}>
                <GroupHeader th={{...th, sidebar: mix(th.sidebar, th.primary, 0.14)}} g={g.group!} count={g.count!} ghost w={w - 8} />
              </div>
            </div>
          );
        })()}
        {ghost && ghostElem && (
          <div style={{position: 'absolute', left: 0, top: ghost.y, width: w, scale: `${1 + 0.025 * ghost.lift}`}}>
            <div style={{position: 'relative', borderRadius: 8, background: th.surface2, boxShadow: `0 0 0 1px ${mix(th.surface2, th.fg, 0.12)}, 0 ${4 * ghost.lift}px ${6 * ghost.lift}px rgba(0,0,0,${0.16 * ghost.lift}), 0 ${8 * ghost.lift}px ${14 * ghost.lift}px rgba(0,0,0,${0.1 * ghost.lift})`}}>
              {ghostElem.kind === 's' ? (
                <Row th={th} t={ghostElem.tab!} ghost frame={frame} />
              ) : (
                <GroupHeader th={{...th, sidebar: th.surface2}} g={ghostElem.group!} count={0} ghost />
              )}
              {ghost.badge && ghost.badge > 1 && (
                <div style={{position: 'absolute', right: -6, top: -6, minWidth: 18, height: 18, borderRadius: 9, background: th.primary, color: th.onPrimary, font: `600 ${10 * (th.ui ?? 1)}px ${UI}`, display: 'grid', placeItems: 'center'}}>{ghost.badge}</div>
              )}
            </div>
          </div>
        )}
      </div>
      <div style={{position: 'absolute', left: 0, bottom: 0, width: w, height: 45, borderTop: `1px solid ${th.border}`, boxSizing: 'border-box'}}>
        <div style={{position: 'absolute', left: 8, top: 8, height: 28, display: 'flex', alignItems: 'center', gap: 8, paddingLeft: 8}}>
          <Icon name="icFolderKanb" size={14} color={th.muted} />
          <span style={{font: `500 ${12.5 * (th.ui ?? 1)}px ${UI}`, color: th.muted}}>Open folder as group</span>
        </div>
        {(['icDetach', 'icMessage', 'icSettings'] as const).map((ic, i) => (
          <div key={ic} style={{position: 'absolute', top: 8, left: w - 8 - 3 * 32 + 4 + i * 32, width: 28, height: 28, borderRadius: 6, display: 'grid', placeItems: 'center', background: ic === 'icSettings' && settingsOn ? th.surface2 : undefined}}>
            <Icon name={ic} size={14} color={ic === 'icMessage' ? mix(th.sidebar, th.muted, 0.5) : ic === 'icSettings' && settingsOn ? th.fg : th.muted} />
          </div>
        ))}
      </div>
      <div style={{position: 'absolute', left: w, top: 0, width: 1, height, background: th.border}} />
    </div>
  );
};
