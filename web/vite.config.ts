import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Собранный клиент кладём прямо в пакет webui — оттуда его забирает go:embed.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/webui/dist',
    // Каталог не вычищаем: в нём лежит .gitkeep, без которого go:embed
    // не соберётся на свежем клоне. Старые ассеты чистит Makefile.
    emptyOutDir: false,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://localhost:8080', changeOrigin: true, ws: true },
      '/healthz': 'http://localhost:8080',
    },
  },
})
