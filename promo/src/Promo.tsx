import React from 'react';
import {Audio, Easing, interpolate, Series, staticFile} from 'remotion';
import {Intro} from './scenes/Intro';
import {Shell} from './scenes/Shell';
import {Agents, Attention} from './scenes/Agents';
import {Osc} from './scenes/Osc';
import {Drag} from './scenes/Drag';
import {PaneMode} from './scenes/PaneMode';
import {SettingsScene} from './scenes/SettingsScene';
import {Outro, Survive} from './scenes/Survive';

// Scene lengths put every cut on a bar of public/music/track.mp3 (123 BPM,
// beats from librosa): the first drop (61.72 s) lands on Agents at 10 s, the
// second (92.60 s, 16 bars on) on Settings, and the breakdown (122.7 s) on
// the outro. A different track needs these recomputed.
const scenes: [React.FC<{dur: number}>, number][] = [
  [Intro, 232],
  [Shell, 368],
  [Agents, 581],
  [Attention, 581],
  [Osc, 233],
  [Drag, 458],
  [SettingsScene, 702],
  [PaneMode, 348],
  [Survive, 466],
  [Outro, 501],
];

const FPS = 60;
// MUSIC_START is where the video's frame 0 sits in the track, in seconds.
const MUSIC_START = 51.719;
// GAIN is the music level, -3 dB; it swells in over FADE_IN frames.
const GAIN = 10 ** (-3 / 20);
const FADE_IN = 4 * FPS;

export const PROMO_FRAMES = scenes.reduce((n, [, d]) => n + d, 0);

/** Promo plays the scenes back to back over the music track. */
export const Promo: React.FC = () => (
  <>
    <Audio
      src={staticFile('music/track.mp3')}
      trimBefore={Math.round(MUSIC_START * FPS)}
      volume={(f) =>
        f < FADE_IN
          ? interpolate(f, [0, FADE_IN], [0, GAIN], {easing: Easing.in(Easing.quad)})
          : interpolate(f, [PROMO_FRAMES - 120, PROMO_FRAMES], [GAIN, 0], {extrapolateLeft: 'clamp', extrapolateRight: 'clamp'})
      }
    />
    <Series>
      {scenes.map(([Scene, d], i) => (
        <Series.Sequence key={i} durationInFrames={d} premountFor={60}>
          <Scene dur={d} />
        </Series.Sequence>
      ))}
    </Series>
  </>
);
