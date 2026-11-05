import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The dev server proxies the API and the websocket to the Go backend so the
// browser can use the same relative URLs as in production. SP_API_TARGET lets a
// second local backend (or a remote one) take the place of the default port.
const env = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env ?? {}
const target = env.SP_API_TARGET || 'http://127.0.0.1:8080'
const wsTarget = target.replace(/^http/, 'ws')

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': { target, changeOrigin: true },
      '/ws': { target: wsTarget, ws: true },
      // Pictures committed beside a script (scripts/<name>/images/…) are served
      // by the Go server in production; in dev the proxy has to stand in.
      '/script-assets': { target, changeOrigin: true },
      '/media': { target, changeOrigin: true },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
  },
})
