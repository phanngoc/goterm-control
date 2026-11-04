import { lazy, Suspense, useCallback, useEffect, useMemo, useState } from 'react'
import type { AgentPlugins, HubPlugin, HubSetting, PluginStatus } from './types'

type Call = (method: string, params?: any) => Promise<any>

// Monaco is heavy; the file browser loads only when a plugin is opened.
const PluginFiles = lazy(() => import('./PluginFiles'))

// The plugins hub.
//
// Every row is computed by the same plugins.Load a turn calls, so the status
// and the reason on screen are what the agent is acting under — "why is that
// plugin not doing anything" is answered by looking, not by reading logs.
// Everything here takes effect on the agent's next turn, with no restart.
//
// Laid out by agent, like the skills hub: the question people bring is "what
// can this one do", and copying a plugin across is a control on the row.

const STATUS_STYLE: Record<PluginStatus, string> = {
  active: 'ring-emerald-500/30 bg-emerald-500/10 text-emerald-300',
  pending: 'ring-amber-500/30 bg-amber-500/10 text-amber-300',
  disabled: 'ring-gray-700 text-gray-500',
  broken: 'ring-red-500/30 bg-red-500/10 text-red-300',
}

const STATUS_LABEL: Record<PluginStatus, string> = {
  active: 'đang chạy',
  pending: 'đang chờ',
  disabled: 'đã tắt',
  broken: 'hỏng',
}

const SCOPE_TITLE: Record<HubPlugin['scope'], string> = {
  agent: 'Plugin của chính agent này',
  installed: 'Cài từ nguồn ngoài — code của người khác',
  project: 'Đi kèm một repo — code của người khác',
}

function Chip({ children, title, tone = 'gray' }: { children: React.ReactNode; title?: string; tone?: 'gray' | 'sky' | 'amber' }) {
  const tones = {
    gray: 'ring-gray-700 text-gray-400',
    sky: 'ring-sky-500/30 bg-sky-500/10 text-sky-300',
    amber: 'ring-amber-500/30 bg-amber-500/10 text-amber-300',
  }
  return (
    <span title={title} className={`text-[10px] px-1.5 py-0.5 rounded ring-1 ${tones[tone]}`}>{children}</span>
  )
}

// What a plugin brings, as chips. A bare skill brings itself and nothing else,
// so it gets no chips: the kind badge already says so.
function Contributions({ p }: { p: HubPlugin }) {
  if (p.kind === 'skill') return null
  const items: [string, string | undefined][] = []
  if (p.skills?.length) items.push([`${p.skills.length} skill`, p.skills.join(', ')])
  if (p.commands?.length) items.push([`${p.commands.length} lệnh`, p.commands.join(', ')])
  if (p.mcp?.length) items.push([`${p.mcp.length} MCP`, p.mcp.join(', ')])
  if (p.hooks?.length) items.push([`${p.hooks.length} hook`, p.hooks.join('\n')])
  if (p.prompt) items.push(['prompt', p.prompt])
  if (items.length === 0 && !p.provides?.length && !p.injects?.length) return null
  return (
    <div className="mt-1 flex flex-wrap gap-1">
      {items.map(([label, title]) => <Chip key={label} title={title} tone="sky">{label}</Chip>)}
      {p.provides?.map(c => <Chip key={'p' + c} title="Năng lực plugin này cung cấp cho plugin khác">cung cấp {c}</Chip>)}
      {p.injects?.map(c => <Chip key={'i' + c} title="Cần từ plugin khác (? = không bắt buộc)">cần {c}</Chip>)}
    </div>
  )
}

// SettingsForm edits a plugin's config against its schema. Only fields the
// person touched are sent; a secret is never shown, only whether it is set.
function SettingsForm({ settings, onSave, busy }: {
  settings: HubSetting[]; busy: boolean; onSave: (values: Record<string, string>) => void
}) {
  const [draft, setDraft] = useState<Record<string, string>>({})
  const changed = Object.keys(draft).length > 0
  const set = (key: string, v: string) => setDraft(d => ({ ...d, [key]: v }))

  return (
    <div className="mt-2 rounded bg-gray-950/60 ring-1 ring-gray-800 p-2 space-y-2" onClick={e => e.stopPropagation()}>
      {settings.map(s => {
        const value = draft[s.key] ?? (s.secret ? '' : s.value ?? '')
        const placeholder = s.secret
          ? (s.set ? '•••• đã đặt — nhập để thay' : (s.env ? `chưa đặt — hoặc lấy từ $${s.env}` : 'chưa đặt'))
          : (s.default ? `mặc định: ${s.default}` : s.env ? `hoặc lấy từ $${s.env}` : '')
        return (
          <label key={s.key} className="block">
            <div className="flex items-baseline gap-2">
              <span className="text-xs text-gray-200 font-mono">{s.key}</span>
              {s.required && <span className="text-[10px] text-amber-300">bắt buộc</span>}
              {s.secret && <span className="text-[10px] text-gray-500">bí mật</span>}
              <span className="ml-auto text-[10px] text-gray-600 font-mono" title="Tên biến mà code của plugin đọc">${s.var}</span>
            </div>
            {s.description && <div className="text-[11px] text-gray-500">{s.description}</div>}
            <div className="mt-1 flex gap-1.5">
              {s.enum?.length ? (
                <select
                  value={value}
                  onChange={e => set(s.key, e.target.value)}
                  className="flex-1 px-2 py-1 text-xs bg-gray-900 rounded ring-1 ring-gray-800 text-gray-200 outline-none"
                >
                  <option value="">{s.default ? `(mặc định: ${s.default})` : '(chưa chọn)'}</option>
                  {s.enum.map(v => <option key={v} value={v}>{v}</option>)}
                </select>
              ) : (
                <input
                  type={s.secret ? 'password' : 'text'}
                  value={value}
                  placeholder={placeholder}
                  autoComplete="off"
                  onChange={e => set(s.key, e.target.value)}
                  className="flex-1 px-2 py-1 text-xs bg-gray-900 rounded ring-1 ring-gray-800 focus:ring-gray-600 text-gray-200 outline-none font-mono"
                />
              )}
              {s.configured && draft[s.key] !== '' && (
                <button
                  type="button"
                  onClick={() => set(s.key, '')}
                  title="Xoá giá trị đã đặt — dùng lại mặc định hoặc biến môi trường"
                  className="px-1.5 text-[11px] rounded ring-1 ring-gray-800 text-gray-500 hover:text-red-300"
                >
                  xoá
                </button>
              )}
            </div>
          </label>
        )
      })}
      <div className="flex items-center gap-2 pt-1">
        <button
          disabled={!changed || busy}
          onClick={() => { onSave(draft); setDraft({}) }}
          className="px-2.5 py-1 text-xs rounded bg-gray-100 text-gray-900 font-medium hover:bg-white disabled:opacity-40"
        >
          Lưu
        </button>
        {changed && (
          <button onClick={() => setDraft({})} className="text-xs text-gray-500 hover:text-gray-300">huỷ</button>
        )}
      </div>
    </div>
  )
}

function PluginRow({ p, agent, peers, busy, act, onBrowse }: {
  p: HubPlugin; agent: string; peers: string[]; busy: boolean
  act: (method: string, params: any, confirmText?: string) => Promise<void>
  onBrowse: () => void
}) {
  const [open, setOpen] = useState(false)
  const enabled = p.status !== 'disabled'
  const removable = p.kind === 'plugin' && p.scope !== 'project'
  const base = { agent_id: agent, name: p.name }

  return (
    <li className="border-b border-gray-800/60 last:border-0 py-2">
      <div className="flex items-baseline gap-2 flex-wrap">
        <button
          onClick={() => setOpen(o => !o)}
          className="text-sm text-gray-100 hover:text-sky-300 text-left"
          title="Xem chi tiết"
        >
          {p.name}
        </button>
        <span className={`text-[10px] px-1.5 py-0.5 rounded ring-1 ${STATUS_STYLE[p.status]}`}>{STATUS_LABEL[p.status]}</span>
        {p.kind === 'skill' && <Chip title="Một thư mục skill trơn — cũng là plugin, chỉ đóng góp chính skill đó">skill</Chip>}
        {p.scope !== 'agent' && <Chip title={SCOPE_TITLE[p.scope]} tone="amber">{p.scope === 'installed' ? 'cài ngoài' : 'repo'}</Chip>}
        {p.version && <span className="text-[10px] text-gray-600 font-mono">v{p.version}</span>}

        <div className="ml-auto flex items-center gap-1.5">
          <button
            onClick={onBrowse}
            title="Xem plugin gồm những file gì và đọc nội dung từng file"
            className="px-1.5 py-0.5 text-[11px] rounded ring-1 ring-gray-800 text-gray-400 hover:text-sky-300 hover:ring-sky-500/40"
          >
            file
          </button>
          {p.needs_trust && (
            <button
              disabled={busy}
              onClick={() => act('plugins.set', { ...base, trusted: !p.trusted },
                p.trusted ? undefined
                  : `Tin cậy "${p.name}" trên ${agent}?\n\nLệnh, biến môi trường, MCP server và hook của nó sẽ chạy ở mọi lượt kế tiếp của agent${p.hooks?.length ? ' — hook chạy trước MỌI lần agent gọi tool khớp' : ''}. Đây là code của người khác — hãy bấm "file" để đọc nó trước.\n\n${p.dir}`)}
              className={`px-1.5 py-0.5 text-[11px] rounded ring-1 ${p.trusted
                ? 'ring-gray-800 text-gray-500 hover:text-amber-300'
                : 'ring-amber-500/40 text-amber-300 hover:bg-amber-500/10'}`}
            >
              {p.trusted ? 'bỏ tin cậy' : 'tin cậy'}
            </button>
          )}
          <button
            disabled={busy || p.status === 'broken'}
            onClick={() => act('plugins.set', { ...base, enabled: !enabled })}
            title={enabled ? 'Tắt — agent không thấy plugin này từ lượt sau' : 'Bật lại'}
            className={`px-1.5 py-0.5 text-[11px] rounded ring-1 ring-gray-800 ${enabled ? 'text-gray-400 hover:text-gray-200' : 'text-emerald-300'}`}
          >
            {enabled ? 'tắt' : 'bật'}
          </button>
        </div>
      </div>

      {p.description && <p className="mt-0.5 text-xs text-gray-500 leading-snug">{p.description}</p>}

      {p.status !== 'active' && p.status !== 'disabled' && p.reasons?.map(r => (
        <p key={r} className={`mt-0.5 text-[11px] ${p.status === 'broken' ? 'text-red-300' : 'text-amber-300'}`}>• {r}</p>
      ))}
      {p.untrusted && (
        <p className="mt-0.5 text-[11px] text-amber-300/80">
          • chưa tin cậy: skill và prompt đã dùng được; lệnh, env và MCP đang tắt
        </p>
      )}

      <Contributions p={p} />

      {open && (
        <div className="mt-2 space-y-1.5">
          <p className="text-[10px] text-gray-600 font-mono break-all">{p.dir}</p>
          {p.source && (
            <p className="text-[11px] text-gray-500">
              nguồn: <span className="font-mono text-gray-400 break-all">{p.source.url}</span>
              {p.source.ref && <> @ <span className="font-mono">{p.source.ref}</span></>}
              {p.source.path && <> / <span className="font-mono">{p.source.path}</span></>}
              {p.source.commit && <> · <span className="font-mono">{p.source.commit.slice(0, 12)}</span></>}
            </p>
          )}
          {p.commands?.length ? <p className="text-[11px] text-gray-500">lệnh: <span className="font-mono text-gray-400">{p.commands.join(' ')}</span></p> : null}
          {p.mcp?.length ? <p className="text-[11px] text-gray-500">MCP: <span className="font-mono text-gray-400">{p.mcp.join(', ')}</span></p> : null}
          {p.hooks?.length ? (
            <p className="text-[11px] text-gray-500" title="Chạy quanh mỗi lần agent gọi tool khớp">
              hook: <span className="font-mono text-gray-400">{p.hooks.join(' · ')}</span>
            </p>
          ) : null}
          {p.prompt && <pre className="text-[11px] text-gray-400 whitespace-pre-wrap bg-gray-950/60 rounded p-2 ring-1 ring-gray-800">{p.prompt}</pre>}

          {p.settings?.length ? (
            <SettingsForm
              settings={p.settings}
              busy={busy}
              onSave={values => act('plugins.config', { ...base, values })}
            />
          ) : null}

          <div className="flex items-center gap-1.5 pt-1">
            {p.source && (
              <button
                disabled={busy}
                onClick={() => act('plugins.update', base,
                  `Cập nhật "${p.name}" từ ${p.source!.url}?\n\nCode mới sẽ ở trạng thái chưa tin cậy cho tới khi bạn tin cậy lại.`)}
                className="px-1.5 py-0.5 text-[11px] rounded ring-1 ring-gray-800 text-gray-400 hover:text-sky-300"
              >
                cập nhật
              </button>
            )}
            {removable && peers.length > 0 && (
              <select
                value=""
                disabled={busy}
                onChange={e => {
                  const to = e.target.value
                  if (to) act('plugins.copy', { from: agent, agent_id: to, name: p.name },
                    `Chép "${p.name}" của ${agent} sang ${to}?\n\nBản hiện có trên ${to} (nếu có) sẽ bị thay, và ${to} phải tin cậy lại.`)
                }}
                className="px-1.5 py-0.5 text-[11px] bg-gray-950 rounded ring-1 ring-gray-800 text-gray-400 outline-none"
              >
                <option value="">chép sang…</option>
                {peers.map(a => <option key={a} value={a}>{a}</option>)}
              </select>
            )}
            {removable && (
              <button
                disabled={busy}
                onClick={() => act('plugins.uninstall', base,
                  `Gỡ "${p.name}" khỏi ${agent}?\n\nThư mục và các cấu hình của nó sẽ bị xoá.`)}
                className="ml-auto px-1.5 py-0.5 text-[11px] rounded ring-1 ring-gray-800 text-gray-500 hover:text-red-300 hover:ring-red-500/40"
              >
                gỡ
              </button>
            )}
          </div>
        </div>
      )}
    </li>
  )
}

// InstallBar fetches a plugin from a git URL (or a folder on this machine)
// into one agent. Untrusted by default: trusting is a separate, deliberate
// click once someone has read what arrived.
function InstallBar({ agents, busy, onInstall }: {
  agents: string[]; busy: boolean
  onInstall: (params: Record<string, any>) => Promise<void>
}) {
  const [agent, setAgent] = useState('')
  const [url, setUrl] = useState('')
  const [ref, setRef] = useState('')
  const [path, setPath] = useState('')
  const [more, setMore] = useState(false)
  const target = agent || agents[0] || ''

  const submit = async () => {
    if (!url.trim() || !target) return
    await onInstall({ agent_id: target, url: url.trim(), ref: ref.trim(), path: path.trim() })
    setUrl(''); setRef(''); setPath('')
  }

  return (
    <div className="mb-4 max-w-3xl rounded-lg ring-1 ring-gray-800 bg-gray-900/40 p-3">
      <div className="flex flex-wrap gap-2 items-center">
        <span className="text-xs text-gray-400">Cài plugin</span>
        <input
          value={url}
          onChange={e => setUrl(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') submit() }}
          placeholder="https://github.com/owner/repo.git — hoặc một thư mục trên máy"
          className="flex-1 min-w-[16rem] px-2 py-1 text-xs bg-gray-950 rounded ring-1 ring-gray-800 focus:ring-gray-600 text-gray-200 outline-none font-mono"
        />
        <select
          value={target}
          onChange={e => setAgent(e.target.value)}
          className="px-2 py-1 text-xs bg-gray-950 rounded ring-1 ring-gray-800 text-gray-300 outline-none"
        >
          {agents.map(a => <option key={a} value={a}>{a}</option>)}
        </select>
        <button onClick={() => setMore(m => !m)} className="text-[11px] text-gray-500 hover:text-gray-300">
          {more ? 'bớt' : 'tuỳ chọn'}
        </button>
        <button
          disabled={busy || !url.trim() || !target}
          onClick={submit}
          className="px-2.5 py-1 text-xs rounded bg-gray-100 text-gray-900 font-medium hover:bg-white disabled:opacity-40"
        >
          {busy ? 'Đang cài…' : 'Cài'}
        </button>
      </div>
      {more && (
        <div className="mt-2 flex flex-wrap gap-2">
          <input value={ref} onChange={e => setRef(e.target.value)} placeholder="branch / tag"
            className="w-40 px-2 py-1 text-xs bg-gray-950 rounded ring-1 ring-gray-800 text-gray-200 outline-none font-mono" />
          <input value={path} onChange={e => setPath(e.target.value)} placeholder="thư mục con trong repo, vd plugins/forge"
            className="flex-1 min-w-[14rem] px-2 py-1 text-xs bg-gray-950 rounded ring-1 ring-gray-800 text-gray-200 outline-none font-mono" />
        </div>
      )}
      <p className="mt-1.5 text-[11px] text-gray-600">
        Cài xong ở trạng thái <span className="text-amber-300">chưa tin cậy</span>: skill và prompt dùng được ngay,
        lệnh và MCP chỉ chạy sau khi bạn bấm <em>tin cậy</em>.
      </p>
    </div>
  )
}

type Filter = 'all' | 'plugin' | 'skill' | 'attention'

export default function PluginsPane({ call }: { call: Call }) {
  const [rows, setRows] = useState<AgentPlugins[]>([])
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [filter, setFilter] = useState<Filter>('all')
  // Which plugin's files are open in the browser.
  const [browsing, setBrowsing] = useState<{ agent: string; name: string } | null>(null)

  const load = useCallback(async () => {
    try {
      setRows((await call('plugins.list')) || [])
      setErr(null)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call])

  useEffect(() => { load() }, [load])

  const act = useCallback(async (method: string, params: any, confirmText?: string) => {
    if (confirmText && !confirm(confirmText)) return
    setBusy(true)
    try {
      await call(method, params)
      setErr(null)
      await load()
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setBusy(false)
    }
  }, [call, load])

  const agentIDs = rows.map(r => r.agent_id)
  const shown = useMemo(() => (list: HubPlugin[]) => list.filter(p => {
    switch (filter) {
      case 'plugin': return p.kind === 'plugin'
      case 'skill': return p.kind === 'skill'
      case 'attention': return p.status === 'pending' || p.status === 'broken' || p.untrusted
      default: return true
    }
  }), [filter])

  const attention = rows.reduce((n, r) =>
    n + r.plugins.filter(p => p.status === 'pending' || p.status === 'broken' || p.untrusted).length, 0)

  return (
    <div className="h-full overflow-y-auto p-4">
      <div className="mb-4 max-w-2xl">
        <h2 className="text-sm text-gray-200 font-medium">Plugins</h2>
        <p className="mt-1 text-xs text-gray-500 leading-relaxed">
          Mọi thứ agent làm được ngoài công cụ có sẵn của CLI đều là plugin: lệnh, MCP server, cấu hình,
          prompt — và cả skill trơn. Plugin thiếu thứ gì thì ở trạng thái <span className="text-amber-300">đang chờ</span>{' '}
          kèm lý do, và tự chạy khi thứ đó có.
        </p>
        <p className="mt-1 text-xs text-gray-600">Mọi thay đổi có hiệu lực ở lượt kế tiếp của agent, không cần restart.</p>
      </div>

      <InstallBar agents={agentIDs} busy={busy} onInstall={params => act('plugins.install', params)} />

      <div className="mb-3 flex items-center gap-1.5">
        {([['all', 'tất cả'], ['plugin', 'plugin'], ['skill', 'skill'], ['attention', `cần xem${attention ? ` (${attention})` : ''}`]] as [Filter, string][]).map(([k, label]) => (
          <button
            key={k}
            onClick={() => setFilter(k)}
            className={`px-2 py-0.5 text-[11px] rounded ring-1 ${filter === k
              ? 'ring-sky-500/40 bg-sky-500/10 text-sky-300'
              : 'ring-gray-800 text-gray-500 hover:text-gray-300'}`}
          >
            {label}
          </button>
        ))}
      </div>

      {err && <div className="mb-3 text-xs text-red-300 bg-red-500/10 ring-1 ring-red-500/30 rounded p-2 whitespace-pre-wrap">{err}</div>}

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
        {rows.map(r => {
          const list = shown(r.plugins)
          return (
            <section key={r.agent_id} className="rounded-lg ring-1 ring-gray-800 bg-gray-900/40 p-3">
              <header className="flex items-baseline gap-2 pb-2 border-b border-gray-800">
                <span className="font-mono text-sm text-gray-100">{r.agent_id}</span>
                {r.provider && <span className="text-[11px] text-gray-500">{r.provider}</span>}
                <span className={`ml-auto text-[10px] px-1.5 py-0.5 rounded ring-1 ${r.online
                  ? 'ring-emerald-500/30 bg-emerald-500/10 text-emerald-300'
                  : 'ring-gray-700 text-gray-500'}`}>
                  {r.online ? 'online' : 'offline'}
                </span>
              </header>
              {r.error ? (
                <p className="pt-2 text-xs text-red-300">{r.error}</p>
              ) : (
                <ul className="pt-1">
                  {list.length === 0 && <li className="py-3 text-xs text-gray-600">không có gì ở đây</li>}
                  {list.map(p => (
                    <PluginRow
                      key={p.name} p={p} agent={r.agent_id} busy={busy} act={act}
                      onBrowse={() => setBrowsing({ agent: r.agent_id, name: p.name })}
                      peers={agentIDs.filter(a => a !== r.agent_id)}
                    />
                  ))}
                </ul>
              )}
              {r.warnings?.map(w => <p key={w} className="pt-1 text-[11px] text-amber-300/80">⚠ {w}</p>)}
            </section>
          )
        })}
      </div>

      {browsing && (
        <Suspense fallback={<div className="fixed inset-0 z-50 bg-black/70 flex items-center justify-center text-sm text-gray-400">Đang mở…</div>}>
          <PluginFiles call={call} agent={browsing.agent} name={browsing.name} onClose={() => setBrowsing(null)} />
        </Suspense>
      )}
    </div>
  )
}
