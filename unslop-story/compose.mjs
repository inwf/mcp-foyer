import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const root = dirname(fileURLToPath(import.meta.url));
const morph = resolve(process.argv[2] ?? resolve(root, '../scratch/morph-wide.mp4'));
const output = resolve(process.argv[3] ?? resolve(root, '../unslop-story.mp4'));
const shots = ['tools-dark', 'overview-light', 'servers-dark', 'settings-dark'];
const inputs = shots.flatMap(name => ['-loop', '1', '-framerate', '60', '-t', '1.2', '-i', resolve(root, `${name}.png`)]);
const filters = [
  '[0:v]scale=1920:1080,setsar=1,fps=60,format=yuv420p,trim=duration=6,settb=1/60,setpts=PTS-STARTPTS[v0]',
  ...shots.map((_, i) => `[${i + 1}:v]scale=1920:1080,setsar=1,fps=60,format=yuv420p,settb=1/60,setpts=PTS-STARTPTS[v${i + 1}]`),
  '[v0][v1]xfade=transition=fade:duration=0.2:offset=5.8[x1]',
  '[x1][v2]xfade=transition=fade:duration=0.2:offset=6.8[x2]',
  '[x2][v3]xfade=transition=fade:duration=0.2:offset=7.8[x3]',
  '[x3][v4]xfade=transition=fade:duration=0.2:offset=8.8[out]',
];
execFileSync('ffmpeg', [
  '-y', '-loglevel', 'error', '-i', morph, ...inputs,
  '-filter_complex_threads', '2', '-filter_complex', filters.join(';'),
  '-map', '[out]', '-t', '10', '-c:v', 'libx264', '-crf', '18', '-preset', 'medium',
  '-pix_fmt', 'yuv420p', '-movflags', '+faststart', output,
], { stdio: 'inherit' });
console.log(output);
