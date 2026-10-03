import type {Group, Item, Tab} from './model';

export const groups: Record<string, Group> = {
  api: {id: 'api', name: 'acme-api', icon: 'server', color: 'sky'},
  billing: {id: 'billing', name: 'billing', icon: 'briefcase', color: 'emerald'},
  web: {id: 'web', name: 'web-app', icon: 'globe', color: 'violet'},
};

export const tabs = {
  rate: {id: 'rate', title: 'Add rate limiting to the public API', agent: 'claude', state: 'working', branch: 'feat/rate-limits', add: 214, del: 37, time: 'just now'},
  flaky: {id: 'flaky', title: 'Fix flaky auth test', agent: 'codex', state: 'working', branch: 'fix/flaky-auth', add: 18, del: 9, time: '12s ago'},
  stripe: {id: 'stripe', title: 'Migrate billing to Stripe v3', agent: 'claude', state: 'working', branch: 'feat/stripe-v3', add: 402, del: 188, time: '40s ago'},
  dev: {id: 'dev', title: 'npm run dev', terminal: true, state: 'running', pill: 'node', branch: 'main', time: '6m ago'},
  docs: {id: 'docs', title: 'Write the onboarding guide', agent: 'codex', state: 'working', branch: 'docs/onboarding', add: 96, del: 4, time: '1m ago'},
  ml: {id: 'ml', title: 'Tune the feature pipeline', agent: 'claude', state: 'plan', branch: 'exp/feature-store', add: 31, del: 12, time: '3m ago'},
  home: {id: 'home', title: '~', path: '~', time: '2h ago'},
} satisfies Record<string, Tab>;

export const mainItems: Item[] = [
  {kind: 'tab', tab: tabs.docs},
  {kind: 'tab', tab: tabs.ml},
  {kind: 'tab', tab: tabs.home},
  {kind: 'group', group: groups.api, tabs: [tabs.rate, tabs.flaky]},
  {kind: 'group', group: groups.billing, tabs: [tabs.stripe]},
  {kind: 'group', group: groups.web, tabs: [tabs.dev]},
];
