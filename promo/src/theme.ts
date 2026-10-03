// see: internal/config/themes.go — the built-in palettes copied here

export type Theme = {
  name: string;
  bg: string;
  sidebar: string;
  surface: string;
  surface2: string;
  elevated: string;
  border: string;
  fg: string;
  muted: string;
  primary: string;
  onPrimary: string;
  red: string;
  yellow: string;
  green: string;
  blue: string;
  purple: string;
  termFg: string;
  termBg: string;
  cursor: string;
  ansi: string[];
  ui?: number;
};

export const themes: Record<string, Theme> = {
  'aide-dark': {
    name: 'aide-dark',
    bg: '#08090c', sidebar: '#08090c', surface: '#14161b', surface2: '#1c1f26',
    elevated: '#252932', border: '#191a1d', fg: '#f2f3f5', muted: '#8e939c',
    primary: '#2997ff', onPrimary: '#06121f', red: '#ff6b6b', yellow: '#ffc533',
    green: '#59d499', blue: '#57c1ff', purple: '#bd93ff',
    termFg: '#f4f4f6', termBg: '#08090c', cursor: '#ffffff',
    ansi: ['#0d0d0d', '#ff6161', '#59d499', '#ffc533', '#57c1ff', '#bb9af7', '#7dcfff', '#cdcdcd',
      '#242728', '#ff6161', '#59d499', '#ffc533', '#57c1ff', '#bb9af7', '#7dcfff', '#ffffff'],
  },
  'aide-light': {
    name: 'aide-light',
    bg: '#f5f5f7', sidebar: '#f5f5f7', surface: '#ebebef', surface2: '#e1e1e6',
    elevated: '#d6d6dc', border: '#dcdce0', fg: '#1d1d1f', muted: '#6e6e73',
    primary: '#0066cc', onPrimary: '#ffffff', red: '#cf222e', yellow: '#9a6700',
    green: '#1a7f37', blue: '#0969da', purple: '#8250df',
    termFg: '#1f2328', termBg: '#ffffff', cursor: '#1d1d1f',
    ansi: ['#24292f', '#cf222e', '#116329', '#4d2d00', '#0969da', '#8250df', '#1b7c83', '#6e7781',
      '#57606a', '#a40e26', '#1a7f37', '#633c01', '#218bff', '#a475f9', '#3192aa', '#8c959f'],
  },
  'tokyo-night': {
    name: 'tokyo-night',
    bg: '#16161e', sidebar: '#16161e', surface: '#1a1b26', surface2: '#232433',
    elevated: '#292e42', border: '#24253a', fg: '#c0caf5', muted: '#737aa2',
    primary: '#7aa2f7', onPrimary: '#16161e', red: '#f7768e', yellow: '#e0af68',
    green: '#9ece6a', blue: '#7dcfff', purple: '#bb9af7',
    termFg: '#c0caf5', termBg: '#1a1b26', cursor: '#c0caf5',
    ansi: ['#15161e', '#f7768e', '#9ece6a', '#e0af68', '#7aa2f7', '#bb9af7', '#7dcfff', '#a9b1d6',
      '#414868', '#f7768e', '#9ece6a', '#e0af68', '#7aa2f7', '#bb9af7', '#7dcfff', '#c0caf5'],
  },
  'catppuccin-mocha': {
    name: 'catppuccin-mocha',
    bg: '#181825', sidebar: '#181825', surface: '#1e1e2e', surface2: '#313244',
    elevated: '#45475a', border: '#28283a', fg: '#cdd6f4', muted: '#9399b2',
    primary: '#89b4fa', onPrimary: '#11111b', red: '#f38ba8', yellow: '#f9e2af',
    green: '#a6e3a1', blue: '#74c7ec', purple: '#cba6f7',
    termFg: '#cdd6f4', termBg: '#1e1e2e', cursor: '#f5e0dc',
    ansi: ['#45475a', '#f38ba8', '#a6e3a1', '#f9e2af', '#89b4fa', '#f5c2e7', '#94e2d5', '#bac2de',
      '#585b70', '#f38ba8', '#a6e3a1', '#f9e2af', '#89b4fa', '#f5c2e7', '#94e2d5', '#a6adc8'],
  },
};

export const dark = themes['aide-dark'];

const parse = (h: string) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));

/** Mix is theme.Mix in the app: fg at opacity a over bg, flattened to one opaque color. */
export const mix = (bg: string, fg: string, a: number): string => {
  const b = parse(bg);
  const f = parse(fg);
  return '#' + b.map((v, i) => Math.round(v + (f[i] - v) * a).toString(16).padStart(2, '0')).join('');
};

/** Blend moves every color of theme a towards theme b by t. */
export const blend = (a: Theme, b: Theme, t: number): Theme => {
  if (t <= 0) return a;
  if (t >= 1) return b;
  const out = {...b, ansi: a.ansi.map((c, i) => mix(c, b.ansi[i], t))} as Theme;
  for (const k of Object.keys(a) as (keyof Theme)[]) {
    const v = a[k];
    if (typeof v === 'string' && v.startsWith('#')) (out as Record<string, unknown>)[k] = mix(v, b[k] as string, t);
  }
  return out;
};

export type State = 'working' | 'connecting' | 'input' | 'approval' | 'plan' | 'done' | 'error' | 'running';

/** StateColor is sidebar.StateColor. */
export const stateColor = (th: Theme, s: State): string => {
  switch (s) {
    case 'error': return th.red;
    case 'approval': case 'input': return th.yellow;
    case 'working': case 'connecting': return th.blue;
    case 'plan': return th.purple;
  }
  return th.green;
};

export const claudeOrange = '#d97757';
export const UI = 'Geist, sans-serif';
export const MONO = '"JetBrains Mono", monospace';
