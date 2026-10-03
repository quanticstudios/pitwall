// see: internal/ui/settings/page.go and appearance.go — the layout ported here

import React from 'react';
import {Icon} from '../icons';
import {mix, themes, UI, MONO, type Theme} from '../theme';

const NAV_W = 224;

const Thumbnail: React.FC<{t: Theme; w: number; h: number}> = ({t, w, h}) => {
  const u = h / 100;
  const px = (v: number) => v * u;
  const side = (w * 0.3) / u;
  const bar = (x: number, y: number, bw: number, c: string, k: string) => (
    <div key={k} style={{position: 'absolute', left: px(x), top: px(y), width: px(bw), height: px(5), borderRadius: px(2.5), background: c}} />
  );
  const pane = {x: px(side), y: px(6), w: w - px(side) - px(6), h: h - px(12)};
  const pw = pane.w / u;
  const x0 = side + 8;
  return (
    <div style={{position: 'relative', width: w, height: h, borderRadius: 6, background: t.sidebar, overflow: 'hidden', flex: 'none'}}>
      <div style={{position: 'absolute', left: px(5), top: px(24), width: px(side - 10), height: px(16), borderRadius: px(4), background: t.surface2}} />
      {[t.blue, t.yellow, t.green].map((c, i) => (
        <React.Fragment key={i}>
          <div style={{position: 'absolute', left: px(10), top: px(12 + i * 18), width: px(6), height: px(6), borderRadius: px(3), background: c}} />
          {bar(20, 12 + i * 18 + 0.5, (side - 30) * [0.8, 0.6, 0.7][i], mix(t.sidebar, t.fg, [0.5, 0.9, 0.5][i]), 'b' + i)}
        </React.Fragment>
      ))}
      <div style={{position: 'absolute', left: pane.x, top: pane.y, width: pane.w, height: pane.h, borderRadius: px(5), background: t.termBg}} />
      {bar(x0, 16, pw * 0.15, t.ansi[2], 'p1')}
      {bar(x0 + pw * 0.18, 16, pw * 0.4, t.termFg, 'p2')}
      {bar(x0, 30, pw * 0.6, mix(t.termBg, t.termFg, 0.6), 'p3')}
      {bar(x0, 44, pw * 0.25, t.ansi[4], 'p4')}
      {bar(x0 + pw * 0.28, 44, pw * 0.2, t.ansi[3], 'p5')}
      {bar(x0, 58, pw * 0.15, t.ansi[2], 'p6')}
      <div style={{position: 'absolute', left: px(x0 + pw * 0.18), top: px(57), width: px(4), height: px(7), background: t.cursor}} />
      <div style={{position: 'absolute', left: pane.x + pane.w - px(36), top: pane.y + pane.h - px(20), width: px(30), height: px(14), borderRadius: px(3), background: t.surface}}>
        <div style={{position: 'absolute', right: px(5), top: (px(14) - px(8)) / 2, width: px(8), height: px(8), borderRadius: px(4), background: t.primary}} />
      </div>
    </div>
  );
};

const Card: React.FC<{th: Theme; children: React.ReactNode; style?: React.CSSProperties}> = ({th, children, style}) => (
  <div style={{borderRadius: 8, background: th.surface2, boxShadow: `inset 0 0 0 1px ${th.border}`, ...style}}>{children}</div>
);

const Keycap: React.FC<{th: Theme; k: string}> = ({th, k}) => (
  <div style={{height: 20, minWidth: 20, padding: '0 6px', boxSizing: 'border-box', borderRadius: 4, border: `1px solid ${th.border}`, background: `linear-gradient(${th.elevated}, ${th.surface})`, display: 'grid', placeItems: 'center', font: `400 ${12 * (th.ui ?? 1)}px ${UI}`, color: mix(th.muted, th.fg, 0.54)}}>{k}</div>
);

const Chip: React.FC<{th: Theme; k: string; on?: boolean; flash?: number}> = ({th, k, on, flash = 0}) => (
  <div style={{height: 24, padding: '0 8px', borderRadius: 6, display: 'grid', placeItems: 'center', font: `400 ${12 * (th.ui ?? 1)}px ${UI}`, color: on ? th.primary : th.fg, background: on ? mix(th.elevated, th.primary, 0.12) : mix(th.elevated, th.yellow, 0.3 * flash), boxShadow: `inset 0 0 0 1px ${on ? mix(th.elevated, th.primary, 0.6) : th.border}`, whiteSpace: 'nowrap'}}>{k}</div>
);

const Stepper: React.FC<{th: Theme; v: number; press?: number}> = ({th, v, press = 0}) => (
  <div style={{display: 'flex', alignItems: 'center', height: 28, borderRadius: 6, background: th.elevated, boxShadow: `inset 0 0 0 1px ${th.border}`}}>
    <div style={{width: 28, height: 28, display: 'grid', placeItems: 'center'}}><Icon name="minus" size={14} color={th.muted} /></div>
    <div style={{width: 52, textAlign: 'center', font: `400 ${13 * (th.ui ?? 1)}px ${UI}`, color: th.fg}}>{v}</div>
    <div style={{width: 28, height: 28, borderRadius: 6, display: 'grid', placeItems: 'center', background: press ? mix(th.elevated, th.fg, 0.1 * press) : undefined}}><Icon name="icPlus" size={14} color={press ? th.fg : th.muted} /></div>
  </div>
);

const Dropdown: React.FC<{th: Theme; v: string}> = ({th, v}) => (
  <div style={{width: 238, height: 30, borderRadius: 6, background: th.elevated, boxShadow: `inset 0 0 0 1px ${th.border}`, display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '0 10px', boxSizing: 'border-box', font: `400 ${13 * (th.ui ?? 1)}px ${UI}`, color: th.fg}}>
    {v}
    <svg width="12" height="12" viewBox="0 0 24 24"><path d="M6 9l6 6 6-6" fill="none" stroke={th.muted} strokeWidth="2" strokeLinecap="round" /></svg>
  </div>
);

const SetRow: React.FC<{th: Theme; label: string; desc?: string; right: React.ReactNode; last?: boolean; below?: React.ReactNode}> = ({th, label, desc, right, last, below}) => (
  <div style={{padding: '12px 16px', borderBottom: last ? undefined : `1px solid ${th.border}`}}>
    <div style={{display: 'flex', alignItems: 'center', gap: 16, minHeight: 28}}>
      <div style={{flex: 1}}>
        <div style={{font: `500 ${13 * (th.ui ?? 1)}px ${UI}`, color: th.fg}}>{label}</div>
        {desc && <div style={{font: `400 ${12 * (th.ui ?? 1)}px ${UI}`, color: th.muted, marginTop: 3}}>{desc}</div>}
      </div>
      {right}
    </div>
    {below}
  </div>
);

export type Shortcut = {label: string; name: string; chords: string[]};

export type SettingsProps = {
  th: Theme;
  w: number;
  h: number;
  page: 'appearance' | 'shortcuts';
  themeName: string;
  hoverTheme?: string;
  uiSize: number;
  stepPress?: number;
  scroll?: number;
  shortcuts?: Shortcut[];
  recording?: string;
  conflict?: {chord: string; other: string; gives: string; swapHover?: boolean};
  flash?: Record<string, number>;
  hoverNav?: string;
};

/** Settings is the settings page: categories and search on the left, the open category's rows on the right. */
export const Settings: React.FC<SettingsProps> = ({th, w, h, page, themeName, hoverTheme, uiSize, stepPress, scroll = 0, shortcuts = [], recording, conflict, flash = {}, hoverNav}) => {
  const inner = Math.min(800, w - NAV_W - 1 - 64);
  const left = NAV_W + 1 + (w - NAV_W - 1 - inner) / 2;
  const cats = ['Appearance', 'Keyboard shortcuts', 'Terminal', 'Agents', 'About'];
  const sel = page === 'appearance' ? 0 : 1;
  const cw = (inner - 32 - 24) / 3;
  const tw = cw - 16;
  return (
    <div style={{position: 'absolute', inset: 0, background: th.surface, overflow: 'hidden'}}>
      <div style={{position: 'absolute', left: 0, top: 0, width: NAV_W, height: h, background: th.bg, borderRight: `1px solid ${th.border}`}}>
        <div style={{position: 'absolute', left: 24, top: 20, font: `600 ${14 * (th.ui ?? 1)}px ${UI}`, color: th.fg}}>Settings</div>
        <div style={{position: 'absolute', left: 16, top: 56, width: NAV_W - 32, height: 32, borderRadius: 6, background: th.elevated, boxShadow: `inset 0 0 0 1px ${th.border}`, display: 'flex', alignItems: 'center', gap: 7, paddingLeft: 9, boxSizing: 'border-box'}}>
          <Icon name="search" size={14} color={th.muted} />
          <span style={{font: `400 ${13 * (th.ui ?? 1)}px ${UI}`, color: th.muted}}>Search settings</span>
        </div>
        {cats.map((c, i) => (
          <div key={c} style={{position: 'absolute', left: 16, top: 104 + i * 32, width: NAV_W - 32, height: 30, borderRadius: 6, background: i === sel ? th.elevated : hoverNav === c ? mix(th.bg, th.elevated, 0.6) : undefined, display: 'flex', alignItems: 'center', paddingLeft: 10, boxSizing: 'border-box', font: `${i === sel ? 500 : 400} ${13 * (th.ui ?? 1)}px ${UI}`, color: i === sel ? th.fg : mix(th.bg, th.fg, 0.82)}}>{c}</div>
        ))}
      </div>
      <div style={{position: 'absolute', left, top: 40 - scroll, width: inner}}>
        <div style={{display: 'flex', alignItems: 'flex-start'}}>
          <div style={{flex: 1}}>
            <div style={{font: `500 ${20 * (th.ui ?? 1)}px ${UI}`, color: th.fg}}>{page === 'appearance' ? 'Appearance' : 'Keyboard shortcuts'}</div>
            <div style={{font: `400 ${13 * (th.ui ?? 1)}px ${UI}`, color: th.muted, marginTop: 6}}>{page === 'appearance' ? 'Theme, fonts and spacing. Changes apply as you make them.' : 'Click a shortcut to record a new one.'}</div>
          </div>
          <div style={{display: 'flex', gap: 8, alignItems: 'center'}}>
            <Keycap th={th} k="Esc" />
            <div style={{height: 28, padding: '0 10px', borderRadius: 6, background: th.elevated, boxShadow: `inset 0 0 0 1px ${th.border}`, display: 'grid', placeItems: 'center', font: `500 ${13 * (th.ui ?? 1)}px ${UI}`, color: th.fg}}>Close</div>
          </div>
        </div>
        {page === 'appearance' ? (
          <>
            <Card th={th} style={{marginTop: 20, padding: 16}}>
              <div style={{font: `500 ${13 * (th.ui ?? 1)}px ${UI}`, color: th.fg}}>Theme</div>
              <div style={{font: `400 ${12 * (th.ui ?? 1)}px ${UI}`, color: th.muted, marginTop: 4}}>Window and terminal colors. Custom themes are themes/&lt;name&gt;.toml next to config.toml.</div>
              <div style={{display: 'flex', flexWrap: 'wrap', gap: 12, marginTop: 14}}>
                {Object.values(themes).map((t) => {
                  const on = t.name === themeName;
                  const ring = on ? th.primary : hoverTheme === t.name ? mix(th.border, th.fg, 0.25) : th.border;
                  return (
                    <div key={t.name} style={{width: cw, padding: 8, boxSizing: 'border-box', borderRadius: 8, background: th.surface2, boxShadow: `inset 0 0 0 ${on ? 2 : 1}px ${ring}`}}>
                      <Thumbnail t={t} w={tw} h={(tw * 10) / 16} />
                      <div style={{font: `500 ${13 * (th.ui ?? 1)}px ${UI}`, color: th.fg, marginTop: 8}}>{t.name}</div>
                    </div>
                  );
                })}
              </div>
            </Card>
            <div style={{font: `500 ${13 * (th.ui ?? 1)}px ${UI}`, color: th.fg, margin: '28px 0 12px'}}>Text</div>
            <Card th={th}>
              <SetRow th={th} label="Interface font" desc="Sidebar, dialogs and this page. Geist ships with pitwall." right={<Dropdown th={th} v="Geist" />} />
              <SetRow th={th} label="Interface text size" right={<Stepper th={th} v={uiSize} press={stepPress} />} />
              <SetRow th={th} label="Terminal font" desc="Any installed family. Characters it lacks come from mono_fallback." right={<Dropdown th={th} v="JetBrains Mono" />} />
              <SetRow th={th} label="Terminal text size" right={<Stepper th={th} v={13} />} last />
            </Card>
          </>
        ) : (
          <>
            <Card th={th} style={{marginTop: 20}}>
              <SetRow th={th} label="Preset" desc="conventional follows Linux terminals (Ghostty, kitty, GNOME Terminal); aide is the Alt-key layout." last right={
                <div style={{display: 'flex', padding: 2, borderRadius: 6, background: th.elevated, boxShadow: `inset 0 0 0 1px ${th.border}`}}>
                  {['conventional', 'aide'].map((p) => (
                    <div key={p} style={{height: 24, padding: '0 10px', borderRadius: 4, display: 'grid', placeItems: 'center', font: `400 ${13 * (th.ui ?? 1)}px ${UI}`, color: p === 'conventional' ? th.fg : th.muted, background: p === 'conventional' ? mix(th.elevated, th.fg, 0.1) : undefined}}>{p}</div>
                  ))}
                </div>
              } />
            </Card>
            <div style={{font: `500 ${13 * (th.ui ?? 1)}px ${UI}`, color: th.fg, margin: '28px 0 12px'}}>Shortcuts</div>
            <Card th={th}>
              {shortcuts.map((s, i) => (
                <SetRow key={s.name} th={th} label={s.label} last={i === shortcuts.length - 1}
                  desc={s.name}
                  right={
                    <div style={{display: 'flex', alignItems: 'center', gap: 6}}>
                      {recording === s.name ? <Chip th={th} k="Press keys…" on /> : s.chords.length ? s.chords.map((c) => <Chip key={c} th={th} k={c} flash={flash[s.name]} />) : <span style={{font: `400 ${12 * (th.ui ?? 1)}px ${UI}`, color: th.muted}}>Unbound</span>}
                      <div style={{width: 24, height: 24, display: 'grid', placeItems: 'center'}}><Icon name="icPlus" size={14} color={th.muted} /></div>
                    </div>
                  }
                  below={recording === s.name && (conflict ? (
                    <div style={{marginTop: 10, padding: '6px 10px', borderRadius: 6, background: mix(th.surface2, th.yellow, 0.08), boxShadow: `inset 0 0 0 1px ${mix(th.surface2, th.yellow, 0.35)}`, display: 'flex', alignItems: 'center', gap: 12}}>
                      <div style={{flex: 1, font: `400 ${12 * (th.ui ?? 1)}px ${UI}`, color: th.fg}}>{conflict.chord} already runs “{conflict.other}”. Swap gives it {conflict.gives}.</div>
                      <div style={{height: 26, padding: '0 10px', borderRadius: 6, display: 'grid', placeItems: 'center', font: `500 ${12 * (th.ui ?? 1)}px ${UI}`, color: th.fg, boxShadow: `inset 0 0 0 1px ${th.border}`}}>Cancel</div>
                      <div style={{height: 26, padding: '0 12px', borderRadius: 6, display: 'grid', placeItems: 'center', font: `500 ${12 * (th.ui ?? 1)}px ${UI}`, color: th.onPrimary, background: conflict.swapHover ? mix(th.primary, th.fg, 0.12) : th.primary}}>Swap</div>
                    </div>
                  ) : (
                    <div style={{marginTop: 8, font: `400 ${12 * (th.ui ?? 1)}px ${UI}`, color: th.primary}}>Press the new shortcut. Esc cancels, Backspace removes this one.</div>
                  ))}
                />
              ))}
            </Card>
          </>
        )}
      </div>
    </div>
  );
};

/** ConfigFile draws config.toml as a small editor card, lines flashing as they change. */
export const ConfigFile: React.FC<{lines: {t: string; flash?: number}[]; th: Theme; w: number}> = ({lines, th, w}) => {
  const tok = (t: string) => {
    if (t.startsWith('#')) return <span style={{color: th.muted}}>{t}</span>;
    if (t.startsWith('[')) return <span style={{color: th.purple}}>{t}</span>;
    const m = t.match(/^(\w+)( = )(.*?)(\s+#.*)?$/);
    if (!m) return t;
    return (
      <>
        <span style={{color: th.blue}}>{m[1]}</span>
        <span style={{color: th.muted}}>{m[2]}</span>
        <span style={{color: m[3].startsWith('"') ? th.green : th.yellow}}>{m[3]}</span>
        {m[4] && <span style={{color: th.muted}}>{m[4]}</span>}
      </>
    );
  };
  return (
    <div style={{width: w, borderRadius: 12, background: mix(th.bg, th.fg, 0.03), boxShadow: `0 0 0 1px ${mix(th.bg, th.fg, 0.1)}, 0 24px 60px rgba(0,0,0,.5)`, overflow: 'hidden'}}>
      <div style={{height: 38, display: 'flex', alignItems: 'center', padding: '0 16px', borderBottom: `1px solid ${mix(th.bg, th.fg, 0.08)}`, font: `500 ${14 * (th.ui ?? 1)}px ${UI}`, color: th.muted}}>~/.config/pitwall/config.toml</div>
      <div style={{padding: '12px 0', font: `400 15px/24px ${MONO}`, whiteSpace: 'pre', color: th.fg}}>
        {lines.map((l, i) => (
          <div key={i} style={{padding: '0 16px', background: l.flash ? mix(mix(th.bg, th.fg, 0.03), th.primary, 0.22 * l.flash) : undefined}}>{tok(l.t) || ' '}</div>
        ))}
      </div>
    </div>
  );
};
