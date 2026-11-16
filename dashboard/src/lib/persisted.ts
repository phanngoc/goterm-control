import { useEffect, useRef, useState } from 'react'

// usePersisted is useState that survives a reload: the editor's layout — which
// panels are open, how wide, what each shows — is the person's, and a page
// that forgets it on every visit has to be set up again every time.
//
// key is stored as "editor.<key>". Pass a project-scoped key (e.g.
// `tabs.${channelID}`) for what belongs to one project: open tabs, the file in
// view, the page inside the preview. A change of key — the editor reused for
// another project — reads that key's value instead of carrying the old one.
export function usePersisted<T>(key: string, initial: T): [T, React.Dispatch<React.SetStateAction<T>>] {
  const read = (k: string): T => {
    try {
      const raw = localStorage.getItem('editor.' + k)
      return raw === null ? initial : (JSON.parse(raw) as T)
    } catch {
      return initial
    }
  }
  const [v, setV] = useState<T>(() => read(key))
  const keyRef = useRef(key)
  useEffect(() => {
    if (keyRef.current !== key) {
      keyRef.current = key
      setV(read(key))
      return
    }
    try { localStorage.setItem('editor.' + key, JSON.stringify(v)) } catch { /* private mode: layout just will not stick */ }
  }, [key, v]) // eslint-disable-line react-hooks/exhaustive-deps
  return [v, setV]
}
