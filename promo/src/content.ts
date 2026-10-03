import type {Line, Seg} from './ui/Panes';

export const prompt = (path: string, branch?: string): Seg[] => [
  {t: 'dev@studio', c: 'g', b: true},
  {t: ':'},
  {t: path, c: 'b', b: true},
  ...(branch ? [{t: ` (${branch})`, c: 'm'}] : []),
  {t: ' $ '},
];

export const cmd = (path: string, branch: string | undefined, text: string): Line => [...prompt(path, branch), {t: text}];

const box = (w: number, rows: Seg[][]): Line[] => {
  const len = (r: Seg[]) => r.reduce((n, s) => n + [...s.t].length, 0);
  return [
    [{t: '╭' + '─'.repeat(w) + '╮', c: 'or'}],
    ...rows.map((r) => [{t: '│ ', c: 'or'}, ...r, {t: ' '.repeat(Math.max(0, w - 1 - len(r))) + '│', c: 'or'}]),
    [{t: '╰' + '─'.repeat(w) + '╯', c: 'or'}],
  ];
};

export const claudeHeader = (folder: string): Line[] => [
  ...box(44, [[{t: '✶ ', c: 'or'}, {t: 'Welcome to Claude Code!', b: true}], [], [{t: `  cwd: ~/src/${folder}`, c: 'dim'}]]),
  '',
];

const spinner = ['·', '•', '✶', '•'];
export const thinking = (frame: number, word: string): Line => [
  {t: spinner[Math.floor(frame / 9) % spinner.length] + ' ', c: 'or'},
  {t: word + '… ', c: 'or'},
  {t: '(esc to interrupt)', c: 'dim'},
];

export const rateLimit: Line[] = [
  ...claudeHeader('acme-api'),
  [{t: '> ', c: 'dim'}, {t: 'Add rate limiting to the public API'}],
  '',
  [{t: '● ', c: 'w'}, {t: "I'll add a token bucket limiter as middleware"}],
  '  and mount it on the public router.',
  '',
  [{t: '● ', c: 'g'}, {t: 'Read', b: true}, {t: '(src/server/router.ts)'}],
  [{t: '  └ ', c: 'dim'}, {t: 'Read 84 lines', c: 'dim'}],
  '',
  [{t: '● ', c: 'g'}, {t: 'Write', b: true}, {t: '(src/middleware/rateLimit.ts)'}],
  [{t: '  └ ', c: 'dim'}, {t: 'Wrote 42 lines', c: 'dim'}],
  [{t: '     1 ', c: 'dim'}, {t: '+ export function rateLimit(opts: Limits) {', c: 'g'}],
  [{t: '     2 ', c: 'dim'}, {t: '+   const buckets = new Map<string, Bucket>()', c: 'g'}],
  [{t: '     3 ', c: 'dim'}, {t: '+   return (req, res, next) => {', c: 'g'}],
  '',
  [{t: '● ', c: 'g'}, {t: 'Update', b: true}, {t: '(src/server/router.ts)'}],
  [{t: '  └ ', c: 'dim'}, {t: 'Updated with 3 additions', c: 'dim'}],
  [{t: '    12 ', c: 'dim'}, {t: '+ public.use(rateLimit({ rpm: 600 }))', c: 'g'}],
  '',
];

export const codexHeader: Line[] = [
  [{t: '>_ ', c: 'dim'}, {t: 'OpenAI Codex', b: true}],
  [{t: '   directory: ~/src/acme-api', c: 'dim'}],
  '',
];

export const codexTests: Line[] = [
  ...codexHeader,
  [{t: '› ', c: 'c'}, {t: 'Write tests for the rate limiter'}],
  '',
  [{t: '• ', c: 'dim'}, {t: 'Explored', b: true}],
  [{t: '  └ Read rateLimit.ts, router.ts', c: 'dim'}],
  '',
  [{t: '• ', c: 'dim'}, {t: 'Added test/limiter.test.ts (+88)'}],
  [{t: '    + it("refills one token per tick", ...)', c: 'g'}],
  [{t: '    + it("returns 429 when empty", ...)', c: 'g'}],
  '',
  [{t: '• ', c: 'dim'}, {t: 'Ran', b: true}, {t: ' npm test -- limiter'}],
  [{t: '  └ ', c: 'dim'}, {t: '12 passed', c: 'g'}],
  '',
  [{t: '─ Worked for 1m 48s ', c: 'dim'}, {t: '─'.repeat(30), c: 'dim'}],
  '',
  [{t: '• ', c: 'dim'}, {t: 'Tests cover refill, burst and the 429 path.'}],
  '',
];

export const codexFlaky: Line[] = [
  [{t: '>_ ', c: 'dim'}, {t: 'OpenAI Codex', b: true}],
  [{t: '   directory: ~/src/acme-api', c: 'dim'}],
  '',
  [{t: '› ', c: 'c'}, {t: 'Fix flaky auth test'}],
  '',
  [{t: '• ', c: 'dim'}, {t: 'The test races the token refresh timer.'}],
  [{t: '• ', c: 'dim'}, {t: 'Edited test/auth.test.ts (+12 -4)'}],
  [{t: '• ', c: 'dim'}, {t: 'Ran', b: true}, {t: ' npm test -- auth --repeat 50'}],
  [{t: '  └ ', c: 'dim'}, {t: '50/50 passed', c: 'g'}],
  '',
  [{t: '─ Worked for 2m 05s ', c: 'dim'}, {t: '─'.repeat(30), c: 'dim'}],
  '',
];

export const stripeBefore: Line[] = [
  ...claudeHeader('billing'),
  [{t: '> ', c: 'dim'}, {t: 'Migrate billing to Stripe v3'}],
  '',
  [{t: '● ', c: 'g'}, {t: 'Read', b: true}, {t: '(src/billing/client.ts)'}],
  [{t: '  └ ', c: 'dim'}, {t: 'Read 132 lines', c: 'dim'}],
  '',
  [{t: '● ', c: 'w'}, {t: 'The v3 client takes the API version up front.'}],
  '',
];

export const stripeAsk: Line[] = [
  ...stripeBefore,
  [{t: '● ', c: 'y'}, {t: 'Update', b: true}, {t: '(src/billing/client.ts)'}],
  ...box(52, [
    [{t: 'Edit file', b: true}],
    [{t: 'src/billing/client.ts', c: 'dim'}],
    [{t: '  41 - ', c: 'r'}, {t: 'const stripe = new Stripe(key)', c: 'r'}],
    [{t: '  41 + ', c: 'g'}, {t: 'const stripe = new Stripe(key, {', c: 'g'}],
    [{t: '  42 + ', c: 'g'}, {t: '  apiVersion: STRIPE_API,', c: 'g'}],
    [{t: '  43 + ', c: 'g'}, {t: '})', c: 'g'}],
    [],
    [{t: 'Do you want to make this edit?'}],
    [{t: '❯ 1. Yes', c: 'b'}],
    [{t: '  2. Yes, allow all edits this session'}],
    [{t: '  3. No, and tell Claude what to do'}],
  ]),
];

export const stripeAfter: Line[] = [
  ...stripeBefore,
  [{t: '● ', c: 'g'}, {t: 'Update', b: true}, {t: '(src/billing/client.ts)'}],
  [{t: '  └ ', c: 'dim'}, {t: 'Updated with 3 additions and 1 removal', c: 'dim'}],
  '',
];

export const testRun = (path: string, branch: string): Line[] => [
  cmd(path, branch, "npm test && printf '\\e]9;tests passed\\a'"),
  '',
  [{t: '> web-app@0.9.0 test', c: 'dim'}],
  [{t: '> vitest run', c: 'dim'}],
  '',
  [{t: ' ✓ ', c: 'g'}, {t: 'src/cart.test.ts '}, {t: '(18 tests) 41ms', c: 'dim'}],
  [{t: ' ✓ ', c: 'g'}, {t: 'src/checkout.test.ts '}, {t: '(9 tests) 22ms', c: 'dim'}],
  [{t: ' ✓ ', c: 'g'}, {t: 'src/session.test.ts '}, {t: '(24 tests) 63ms', c: 'dim'}],
  '',
  [{t: ' Test Files  ', c: 'dim'}, {t: '3 passed', c: 'g', b: true}, {t: ' (3)', c: 'dim'}],
  [{t: '      Tests  ', c: 'dim'}, {t: '51 passed', c: 'g', b: true}, {t: ' (51)', c: 'dim'}],
  '',
];

export const viteDev = (path: string, branch: string): Line[] => [
  cmd(path, branch, 'npm run dev'),
  '',
  [{t: '> web-app@0.9.0 dev', c: 'dim'}],
  [{t: '> vite', c: 'dim'}],
  '',
  [{t: '  VITE v7.1.2', c: 'g', b: true}, {t: '  ready in 412 ms', c: 'dim'}],
  '',
  [{t: '  → ', c: 'g'}, {t: 'Local:   ', b: true}, {t: 'http://localhost:5173/', c: 'c'}],
  [{t: '  → ', c: 'g'}, {t: 'Network: ', b: true}, {t: 'use --host to expose', c: 'dim'}],
  '',
  [{t: '  12:04:31 ', c: 'dim'}, {t: '[vite] ', c: 'c'}, {t: 'hmr update ', c: 'g'}, {t: '/src/Cart.tsx', c: 'dim'}],
];

const lsRows = [
  ['#', 'NAME', 'STATE', 'FOLDER', 'GROUP'],
  ['1', 'Write the onboarding guide', 'Agent Working', '~/src/docs-site', '-'],
  ['2', 'Tune the feature pipeline', 'Agent Plan Ready', '~/src/ml-pipeline', '-'],
  ['3', '~', 'idle', '~', '-'],
  ['4', 'Add rate limiting to the public API', 'Agent Working', '~/src/acme-api', 'acme-api'],
  ['5', 'Fix flaky auth test', 'Agent Done', '~/src/acme-api', 'acme-api'],
  ['6', 'Migrate billing to Stripe v3', 'Agent Working', '~/src/billing', 'billing'],
  ['7', 'npm run dev', 'Running', '~/src/web-app', 'web-app'],
];

const widths = lsRows[0].map((_, c) => Math.max(...lsRows.map((r) => r[c].length)) + 2);

/** LsOutput is `pitwall ls` as tabwriter lays it out: columns two spaces past the widest cell. */
export const lsOutput: Line[] = lsRows.map((r, i) => {
  const t = r.map((cell, c) => (c === r.length - 1 ? cell : cell.padEnd(widths[c]))).join('');
  return i === 0 ? [{t, b: true}] : [{t: t.slice(0, widths[0] + widths[1])}, {t: t.slice(widths[0] + widths[1], widths[0] + widths[1] + widths[2]), c: r[2].includes('Working') ? 'b' : r[2].includes('Done') || r[2] === 'Running' ? 'g' : r[2].includes('Plan') ? 'm' : 'dim'}, {t: t.slice(widths[0] + widths[1] + widths[2])}];
});

/** Reveal is the first n lines of ls, n growing from start at rate lines a second. */
export const reveal = (ls: Line[], frame: number, start: number, perSec: number) =>
  ls.slice(0, Math.max(0, Math.min(ls.length, Math.floor(((frame - start) / 60) * perSec))));
