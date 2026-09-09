// Presentation helpers shared by the admin views.

export function ms(n: number): string {
  if (!n) return '—'
  if (n < 1000) return `${n}ms`
  if (n < 60_000) return `${(n / 1000).toFixed(n < 10_000 ? 2 : 1)}s`
  const m = Math.floor(n / 60_000)
  return `${m}m ${Math.round((n % 60_000) / 1000)}s`
}

export function tokens(n: number): string {
  if (!n) return '0'
  if (n < 1000) return String(n)
  if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k`
  return `${(n / 1_000_000).toFixed(1)}M`
}

export function ago(iso?: string): string {
  if (!iso) return '—'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return '—'
  const d = Date.now() - t
  if (d < 0) return 'in ' + ago(new Date(Date.now() * 2 - t).toISOString())
  if (d < 60_000) return `${Math.max(0, Math.floor(d / 1000))}s ago`
  if (d < 3_600_000) return `${Math.floor(d / 60_000)}m ago`
  if (d < 86_400_000) return `${Math.floor(d / 3_600_000)}h ago`
  return `${Math.floor(d / 86_400_000)}d ago`
}

export function clock(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  return Number.isNaN(d.getTime())
    ? ''
    : d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

// Each run type gets one hue, used consistently for its badge and its
// waterfall bar so the eye can follow a kind of work down the tree.
export const RUN_COLORS: Record<string, { bar: string; text: string; chip: string }> = {
  chain:  { bar: 'bg-violet-500',  text: 'text-violet-300',  chip: 'bg-violet-500/15 text-violet-300 ring-violet-500/30' },
  llm:    { bar: 'bg-sky-500',     text: 'text-sky-300',     chip: 'bg-sky-500/15 text-sky-300 ring-sky-500/30' },
  tool:   { bar: 'bg-emerald-500', text: 'text-emerald-300', chip: 'bg-emerald-500/15 text-emerald-300 ring-emerald-500/30' },
  memory: { bar: 'bg-amber-500',   text: 'text-amber-300',   chip: 'bg-amber-500/15 text-amber-300 ring-amber-500/30' },
  task:   { bar: 'bg-fuchsia-500', text: 'text-fuchsia-300', chip: 'bg-fuchsia-500/15 text-fuchsia-300 ring-fuchsia-500/30' },
}

export function runColor(type: string) {
  return RUN_COLORS[type] ?? { bar: 'bg-gray-500', text: 'text-gray-300', chip: 'bg-gray-500/15 text-gray-300 ring-gray-500/30' }
}

export const TASK_STATE_STYLE: Record<string, string> = {
  submitted:        'bg-sky-500/15 text-sky-300 ring-sky-500/30',
  working:          'bg-amber-500/15 text-amber-300 ring-amber-500/30',
  completed:        'bg-emerald-500/15 text-emerald-300 ring-emerald-500/30',
  failed:           'bg-red-500/15 text-red-300 ring-red-500/30',
  canceled:         'bg-gray-500/15 text-gray-400 ring-gray-500/30',
  rejected:         'bg-orange-500/15 text-orange-300 ring-orange-500/30',
  'input-required': 'bg-violet-500/15 text-violet-300 ring-violet-500/30',
  blocked:          'bg-violet-500/15 text-violet-300 ring-violet-500/30',
}

// Run liveness colours: green for done, amber for "call me back", violet for
// waiting, red for broke, grey for the run itself being cut off.
export const RUN_LIVENESS_STYLE: Record<string, string> = {
  running:   'bg-amber-500/15 text-amber-300 ring-amber-500/30',
  completed: 'bg-emerald-500/15 text-emerald-300 ring-emerald-500/30',
  advanced:  'bg-sky-500/15 text-sky-300 ring-sky-500/30',
  plan_only: 'bg-orange-500/15 text-orange-300 ring-orange-500/30',
  empty:     'bg-orange-500/15 text-orange-300 ring-orange-500/30',
  blocked:   'bg-violet-500/15 text-violet-300 ring-violet-500/30',
  failed:    'bg-red-500/15 text-red-300 ring-red-500/30',
  timed_out: 'bg-red-500/15 text-red-300 ring-red-500/30',
  canceled:  'bg-gray-500/15 text-gray-400 ring-gray-500/30',
}

export function runLivenessStyle(l: string): string {
  return RUN_LIVENESS_STYLE[l] ?? 'bg-gray-500/15 text-gray-400 ring-gray-500/30'
}

export function taskStateStyle(state: string): string {
  return TASK_STATE_STYLE[state] ?? 'bg-gray-500/15 text-gray-400 ring-gray-500/30'
}

// truncate keeps long payloads from blowing out a table row.
export function truncate(s: string | undefined, n: number): string {
  if (!s) return ''
  return s.length <= n ? s : s.slice(0, n - 1) + '…'
}

// Go marshals a zero time.Time as 0001-01-01T00:00:00Z — `omitempty` does not
// drop struct zero values — so an "unset" timestamp arrives looking like a
// date 2000 years ago. Treat it as absent.
export function isZeroTime(iso?: string): boolean {
  return !iso || iso.startsWith('0001-')
}

// rel renders a timestamp relative to now in either direction: "3m ago" for
// the past, "in 3m" for the future. ago() is past-only and its future branch
// reads "in 3m ago", which is fine for things that are always past (created_at)
// and wrong for a next-run column.
export function rel(iso?: string): string {
  if (isZeroTime(iso)) return '—'
  const t = new Date(iso!).getTime()
  if (Number.isNaN(t)) return '—'
  const d = Date.now() - t
  const abs = Math.abs(d)
  let span: string
  if (abs < 60_000) span = `${Math.max(0, Math.floor(abs / 1000))}s`
  else if (abs < 3_600_000) span = `${Math.floor(abs / 60_000)}m`
  else if (abs < 86_400_000) span = `${Math.floor(abs / 3_600_000)}h`
  else span = `${Math.floor(abs / 86_400_000)}d`
  return d >= 0 ? `${span} ago` : `in ${span}`
}
