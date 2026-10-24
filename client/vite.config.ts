import { defineConfig } from 'vite'
import { createBuildProfilePlugin } from '@minbgames/web-kit/build-profile-plugin'

const native = process.env.MINB_APP_PLATFORM === 'android'
if (native && !process.env.VITE_RELAY_API_URL?.startsWith('https://')) {
  throw new Error('Android build requires VITE_RELAY_API_URL=https://.../v2/api')
}

export default defineConfig({
  plugins: [createBuildProfilePlugin()],
  base: './',
  server: {
    host: '127.0.0.1',
    port: 5173,
    strictPort: true,
    proxy: { '/v2/api': 'http://127.0.0.1:8090' },
    fs: { deny: ['.env*', '**/.git/**', '**/.secrets/**', '**/.cache/**', '**/key.pem', '**/Keystore/**', '*.keystore', '*.jks', '*.p12'] },
  },
  build: { emptyOutDir: true, outDir: process.env.MINB_APP_PLATFORM === 'android' ? '../_Build/dist-app' : '../_Build/dist', sourcemap: false },
})
