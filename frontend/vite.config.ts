import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// In local development with the Docker stack running, requests proxy directly
// to the Caddy ingress at https://localhost without reopening internal host ports.
// Alternatively, if running with docker-compose.dev.yml, set VITE_PROXY_TARGET=http://localhost:8082.
const proxyTarget = process.env.VITE_PROXY_TARGET || 'https://localhost'
const isDirectGateway = proxyTarget.includes(':8082')

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
  ],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: proxyTarget,
        changeOrigin: true,
        secure: false,
      },
      '/health': {
        target: proxyTarget,
        changeOrigin: true,
        secure: false,
      },
      '/metrics': {
        target: proxyTarget,
        changeOrigin: true,
        secure: false,
      },
      '/prometheus': isDirectGateway
        ? {
            target: process.env.VITE_PROMETHEUS_TARGET || 'http://localhost:9090',
            changeOrigin: true,
            rewrite: (path) => path.replace(/^\/prometheus/, ''),
          }
        : {
            target: proxyTarget,
            changeOrigin: true,
            secure: false,
          },
    },
  },
})
