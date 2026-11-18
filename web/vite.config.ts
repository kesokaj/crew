import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { tanstackStart } from '@tanstack/react-start/plugin/vite'

// TanStack Start in SPA mode: the build writes a static shell and the client bundle into dist/client,
// which the server image copies in and crew-server serves. No Node runs in production (design 15.1).
// The dev server passes the API to a running crew-server.
export default defineConfig({
  // The routes are one bundle, not a file each: a page load is a few requests, so a quick reload
  // stays under the ingress's rate limit (100 a second for each client).
  plugins: [tanstackStart({ spa: { enabled: true } }), react(), tailwindcss()],
  environments: { client: { build: { rollupOptions: { output: { manualChunks: () => 'app' } } } } },
  server: {
    proxy: { '/api': 'http://127.0.0.1:8080', '/healthz': 'http://127.0.0.1:8080' },
  },
  // The build's prerender starts a preview server and fetches the page from it. Named "localhost",
  // the server can listen on ::1 while the fetch goes to 127.0.0.1, which failed in an image build
  // on a kernel with IPv6 (the install test's Debian). One address for both.
  preview: { host: '127.0.0.1' },
})
