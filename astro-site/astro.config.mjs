import { defineConfig } from 'astro/config';

export default defineConfig({
  site: 'https://hsh.ifup.org',
  output: 'static',
  server: {
    host: true
  },
  vite: {
    server: {
      allowedHosts: ['macmini.rhino-dragon.ts.net']
    }
  }
});
