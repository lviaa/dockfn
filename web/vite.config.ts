import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'node:path'

export default defineConfig({
  plugins: [vue()],
  base: './',
  build: { outDir: resolve(import.meta.dirname, '../internal/webui/dist'), emptyOutDir: true },
  server: { proxy: { '/api': 'http://localhost:32100' } },
  test: { environment: 'jsdom', globals: true, include: ['src/**/*.test.ts'] },
})
