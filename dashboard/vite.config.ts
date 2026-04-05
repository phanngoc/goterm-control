import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5174,
    proxy: {
      '/ws': { target: 'ws://127.0.0.1:19000', ws: true },
      '/health': { target: 'http://127.0.0.1:19000' },
    },
  },
  build: { outDir: 'dist' },
})
