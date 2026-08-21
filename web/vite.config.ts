import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// The console is served by the same binary as the API, from `go:embed`, with no CDN anywhere in
// the picture: a strict deployment has no outbound network at all, and an asset that loads from
// somewhere else is an asset that does not load.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    // `web/dist` is Vite's alone and is gitignored, so emptying it is safe. The go:embed target is
    // a DIFFERENT directory — `internal/ui/dist`, which `make build-web` stages into — and the
    // `.gitkeep` committed there is what makes `//go:embed all:dist` compile on a clone where
    // nobody has run the web build: an embed pattern matching no files is a compile error, and the
    // Go package would otherwise stop building because a JavaScript toolchain had not been run.
    emptyOutDir: true,
    // Named `assets/` rather than hashed at the root so the Go handler can cache-bust on the
    // directory alone: everything under it carries a content hash in its filename and is
    // immutable, and `index.html` is the one file that must never be cached.
    assetsDir: 'assets',
    sourcemap: false,
  },
  server: {
    // `npm run dev` proxies the API to a locally running `tod-serve serve`, so the browser sees
    // one origin in development too. Anything else would need CORS, and the production deployment
    // has no cross-origin case at all.
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false },
    },
  },
})
