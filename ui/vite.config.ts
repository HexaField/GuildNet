import { defineConfig } from 'vite'
import solid from 'vite-plugin-solid'
import fs from 'node:fs'
import path from 'node:path'

export default defineConfig({
  plugins: [solid()],
  server: {
    port: 5173,
    https: (() => {
      const repoCrt = path.resolve(process.cwd(), '../certs/server.crt')
      const repoKey = path.resolve(process.cwd(), '../certs/server.key')
      if (fs.existsSync(repoCrt) && fs.existsSync(repoKey)) {
        return {
          cert: fs.readFileSync(repoCrt),
          key: fs.readFileSync(repoKey)
        } as any
      }
      return true // Last resort: Vite self-signed
    })(),
    proxy: {
      // Proxy API + SPA root to backend on 8090 for same-origin dev
      '^/(api|sse|proxy|ws)': {
        target: 'https://127.0.0.1:8090',
        changeOrigin: true,
        secure: false
      },
      '^/$': {
        target: 'https://127.0.0.1:8090',
        changeOrigin: true,
        secure: false
      }
    }
  },
  build: {
    target: 'esnext'
  }
})
