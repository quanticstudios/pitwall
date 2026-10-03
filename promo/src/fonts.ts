import {loadFont} from '@remotion/fonts';
import {staticFile} from 'remotion';

loadFont({family: 'Geist', url: staticFile('fonts/Geist.ttf'), weight: '100 900'});
loadFont({family: 'JetBrains Mono', url: staticFile('fonts/JetBrainsMono-Regular.woff2'), weight: '400'});
loadFont({family: 'JetBrains Mono', url: staticFile('fonts/JetBrainsMono-Bold.woff2'), weight: '700'});
