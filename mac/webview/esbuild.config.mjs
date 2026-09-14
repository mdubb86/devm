import esbuild from 'esbuild';
import sveltePlugin from 'esbuild-svelte';
import { sveltePreprocess } from 'svelte-preprocess';
import postcss from 'postcss';
import tailwindcss from 'tailwindcss';
import autoprefixer from 'autoprefixer';
import tailwindConfig from './tailwind.config.js';
import fs from 'fs';

const outdir = '../devm/Resources';
const watch = process.argv.includes('--watch');
const cssSrc = 'src/app.css';
const cssDst = `${outdir}/gui.css`;

const buildOpts = {
  entryPoints: ['src/main.ts'],
  bundle: true,
  minify: !watch,
  sourcemap: watch,
  outfile: `${outdir}/gui.js`,
  format: 'iife',
  target: ['safari16'],
  loader: { '.svg': 'dataurl' },
  plugins: [
    sveltePlugin({
      preprocess: sveltePreprocess({ postcss: true }),
      compilerOptions: { css: 'external' },
    }),
  ],
};

// esbuild has no built-in Tailwind/PostCSS step, so app.css is run through
// PostCSS directly (same tailwindcss + autoprefixer plugins as
// postcss.config.mjs) before minifying with esbuild's CSS transform.
async function buildCss() {
  const src = fs.readFileSync(cssSrc, 'utf8');
  const result = await postcss([tailwindcss(tailwindConfig), autoprefixer]).process(src, {
    from: cssSrc,
    to: cssDst,
  });
  const css = watch ? result.css : (await esbuild.transform(result.css, { loader: 'css', minify: true })).code;
  fs.writeFileSync(cssDst, css);
}

const htmlSrc = 'index.html';
const htmlDst = `${outdir}/gui.html`;
fs.mkdirSync(outdir, { recursive: true });
fs.copyFileSync(htmlSrc, htmlDst);

if (watch) {
  const ctx = await esbuild.context(buildOpts);
  await ctx.watch();
  await buildCss();
  // Tailwind's JIT output depends on class usage across src/**, not just
  // app.css itself, so re-run PostCSS whenever any source file changes.
  fs.watch('src', { recursive: true }, () => { buildCss().catch(err => console.error(err)); });
  console.log('watching…');
} else {
  await esbuild.build(buildOpts);
  await buildCss();
  console.log('built to', outdir);
}
