import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The Go binary embeds web/dist, so the UI builds there.
export default defineConfig({
  plugins: [react()],
  base: './',
  build: {
    outDir: '../dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:1234',
      '/v1': 'http://127.0.0.1:1234',
    },
  },
})
