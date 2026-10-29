import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { viteStaticCopy } from 'vite-plugin-static-copy'

export default defineConfig({
  plugins: [react(), viteStaticCopy({ targets: [
    { src: 'node_modules/cesium/Build/Cesium/Workers', dest: 'cesium' },
    { src: 'node_modules/cesium/Build/Cesium/ThirdParty', dest: 'cesium' },
    { src: 'node_modules/cesium/Build/Cesium/Assets', dest: 'cesium' },
    { src: 'node_modules/cesium/Build/Cesium/Widgets', dest: 'cesium' },
  ] })],
  server: { proxy: { '/api': 'http://127.0.0.1:8080' } },
  build: { outDir: '../internal/api/static', emptyOutDir: false },
  // vite-plugin-static-copy preserves the package path beneath this destination.
  // Keep Cesium's runtime worker/asset lookup aligned with that embedded path.
  define: { CESIUM_BASE_URL: JSON.stringify('/cesium/node_modules/cesium/Build/Cesium') },
})
