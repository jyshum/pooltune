import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Relative base so the built folder works on any static host or from disk via a local server.
export default defineConfig({ base: './', publicDir: false, plugins: [react()], build: { sourcemap: false, rollupOptions: { input: { main: "index.html", setup: "setup.html" } } } });
