// @ts-check
import { defineConfig } from 'astro/config';

import tailwindcss from '@tailwindcss/vite';

// https://astro.build/config
export default defineConfig({
  output: 'static',
  site: 'https://hwankishin.github.io', 
  // base: '/OpsView', // 만약 깃허브 기본 주소(hwankishin.github.io/OpsView)를 사용하신다면 주석을 해제하세요. 개인 도메인이면 이대로 둡니다.
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