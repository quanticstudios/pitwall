import {Easing, interpolate} from 'remotion';

export const FPS = 60;
export const s = (sec: number) => Math.round(sec * FPS);

export const outCubic = Easing.bezier(0.33, 1, 0.68, 1);
export const inOut = Easing.bezier(0.65, 0, 0.35, 1);
export const smooth = Easing.bezier(0.16, 1, 0.3, 1);

/** Ease is 0 before start, 1 after start+dur, eased in between. */
export const ease = (frame: number, start: number, dur: number, fn = outCubic) =>
  interpolate(frame, [start, start + Math.max(1, dur)], [0, 1], {extrapolateLeft: 'clamp', extrapolateRight: 'clamp', easing: fn});

/** Track interpolates a value through [frame, value] keys, holding the ends. */
export const track = (frame: number, keys: [number, number][], fn = inOut) =>
  interpolate(frame, keys.map((k) => k[0]), keys.map((k) => k[1]), {extrapolateLeft: 'clamp', extrapolateRight: 'clamp', easing: fn});

/** Typed is the prefix of text a typist at cps characters a second has written since start. */
export const typed = (text: string, frame: number, start: number, cps = 28) =>
  text.slice(0, Math.max(0, Math.floor(((frame - start) / FPS) * cps)));

export const lerp = (a: number, b: number, t: number) => a + (b - a) * t;

/** Pulse is Tailwind's animate-pulse opacity in sidebar.pulse: 1 to .5 and back every 2s. */
export const pulse = (frame: number) => 0.75 + 0.25 * Math.cos((frame / FPS) * Math.PI);

/** Pick returns the value of the last [frame, value] key at or before frame. */
export function pick<T>(frame: number, keys: [number, T][]): T {
  let v = keys[0][1];
  for (const [f, x] of keys) if (frame >= f) v = x;
  return v;
}
