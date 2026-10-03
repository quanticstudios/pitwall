import React from 'react';
import {Series} from 'remotion';
import {Intro} from './scenes/Intro';
import {Shell} from './scenes/Shell';
import {Agents, Attention} from './scenes/Agents';
import {Osc} from './scenes/Osc';
import {Drag} from './scenes/Drag';
import {PaneMode} from './scenes/PaneMode';
import {SettingsScene} from './scenes/SettingsScene';
import {Outro, Survive} from './scenes/Survive';

const scenes: [React.FC<{dur: number}>, number][] = [
  [Intro, 210],
  [Shell, 390],
  [Agents, 600],
  [Attention, 600],
  [Osc, 270],
  [Drag, 480],
  [PaneMode, 360],
  [SettingsScene, 720],
  [Survive, 390],
  [Outro, 300],
];

export const PROMO_FRAMES = scenes.reduce((n, [, d]) => n + d, 0);

/** Promo plays the scenes back to back. */
export const Promo: React.FC = () => (
  <Series>
    {scenes.map(([Scene, d], i) => (
      <Series.Sequence key={i} durationInFrames={d} premountFor={60}>
        <Scene dur={d} />
      </Series.Sequence>
    ))}
  </Series>
);
