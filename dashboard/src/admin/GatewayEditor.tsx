import { useCallback, useEffect, useState } from 'react'
import type { ChannelGateway, ForwardMode, GatewayKind, GatewayList } from './types'
import { ago } from './format'

type Call = (method: string, params?: any) => Promise<any>

// Where a room speaks outside the dashboard.
//
// Registering a destination used to be a CLI command and nothing else, so
// giving a room a phone meant opening a terminal. And a room could only have
// one: the binding table keyed on the channel. Both are gone — this is a list,
// and the list can grow.
//
// The carrier (`via`) is the part that looks like bookkeeping and is not. Every
// gateway process runs the same sweep; a row is only ever seen by the process
// named on it. That is what keeps three gateways from all sending the same
// line, which is a bug this project has actually shipped once.

const MODE_LABEL: Record<ForwardMode, string> = {
  all: 'mọi dòng',
  mentions: 'khi được nhắc',
  off: 'tạm dừng',
}

const KIND_BADGE: Record<GatewayKind, { text: string; cls: string }> = {
  telegram: { text: 'TG', cls: 'bg-sky-500/15 text-sky-300 ring-sky-500/30' },
  webhook: { text: 'WH', cls: 'bg-violet-500/15 text-violet-300 ring-violet-500/30' },
}

const field = 'px-2 py-1.5 text-sm bg-gray-900 rounded ring-1 ring-gray-800 text-gray-200 outline-none focus:ring-gray-600'

export default function GatewayEditor({ call, channelID, channelName, agents, bots, onClose, onChanged }: {
  call: Call
  channelID: string
  channelName: string
  agents: string[]
  /** Which agents actually run a Telegram bot. One that does not cannot carry
   *  a telegram row, so it is not offered as a carrier for one. */
  bots?: Record<string, string>
  onClose: () => void
  onChanged?: () => void
}) {
  const [list, setList] = useState<GatewayList | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      const r: GatewayList = await call('channels.gateways', { channel_id: channelID })
      setList(r)
      setErr(null)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call, channelID])

  useEffect(() => { void load() }, [load])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const mutate = async (method: string, params: any) => {
    if (busy) return
    setBusy(true)
    try {
      await call(method, params)
      setErr(null)
      await load()
      onChanged?.()
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setBusy(false)
    }
  }

  const gateways = list?.gateways ?? []

  return (
    <div onClick={onClose} className="fixed inset-0 z-50 bg-black/70 flex items-center justify-center p-6">
      <div
        onClick={e => e.stopPropagation()}
        className="w-full max-w-3xl max-h-[88vh] flex flex-col rounded-xl bg-gray-950 ring-1 ring-gray-800 shadow-2xl"
      >
        <header className="flex items-baseline gap-3 px-5 py-3 border-b border-gray-800">
          <span className="text-sm text-gray-200 font-medium">Gateways</span>
          <span className="text-[11px] text-gray-500 font-mono truncate">#{channelName}</span>
          <button onClick={onClose} className="ml-auto text-xs text-gray-500 hover:text-gray-300">close</button>
        </header>

        <div className="flex-1 min-h-0 overflow-y-auto px-5 py-4 space-y-4">
          {list === null ? (
            <div className="text-sm text-gray-500">Loading…</div>
          ) : gateways.length === 0 ? (
            <div className="text-sm text-gray-500">
              Phòng này mới chỉ nói trong dashboard. Thêm một đích đến bên dưới — có thể thêm nhiều.
            </div>
          ) : (
            <div className="space-y-1">
              {gateways.map(g => (
                <Row
                  key={g.id} g={g} bots={bots} busy={busy}
                  onMode={mode => mutate('channels.bind', { id: g.id, mode })}
                  onRemove={() => {
                    if (!window.confirm(`Gỡ ${g.kind} ${g.target} khỏi #${channelName}?`)) return
                    void mutate('channels.unbind', { id: g.id })
                  }}
                />
              ))}
            </div>
          )}

          {list && (
            <AddForm
              list={list} agents={agents} bots={bots} busy={busy}
              onAdd={params => mutate('channels.bind', { channel_id: channelID, ...params })}
            />
          )}
        </div>

        <footer className="flex items-center gap-3 px-5 py-3 border-t border-gray-800">
          {err && <span className="text-xs text-red-300 truncate">{err}</span>}
          <span className="ml-auto text-[11px] text-gray-600">
            Chỉ những gì nói từ bây giờ mới đi — lịch sử của phòng ở lại đây.
          </span>
        </footer>
      </div>
    </div>
  )
}

function Row({ g, bots, busy, onMode, onRemove }: {
  g: ChannelGateway
  bots?: Record<string, string>
  busy: boolean
  onMode: (mode: ForwardMode) => void
  onRemove: () => void
}) {
  const badge = KIND_BADGE[g.kind] ?? { text: g.kind, cls: 'bg-gray-800 text-gray-300 ring-gray-700' }
  const bot = bots?.[g.agent_id]
  return (
    <div className="flex items-center gap-3 px-3 py-2 rounded bg-gray-900/50 ring-1 ring-gray-800">
      <span className={`shrink-0 px-1.5 py-0.5 text-[10px] font-mono rounded ring-1 ${badge.cls}`}>
        {badge.text}
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-sm text-gray-200 truncate" title={g.target}>
          {g.label || g.target}
        </div>
        <div className="text-[11px] text-gray-600 truncate">
          via {g.agent_id}{bot ? ` · @${bot}` : ''} · từ {ago(g.since)}
          {g.has_secret && ' · có secret'}
          {g.kind === 'webhook' && ' · một chiều'}
        </div>
      </div>
      <select
        value={g.mode} disabled={busy}
        onChange={e => onMode(e.target.value as ForwardMode)}
        title="Bao nhiêu phần của phòng đi ra đây"
        className={field}
      >
        {(Object.keys(MODE_LABEL) as ForwardMode[]).map(m => (
          <option key={m} value={m}>{MODE_LABEL[m]}</option>
        ))}
      </select>
      <button
        onClick={onRemove} disabled={busy}
        title="Gỡ đích đến này"
        className="shrink-0 px-2 py-1 text-sm text-gray-600 hover:text-red-300 disabled:opacity-40"
      >×</button>
    </div>
  )
}

function AddForm({ list, agents, bots, busy, onAdd }: {
  list: GatewayList
  agents: string[]
  bots?: Record<string, string>
  busy: boolean
  onAdd: (params: any) => void
}) {
  const [kind, setKind] = useState<GatewayKind>('telegram')
  const [target, setTarget] = useState('')
  const [secret, setSecret] = useState('')
  const [label, setLabel] = useState('')
  // Only an agent with a bot of its own can carry Telegram; the server refuses
  // the rest, and offering them would be offering an error.
  const carriers = kind === 'telegram' ? agents.filter(a => bots?.[a]) : agents
  const [agent, setAgent] = useState('')
  useEffect(() => {
    setAgent(a => (carriers.includes(a) ? a : (carriers.includes(list.default_agent) ? list.default_agent : carriers[0] ?? '')))
  }, [kind, list.default_agent, carriers.join(',')])

  const ready = kind === 'telegram' ? !!agent : !!agent && target.trim() !== ''

  return (
    <div className="pt-3 border-t border-gray-800 space-y-2">
      <div className="text-[11px] uppercase tracking-wide text-gray-500">Thêm đích đến</div>
      <div className="flex flex-wrap items-center gap-2">
        <select value={kind} onChange={e => { setKind(e.target.value as GatewayKind); setTarget('') }} className={field}>
          {list.kinds.map(k => <option key={k} value={k}>{k}</option>)}
        </select>
        <select value={agent} onChange={e => setAgent(e.target.value)} title="Tiến trình nào mang nó" className={field}>
          {carriers.map(a => <option key={a} value={a}>via {a}</option>)}
          {carriers.length === 0 && <option value="">không có agent nào mang được</option>}
        </select>
        {kind === 'telegram' ? (
          <input
            value={target} onChange={e => setTarget(e.target.value)}
            placeholder={list.default_target ? `chat riêng của bạn (${list.default_target})` : 'chat id'}
            className={`${field} flex-1 min-w-[12rem]`}
          />
        ) : (
          <input
            value={target} onChange={e => setTarget(e.target.value)}
            placeholder="https://hooks.slack.com/services/…"
            className={`${field} flex-1 min-w-[16rem]`}
          />
        )}
      </div>
      {kind === 'webhook' && (
        <div className="flex flex-wrap items-center gap-2">
          <input
            value={label} onChange={e => setLabel(e.target.value)}
            placeholder="tên hiển thị (tuỳ chọn)" className={`${field} w-48`}
          />
          <input
            value={secret} onChange={e => setSecret(e.target.value)}
            type="password" placeholder="bearer token (tuỳ chọn)" className={`${field} w-56`}
          />
          <span className="text-[11px] text-gray-600">
            Một chiều: không ai trả lời ngược vào phòng từ đây được.
          </span>
        </div>
      )}
      <div className="flex items-center gap-3">
        {kind === 'telegram' && carriers.length === 0 && (
          <span className="text-[11px] text-amber-300">
            Chưa agent nào đăng nhập bot Telegram, nên chưa ai mang được đích Telegram.
          </span>
        )}
        <button
          onClick={() => {
            onAdd({ kind, agent_id: agent, target: target.trim(), secret: secret || undefined, label: label || undefined })
            setTarget(''); setSecret(''); setLabel('')
          }}
          disabled={busy || !ready}
          className="ml-auto px-3 py-1.5 text-sm rounded bg-gray-100 text-gray-900 font-medium hover:bg-white disabled:opacity-40"
        >Thêm</button>
      </div>
    </div>
  )
}
