// The dashboard's color theme: dark (the default) or light.
//
// The choice is the `light` class on <html>, which flips Tailwind's palette
// (see src/theme-light.css). It is applied before React starts by the inline
// script in index.html, so a light page never flashes dark on load.

export type Theme = 'dark' | 'light'

const KEY = 'bomclaw.theme'

export function getTheme(): Theme {
  return document.documentElement.classList.contains('light') ? 'light' : 'dark'
}

export function setTheme(t: Theme) {
  document.documentElement.classList.toggle('light', t === 'light')
  try { localStorage.setItem(KEY, t) } catch { /* private mode: just not remembered */ }
  window.dispatchEvent(new CustomEvent('bomclaw-theme', { detail: t }))
}
