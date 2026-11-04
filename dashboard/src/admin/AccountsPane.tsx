import { useCallback, useEffect, useState } from 'react'

type Call = (method: string, params?: any) => Promise<any>

type Kind = 'setup_token' | 'access_token' | 'api_key'

interface TokenLive {
  agent_id: string
  current?: boolean
  uses?: number
  turns: number
  cooling_until?: string
  last_error?: string
  sessions?: number
}

interface TokenView {
  name: string
  kind: Kind
  masked: string
  rotate: boolean
  note?: string
  added: string
  updated: string
  live?: TokenLive[]
}

interface ProviderView {
  provider: 'claude' | 'codex'
  rotate_every: number
  tokens: TokenView[]
  agents: string[]
  other?: TokenLive[]
  other_names?: string[]
}

const PROVIDER_LABEL: Record<string, string> = { claude: 'Claude Code', codex: 'Codex' }

const KINDS: Record<string, { kind: Kind; label: string; help: string; placeholder: string }[]> = {
  claude: [
    {
      kind: 'setup_token', label: 'setup-token',
      help: 'Chạy `claude setup-token` trên máy có trình duyệt (cần gói Pro/Max/Team), dán token in ra. Hiệu lực một năm.',
      placeholder: 'sk-ant-oat01-…',
    },
    {
      kind: 'api_key', label: 'API key',
      help: 'Key từ console.anthropic.com — tính tiền theo token, không theo gói.',
      placeholder: 'sk-ant-api03-…',
    },
  ],
  codex: [
    {
      kind: 'access_token', label: 'access token',
      help: 'Token đăng nhập ChatGPT (như `codex login --with-access-token`). Token này hết hạn sau một thời gian — khi đó sửa và dán token mới.',
      placeholder: 'eyJ…',
    },
    {
      kind: 'api_key', label: 'API key',
      help: 'Key từ platform.openai.com — tính tiền theo token.',
      placeholder: 'sk-proj-…',
    },
  ],
}

const KIND_LABEL: Record<Kind, string> = { setup_token: 'setup-token', access_token: 'access token', api_key: 'API key' }

function until(stamp?: string): string {
  if (!stamp) return ''
  const ms = new Date(stamp).getTime() - Date.now()
  if (ms <= 0) return ''
  const m = Math.round(ms / 60000)
  return m >= 60 ? `${Math.floor(m / 60)}h${String(m % 60).padStart(2, '0')}` : `${m}m`
}

// LiveLine is how each agent is using one account right now.
function LiveLine({ l, every }: { l: TokenLive; every: number }) {
  const cooling = until(l.cooling_until)
  return (
    <div className="flex flex-wrap items-baseline gap-x-2 text-[11px]">
      <span className="font-mono text-gray-400">{l.agent_id}</span>
      {l.current && (
        <span className="px-1 rounded ring-1 ring-emerald-500/30 bg-emerald-500/10 text-emerald-300">
          đang dùng{every > 0 ? ` · ${l.uses ?? 0}/${every}` : ''}
        </span>
      )}
      <span className="text-gray-500">{l.turns} lượt</span>
      {cooling && <span className="text-amber-300">nghỉ {cooling}</span>}
      {l.last_error && <span className="text-red-300/90 truncate max-w-full" title={l.last_error}>{l.last_error}</span>}
    </div>
  )
}

interface Draft {
  old_name?: string
  name: string
  kind: Kind
  value: string
  rotate: boolean
  note: string
}

// TokenForm adds a token, or edits one. Editing never shows the value — the
// server does not send it — so an empty value field means "keep it".
function TokenForm({ provider, initial, busy, onSave, onCancel }: {
  provider: string; initial?: TokenView; busy: boolean
  onSave: (d: Draft) => Promise<boolean>; onCancel: () => void
}) {
  const kinds = KINDS[provider]
  const [d, setD] = useState<Draft>(() => initial
    ? { old_name: initial.name, name: initial.name, kind: initial.kind, value: '', rotate: initial.rotate, note: initial.note ?? '' }
    : { name: '', kind: kinds[0].kind, value: '', rotate: true, note: '' })
  const meta = kinds.find(k => k.kind === d.kind) ?? kinds[0]
  const editing = !!initial
  const valid = /^[a-z0-9][a-z0-9_.-]{0,47}$/.test(d.name) && (editing || d.value.trim() !== '')

  return (
    <div className="rounded-lg ring-1 ring-sky-500/30 bg-gray-950/60 p-3 space-y-2">
      <div className="flex flex-wrap gap-2">
        <label className="flex-1 min-w-[10rem]">
          <span className="block text-[10px] uppercase tracking-wider text-gray-600">tên</span>
          <input
            value={d.name}
            onChange={e => setD({ ...d, name: e.target.value.toLowerCase() })}
            placeholder="vd: max-ngoc, team-2"
            className="w-full mt-0.5 px-2 py-1 text-xs bg-gray-900 rounded ring-1 ring-gray-800 focus:ring-gray-600 text-gray-200 outline-none font-mono"
          />
        </label>
        <label>
          <span className="block text-[10px] uppercase tracking-wider text-gray-600">loại</span>
          <select
            value={d.kind}
            onChange={e => setD({ ...d, kind: e.target.value as Kind })}
            className="mt-0.5 px-2 py-1 text-xs bg-gray-900 rounded ring-1 ring-gray-800 text-gray-200 outline-none"
          >
            {kinds.map(k => <option key={k.kind} value={k.kind}>{k.label}</option>)}
          </select>
        </label>
      </div>
      <label className="block">
        <span className="block text-[10px] uppercase tracking-wider text-gray-600">giá trị</span>
        <input
          type="password"
          autoComplete="off"
          value={d.value}
          onChange={e => setD({ ...d, value: e.target.value })}
          placeholder={editing ? `${initial!.masked} — để trống để giữ nguyên` : meta.placeholder}
          className="w-full mt-0.5 px-2 py-1 text-xs bg-gray-900 rounded ring-1 ring-gray-800 focus:ring-gray-600 text-gray-200 outline-none font-mono"
        />
        <span className="block mt-1 text-[11px] text-gray-500">{meta.help}</span>
      </label>
      <label className="block">
        <span className="block text-[10px] uppercase tracking-wider text-gray-600">ghi chú</span>
        <input
          value={d.note}
          onChange={e => setD({ ...d, note: e.target.value })}
          placeholder="của ai, gói gì…"
          className="w-full mt-0.5 px-2 py-1 text-xs bg-gray-900 rounded ring-1 ring-gray-800 focus:ring-gray-600 text-gray-200 outline-none"
        />
      </label>
      <label className="flex items-center gap-2 text-xs text-gray-300">
        <input type="checkbox" checked={d.rotate} onChange={e => setD({ ...d, rotate: e.target.checked })} />
        Cho vào vòng xoay
        <span className="text-[11px] text-gray-500">— tắt nếu chỉ muốn dùng khi chọn đích danh (vd tài khoản của đồng nghiệp)</span>
      </label>
      <div className="flex items-center gap-2 pt-1">
        <button
          disabled={!valid || busy}
          onClick={async () => { if (await onSave(d)) onCancel() }}
          className="px-3 py-1 text-xs rounded bg-gray-100 text-gray-900 font-medium hover:bg-white disabled:opacity-40"
        >
          {editing ? 'Lưu' : 'Thêm'}
        </button>
        <button onClick={onCancel} className="text-xs text-gray-500 hover:text-gray-300">huỷ</button>
        <span className="ml-auto text-[11px] text-gray-600">Lưu vào file 0600 · agent dùng từ lượt kế tiếp</span>
      </div>
    </div>
  )
}

function ProviderSection({ pv, busy, act }: {
  pv: ProviderView; busy: boolean
  act: (method: string, params: any, confirmText?: string) => Promise<boolean>
}) {
  const [adding, setAdding] = useState(false)
  const [editing, setEditing] = useState<string | null>(null)
  const [every, setEvery] = useState(String(pv.rotate_every || ''))
  useEffect(() => { setEvery(String(pv.rotate_every || '')) }, [pv.rotate_every])

  const save = (d: Draft) => act('tokens.save', {
    provider: pv.provider, old_name: d.old_name, name: d.name, kind: d.kind,
    value: d.value, rotate: d.rotate, note: d.note,
  })

  const n = parseInt(every || '0', 10)
  const everyChanged = !Number.isNaN(n) && n !== (pv.rotate_every || 0)

  return (
    <section className="rounded-lg ring-1 ring-gray-800 bg-gray-900/40 p-4">
      <header className="flex flex-wrap items-baseline gap-2 pb-3 border-b border-gray-800">
        <h3 className="text-sm text-gray-100 font-medium">{PROVIDER_LABEL[pv.provider]}</h3>
        <span className="text-[11px] text-gray-500">
          {pv.agents.length ? `dùng bởi ${pv.agents.join(', ')}` : 'chưa có agent nào chạy provider này'}
        </span>
        <div className="ml-auto flex items-center gap-1.5 text-xs text-gray-400">
          đổi token sau
          <input
            value={every}
            onChange={e => setEvery(e.target.value.replace(/[^0-9]/g, ''))}
            placeholder="0"
            className="w-14 px-1.5 py-0.5 text-xs text-center bg-gray-950 rounded ring-1 ring-gray-800 text-gray-200 outline-none"
          />
          lượt
          {everyChanged && (
            <button
              disabled={busy}
              onClick={() => act('tokens.settings', { provider: pv.provider, rotate_every: n })}
              className="px-2 py-0.5 text-[11px] rounded bg-gray-100 text-gray-900 hover:bg-white disabled:opacity-40"
            >
              lưu
            </button>
          )}
        </div>
      </header>
      <p className="pt-2 text-[11px] text-gray-500 leading-relaxed">
        {(pv.rotate_every || 0) > 0
          ? <>Mỗi tài khoản trong vòng xoay phục vụ <strong className="text-gray-300">{pv.rotate_every}</strong> lượt (đếm trên mọi hội thoại) rồi chuyển sang cái kế tiếp — kể cả giữa một hội thoại. Cái nào hết hạn mức hay bị từ chối thì tự bỏ qua, và lượt đang chạy chuyển ngay sang cái kế tiếp.</>
          : <>0 = mỗi hội thoại ở yên trên tài khoản nó bắt đầu (theo <code>accounts.rotate_every</code> trong config). Token vẫn được dùng để chia hội thoại mới, và để chạy tiếp khi một tài khoản hết hạn mức.</>}
      </p>

      <ul className="mt-3 space-y-2">
        {pv.tokens.length === 0 && !adding && (
          <li className="text-xs text-gray-600">chưa có token nào</li>
        )}
        {pv.tokens.map(t => editing === t.name ? (
          <li key={t.name}>
            <TokenForm provider={pv.provider} initial={t} busy={busy} onSave={save} onCancel={() => setEditing(null)} />
          </li>
        ) : (
          <li key={t.name} className="rounded ring-1 ring-gray-800 px-3 py-2">
            <div className="flex flex-wrap items-baseline gap-2">
              <span className="text-sm text-gray-100 font-mono">{t.name}</span>
              <span className="text-[10px] px-1.5 py-0.5 rounded ring-1 ring-gray-700 text-gray-400">{KIND_LABEL[t.kind]}</span>
              <span className="text-[11px] text-gray-500 font-mono">{t.masked}</span>
              {!t.rotate && (
                <span title="Chỉ dùng khi chọn đích danh" className="text-[10px] px-1.5 py-0.5 rounded ring-1 ring-amber-500/30 text-amber-300">ngoài vòng xoay</span>
              )}
              <div className="ml-auto flex items-center gap-1.5">
                <button
                  disabled={busy}
                  onClick={() => act('tokens.save', { provider: pv.provider, old_name: t.name, name: t.name, kind: t.kind, rotate: !t.rotate, note: t.note })}
                  className="px-1.5 py-0.5 text-[11px] rounded ring-1 ring-gray-800 text-gray-400 hover:text-gray-200"
                >
                  {t.rotate ? 'bỏ khỏi vòng xoay' : 'cho vào vòng xoay'}
                </button>
                <button
                  onClick={() => { setEditing(t.name); setAdding(false) }}
                  className="px-1.5 py-0.5 text-[11px] rounded ring-1 ring-gray-800 text-gray-400 hover:text-sky-300"
                >
                  sửa
                </button>
                <button
                  disabled={busy}
                  onClick={() => act('tokens.delete', { provider: pv.provider, name: t.name },
                    `Xoá token "${t.name}" của ${PROVIDER_LABEL[pv.provider]}?\n\nCác hội thoại đang chạy trên nó sẽ chuyển sang tài khoản khác trong vòng xoay ở lượt kế tiếp.`)}
                  className="px-1.5 py-0.5 text-[11px] rounded ring-1 ring-gray-800 text-gray-500 hover:text-red-300 hover:ring-red-500/40"
                >
                  xoá
                </button>
              </div>
            </div>
            {t.note && <p className="mt-0.5 text-[11px] text-gray-500">{t.note}</p>}
            {(t.live?.length ?? 0) > 0
              ? <div className="mt-1 space-y-0.5">{t.live!.map(l => <LiveLine key={l.agent_id} l={l} every={pv.rotate_every} />)}</div>
              : <p className="mt-1 text-[11px] text-gray-600">chưa agent nào báo về token này (agent offline, hoặc chưa tới lượt kế tiếp)</p>}
          </li>
        ))}
        {adding && (
          <li><TokenForm provider={pv.provider} busy={busy} onSave={save} onCancel={() => setAdding(false)} /></li>
        )}
      </ul>

      {!adding && (
        <button
          onClick={() => { setAdding(true); setEditing(null) }}
          className="mt-3 px-2.5 py-1 text-xs rounded ring-1 ring-gray-700 text-gray-300 hover:text-sky-300 hover:ring-sky-500/40"
        >
          + thêm token
        </button>
      )}

      {(pv.other?.length ?? 0) > 0 && (
        <div className="mt-4 pt-3 border-t border-gray-800">
          <div className="text-[10px] uppercase tracking-wider text-gray-600 mb-1">
            tài khoản từ config.yaml (sửa trong file)
          </div>
          <ul className="space-y-1">
            {pv.other!.map((l, i) => (
              <li key={`${l.agent_id}-${i}`}>
                <span className="text-xs text-gray-300 font-mono mr-2">{pv.other_names?.[i]}</span>
                <LiveLine l={l} every={pv.rotate_every} />
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  )
}

// AccountsPane manages the credentials agents run on, per provider: the
// tokens and API keys they rotate through. Values are write-only here — the
// server keeps them and shows only a mask.
export default function AccountsPane({ call }: { call: Call }) {
  const [views, setViews] = useState<ProviderView[]>([])
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      setViews((await call('tokens.list')) || [])
      setErr(null)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call])

  // Live status (which token is current, cooldowns) changes with every turn.
  useEffect(() => {
    load()
    const t = setInterval(load, 10000)
    return () => clearInterval(t)
  }, [load])

  const act = useCallback(async (method: string, params: any, confirmText?: string) => {
    if (confirmText && !confirm(confirmText)) return false
    setBusy(true)
    try {
      setViews((await call(method, params)) || [])
      setErr(null)
      return true
    } catch (e: any) {
      setErr(String(e?.message ?? e))
      return false
    } finally {
      setBusy(false)
    }
  }, [call])

  return (
    <div className="h-full overflow-y-auto p-4">
      <div className="mb-4 max-w-3xl">
        <h2 className="text-sm text-gray-200 font-medium">Tài khoản</h2>
        <p className="mt-1 text-xs text-gray-500 leading-relaxed">
          Token và API key các agent dùng, theo từng provider. Thêm nhiều cái để chia tải: khi một tài khoản hết hạn
          mức, agent tự chuyển sang cái kế tiếp. Giá trị chỉ ghi được, không đọc lại được — trang này chỉ hiện vài
          ký tự cuối.
        </p>
      </div>
      {err && <div className="mb-3 text-xs text-red-300 bg-red-500/10 ring-1 ring-red-500/30 rounded p-2 max-w-3xl">{err}</div>}
      <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
        {views.map(pv => <ProviderSection key={pv.provider} pv={pv} busy={busy} act={act} />)}
      </div>
    </div>
  )
}
