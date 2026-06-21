import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

const apiTarget = process.env.VITE_API_TARGET || 'http://127.0.0.1:18443'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': apiTarget,
      '/tunnel': {
        target: apiTarget,
        ws: true,
      },
    },
  },
  build: {
    rolldownOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes('node_modules')) return
          if (id.includes('@mui') || id.includes('@emotion')) return 'mui'
          if (id.includes('react-router-dom')) return 'router'
          if (id.includes('react') || id.includes('react-dom')) return 'react'
          return 'vendor'
        },
      },
    },
  },
})
