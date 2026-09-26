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
  // es2022, not Vite's es2020 default: lowering xterm's `r ||= {}` for es2020
  // made esbuild emit an assignment to an undeclared variable, and every
  // DECRQM query (vim sends one on start) threw a ReferenceError.
  build: { outDir: 'dist', target: 'es2022' },
})
