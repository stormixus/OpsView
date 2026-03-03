import { defineConfig } from 'astro/config';
import tailwind from '@astrojs/tailwind';

export default defineConfig({
  site: 'https://stormixus.github.io',
  base: '/OpsView/',
  outDir: '../docs',
  integrations: [tailwind()],
  build: {
    assets: '_astro',
  },
});
