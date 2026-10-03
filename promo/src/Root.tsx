import React from 'react';
import {Composition} from 'remotion';
import './fonts';
import {Promo, PROMO_FRAMES} from './Promo';
import {Hero, HERO_FRAMES} from './Hero';

export const RemotionRoot: React.FC = () => (
  <>
    <Composition id="Promo" component={Promo} durationInFrames={PROMO_FRAMES} fps={60} width={1920} height={1080} />
    <Composition id="Hero" component={Hero} durationInFrames={HERO_FRAMES} fps={30} width={1600} height={900} />
  </>
);
