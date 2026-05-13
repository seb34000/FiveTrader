import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  // Dev: proxy API and WS calls to the running Go bot
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:8080',
      '/ws': { target: 'ws://localhost:8080', ws: true },
    },
  },
  build: {
    // Output directly into the Go embed folder
    outDir: '../static',
    emptyOutDir: true,
  },
})
