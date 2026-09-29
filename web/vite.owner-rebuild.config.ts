import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

const rebuildRoot = fileURLToPath(new URL('./rebuild-owner', import.meta.url));
const rebuildOutput = fileURLToPath(new URL('./dist/owner-rebuild', import.meta.url));

export default defineConfig(({ command }) => ({
  root: rebuildRoot,
  base: command === 'serve' ? '/' : '/owner-rebuild/',
  plugins: [react()],
  build: {
    outDir: rebuildOutput,
    emptyOutDir: true,
  },
}));
