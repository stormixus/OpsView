// @ts-check
import { defineConfig } from 'astro/config';

import tailwindcss from '@tailwindcss/vite';

// https://astro.build/config
export default defineConfig({
  output: 'static',
  site: 'https://hwankishin.github.io', // Placeholder, but handles absolute URLs nicely
  base: '/OpsView', // The repo name, assuming standard GitHub Pages setup for a project site
  vite: {
    plugins: [tailwindcss()]
  },
  i18n: {
    defaultLocale: 'en',
    locales: ['en', 'ko', 'ja', 'pt', 'es'],
    routing: {
      prefixDefaultLocale: false,
    }
  }
});