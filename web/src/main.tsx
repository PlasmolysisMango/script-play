import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import './index.css'
import { App } from './app/App'
import { applyTheme, readTheme } from './lib/theme'

// The inline script in index.html already did this before first paint; repeating
// it here keeps the theme correct if that script was blocked.
applyTheme(readTheme())

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      refetchOnWindowFocus: false,
      staleTime: 3000,
    },
  },
})

const root = document.getElementById('root')
if (!root) throw new Error('#root missing')

try {
  createRoot(root).render(
    <StrictMode>
      <QueryClientProvider client={queryClient}>
        <App />
      </QueryClientProvider>
    </StrictMode>,
  )
} catch (err) {
  // A blank dark page is impossible to debug remotely; show the boot card instead.
  const box = document.getElementById('boot-fallback')
  const reason = document.getElementById('boot-reason')
  if (reason) reason.textContent = `前端启动失败：${err instanceof Error ? err.message : String(err)}`
  if (box) box.hidden = false
  throw err
}
