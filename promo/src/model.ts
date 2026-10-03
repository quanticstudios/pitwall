import type {Agent} from './icons';
import type {State} from './theme';

export type Tab = {
  id: string;
  title: string;
  agent?: Agent;
  state?: State;
  terminal?: boolean;
  pill?: string;
  branch?: string;
  add?: number;
  del?: number;
  path?: string;
  time: string;
  unseen?: State;
};

export type Group = {id: string; name: string; icon: string; color: string};

export type Item = {kind: 'tab'; tab: Tab} | {kind: 'group'; group: Group; tabs: Tab[]; collapsed?: boolean};

/** PillText is sidebar.PillText: the command a busy terminal runs, else the state label. */
export const pillText = (t: Tab): string => {
  if (t.pill) return t.pill;
  const short: Record<State, string> = {
    input: 'Input', done: 'Done', connecting: 'Connecting', error: 'Error',
    approval: 'Approval', plan: 'Plan Ready', running: 'Running', working: 'Working',
  };
  const s = short[t.state!];
  return t.terminal ? s : 'Agent ' + s;
};

/** With returns tabs with the tab whose id is id changed by patch. */
export const withTab = (items: Item[], id: string, patch: Partial<Tab>): Item[] =>
  items.map((it) =>
    it.kind === 'tab'
      ? it.tab.id === id ? {...it, tab: {...it.tab, ...patch}} : it
      : {...it, tabs: it.tabs.map((t) => (t.id === id ? {...t, ...patch} : t))},
  );

export const findTab = (items: Item[], id: string): Tab | undefined => {
  for (const it of items) {
    if (it.kind === 'tab' && it.tab.id === id) return it.tab;
    if (it.kind === 'group') for (const t of it.tabs) if (t.id === id) return t;
  }
  return undefined;
};
