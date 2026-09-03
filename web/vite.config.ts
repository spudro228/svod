import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Собранный клиент кладём прямо в пакет webui — оттуда его забирает go:embed.
export default defineConfig({
  plugins: [react()],
  build: {
    // Две точки входа: приложение и отдельная страница для гостя.
    // У гостевой сборки нет кода, который умеет спрашивать дерево свода.
    rollupOptions: {
      input: {
        main: 'index.html',
        // Гостевой странице нужен только набор формул: разметку присылает
        // сервер готовой. Имена файлов фиксированы, потому что на них
        // ссылается сам сервер.
        math: 'src/math.ts',
        guest: 'src/guest.css',
      },
      output: {
        entryFileNames: (chunk) =>
          chunk.name === 'math' ? 'assets/math.js' : 'assets/[name]-[hash].js',
        assetFileNames: (info) =>
          info.names?.[0] === 'guest.css' ? 'assets/guest.css' : 'assets/[name]-[hash][extname]',
      },
    },
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
