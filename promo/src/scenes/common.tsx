import React from 'react';
import {Sidebar, type SidebarProps} from '../ui/Sidebar';
import {PaneArea, type PaneAreaProps} from '../ui/Panes';
import {Win, WIN_H, WIN_W} from '../ui/Window';
import {SIDEBAR_W} from '../ui/Sidebar';
import type {Theme} from '../theme';

export const AREA_W = WIN_W - SIDEBAR_W - 1;
export const AREA_H = WIN_H;

type AppProps = {
  th: Theme;
  side: Omit<SidebarProps, 'th' | 'height'>;
  panes?: Omit<PaneAreaProps, 'th' | 'w' | 'h'>;
  content?: React.ReactNode;
  pill?: React.ReactNode;
  overlay?: React.ReactNode;
};

/** App is the whole pitwall window: sidebar plus the pane area or a page in its place. */
export const App: React.FC<AppProps> = ({th, side, panes, content, pill, overlay}) => (
  <Win th={th} pill={pill} overlay={overlay} sidebar={<Sidebar th={th} height={WIN_H} {...side} />}>
    {content ?? (panes && <PaneArea th={th} w={AREA_W} h={AREA_H} {...panes} />)}
  </Win>
);

/** InArea converts a point in the pane area to window coordinates. */
export const inArea = (x: number, y: number) => ({x: SIDEBAR_W + 1 + x, y});
