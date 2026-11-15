import { useEffect, useState } from 'react'
import { getTheme, setTheme, type Theme } from '../lib/theme'

/** A sun/moon switch between the dark and light themes. */
export default function ThemeToggle({ className = '' }: { className?: string }) {
  const [theme, set] = useState<Theme>(getTheme)
  useEffect(() => {
    const on = (e: Event) => set((e as CustomEvent<Theme>).detail)
    window.addEventListener('bomclaw-theme', on)
    return () => window.removeEventListener('bomclaw-theme', on)
  }, [])
  const next: Theme = theme === 'light' ? 'dark' : 'light'
  return (
    <button
      onClick={() => setTheme(next)}
      title={next === 'light' ? 'Chuyển sang giao diện sáng' : 'Chuyển sang giao diện tối'}
      aria-label="Đổi giao diện sáng/tối"
      className={`text-gray-500 hover:text-gray-200 ${className}`}
    >
      {theme === 'light' ? '🌙' : '☀️'}
    </button>
  )
}
