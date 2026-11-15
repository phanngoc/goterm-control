// Who is talking, at a glance. Three agents and a person share every room,
// and a monospace name in gray made them one voice: each gets a color and a
// round badge, the same everywhere it appears.

type Look = { badge: string; name: string; ring: string; soft: string }

const LOOKS: Record<string, Look> = {
  bomclaw: { badge: 'bg-sky-500 text-white', name: 'text-sky-300', ring: 'ring-sky-500/40', soft: 'bg-sky-500/10' },
  bomclaw2: { badge: 'bg-emerald-500 text-white', name: 'text-emerald-300', ring: 'ring-emerald-500/40', soft: 'bg-emerald-500/10' },
  bomclaw3: { badge: 'bg-violet-500 text-white', name: 'text-violet-300', ring: 'ring-violet-500/40', soft: 'bg-violet-500/10' },
}
const PERSON: Look = { badge: 'bg-amber-500 text-white', name: 'text-amber-300', ring: 'ring-amber-500/40', soft: 'bg-amber-500/10' }
const OTHERS: Look[] = [
  { badge: 'bg-orange-500 text-white', name: 'text-orange-300', ring: 'ring-orange-500/40', soft: 'bg-orange-500/10' },
  { badge: 'bg-blue-500 text-white', name: 'text-blue-300', ring: 'ring-blue-500/40', soft: 'bg-blue-500/10' },
  { badge: 'bg-red-500 text-white', name: 'text-red-300', ring: 'ring-red-500/40', soft: 'bg-red-500/10' },
]

const PEOPLE = new Set(['owner', 'you', 'bạn', 'user'])

/** The look of an author. A person (the owner) is always amber. */
export function agentLook(id: string, isPerson = false): Look {
  if (isPerson || PEOPLE.has(id)) return PERSON
  if (LOOKS[id]) return LOOKS[id]
  let h = 0
  for (const c of id) h = (h * 31 + c.charCodeAt(0)) >>> 0
  return OTHERS[h % OTHERS.length]
}

/** What the badge says: "1", "2", "3" for the numbered agents, else initials. */
function mark(id: string, isPerson: boolean): string {
  if (isPerson || PEOPLE.has(id)) return 'B'
  const n = id.match(/(\d+)$/)
  if (/^bomclaw\d*$/.test(id)) return n ? n[1] : '1'
  return id.replace(/[^A-Za-z0-9]/g, '').slice(0, 2).toUpperCase() || '?'
}

export function AgentAvatar({ id, person = false, size = 'md' }: { id: string; person?: boolean; size?: 'sm' | 'md' }) {
  const l = agentLook(id, person)
  const dim = size === 'sm' ? 'w-4 h-4 text-[9px]' : 'w-6 h-6 text-[11px]'
  return (
    <span title={person ? 'bạn' : id}
      className={`inline-grid place-items-center shrink-0 rounded-full font-semibold ${dim} ${l.badge}`}>
      {mark(id, person)}
    </span>
  )
}

/** Avatar and name, colored. */
export function AgentName({ id, person = false, size = 'md' }: { id: string; person?: boolean; size?: 'sm' | 'md' }) {
  const l = agentLook(id, person)
  return (
    <span className="inline-flex items-center gap-1.5 min-w-0">
      <AgentAvatar id={id} person={person} size={size} />
      <span className={`truncate font-semibold ${size === 'sm' ? 'text-[11px]' : 'text-[13px]'} ${l.name}`}>
        {person ? 'bạn' : id}
      </span>
    </span>
  )
}
