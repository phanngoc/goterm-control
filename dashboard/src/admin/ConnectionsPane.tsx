import { useCallback, useEffect, useState } from 'react'
import { ago } from './format'

type Call = (method: string, params?: any) => Promise<any>

// Where the agents deploy to, and the keys to get there. One card per service:
// what it is for, each value with where it came from (stored here, or the
// gateway's .env), a form to set them, and a check that asks the service
// itself whether the key works. Values never come back to the page — a stored
// secret shows as a mask, and leaving a field empty on save keeps it.
//
// The same store an agent writes with `bomclaw secrets set` when you hand it a
// token in chat, so either way of entering one is the same thing.

interface FieldView {
  env: string; label: string; secret: boolean; required: boolean
  placeholder?: string; help?: string
  set: boolean; source?: 'store' | 'env'; masked?: string; updated?: string; by?: string
}
interface ServiceView {
  id: string; title: string; purpose: string; docs?: string; planned?: boolean
  fields: FieldView[]; connected: boolean
}

const input = 'w-full px-2 py-1.5 text-sm bg-gray-950 rounded ring-1 ring-gray-800 text-gray-200 outline-none focus:ring-gray-600 font-mono'
const btn = 'px-2.5 py-1 text-xs rounded ring-1 transition-colors disabled:opacity-40'

export default function ConnectionsPane({ call }: { call: Call }) {
  const [list, setList] = useState<ServiceView[] | null>(null)
  const [err, setErr] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      setList(await call('secrets.list'))
      setErr(null)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call])
  useEffect(() => { void load() }, [load])

  return (
    <div className="h-full overflow-y-auto p-4 space-y-4">
      <div>
        <h2 className="text-sm font-medium text-gray-200">Kết nối hạ tầng</h2>
        <p className="mt-1 text-xs text-gray-500 max-w-3xl">
          Khóa để agent deploy PROD (nút <span className="text-gray-300">DEV · PROD</span> trong mỗi phòng dự án).
          Nhập ở đây, hoặc dán token cho agent trong chat — agent lưu bằng{' '}
          <code className="font-mono text-gray-400">bomclaw secrets set</code>, cùng một kho.
          Giá trị lưu ở đây được ưu tiên hơn <code className="font-mono text-gray-400">.env</code>; không bao giờ hiện lại đầy đủ.
        </p>
      </div>
      {err && <div className="text-xs text-red-300">{err}</div>}
      <div className="grid grid-cols-1 xl:grid-cols-2 gap-3">
        {list?.map(s => <ServiceCard key={s.id} s={s} call={call} onChanged={load} />)}
      </div>
    </div>
  )
}

function ServiceCard({ s, call, onChanged }: { s: ServiceView; call: Call; onChanged: () => void }) {
  const [values, setValues] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [check, setCheck] = useState<{ ok: boolean; detail: string } | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const dirty = Object.values(values).some(v => v.trim() !== '')
  const stored = s.fields.some(f => f.source === 'store')

  const verify = async () => {
    setBusy(true)
    try {
      setCheck(await call('secrets.verify', { service: s.id }))
    } catch (e: any) {
      setCheck({ ok: false, detail: String(e?.message ?? e) })
    } finally {
      setBusy(false)
    }
  }

  const save = async () => {
    setBusy(true)
    try {
      await call('secrets.set', { service: s.id, values })
      setValues({})
      setErr(null)
      onChanged()
      setBusy(false)
      await verify() // saving is asking "does this work?" — answer it
    } catch (e: any) {
      setErr(String(e?.message ?? e))
      setBusy(false)
    }
  }

  const remove = async () => {
    if (!confirm(`Xóa các khóa ${s.title} đã lưu ở đây? (Giá trị trong .env không bị đụng tới.)`)) return
    try {
      await call('secrets.unset', { service: s.id })
      setCheck(null)
      onChanged()
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }

  return (
    <section className="rounded-lg ring-1 ring-gray-800 bg-gray-900/40 p-3 space-y-3">
      <div className="flex items-start gap-2">
        <span className={`mt-1 w-2 h-2 rounded-full shrink-0 ${s.connected ? 'bg-emerald-400' : 'bg-gray-600'}`} />
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-sm text-gray-100 font-medium">{s.title}</span>
            {s.planned && <span className="text-[10px] px-1.5 rounded bg-gray-800 text-gray-400">sắp dùng</span>}
            <span className={`text-[11px] ${s.connected ? 'text-emerald-300' : 'text-gray-500'}`}>
              {s.connected ? 'đã kết nối' : 'chưa kết nối'}
            </span>
          </div>
          <div className="text-xs text-gray-500">{s.purpose}</div>
        </div>
        {s.docs && (
          <a href={s.docs} target="_blank" rel="noreferrer" className="ml-auto shrink-0 text-[11px] text-sky-400 hover:underline">
            tạo khóa ↗
          </a>
        )}
      </div>

      <div className="space-y-2">
        {s.fields.map(f => (
          <label key={f.env} className="block">
            <div className="flex items-baseline gap-2 text-[11px]">
              <span className="text-gray-300">{f.label}{f.required && <span className="text-amber-400"> *</span>}</span>
              <span className="font-mono text-gray-600">{f.env}</span>
              {f.set && (
                <span className="ml-auto text-gray-500">
                  {f.source === 'env' ? 'từ .env' : `đã lưu${f.by ? ` · ${f.by}` : ''}${f.updated ? ` · ${ago(f.updated)}` : ''}`}
                </span>
              )}
            </div>
            <input
              type={f.secret ? 'password' : 'text'}
              autoComplete="off"
              spellCheck={false}
              value={values[f.env] ?? ''}
              onChange={e => setValues(v => ({ ...v, [f.env]: e.target.value }))}
              placeholder={f.set ? `${f.masked} — để trống để giữ nguyên` : (f.placeholder || '')}
              className={`${input} mt-0.5`}
            />
            {f.help && <div className="mt-0.5 text-[11px] text-gray-600">{f.help}</div>}
          </label>
        ))}
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <button disabled={busy || !dirty} onClick={save}
          className={`${btn} ring-sky-500/40 bg-sky-500/15 text-sky-200 hover:bg-sky-500/25`}>
          Lưu & kiểm tra
        </button>
        <button disabled={busy || !s.connected} onClick={verify}
          className={`${btn} ring-gray-700 text-gray-300 hover:bg-gray-800`}>
          {busy ? 'Đang kiểm tra…' : 'Kiểm tra'}
        </button>
        {stored && (
          <button disabled={busy} onClick={remove} className={`${btn} ring-transparent text-gray-500 hover:text-red-300`}>
            Xóa
          </button>
        )}
      </div>
      {check && (
        <div className={`text-xs rounded px-2 py-1.5 ${check.ok ? 'bg-emerald-500/10 text-emerald-300' : 'bg-red-500/10 text-red-300'}`}>
          {check.ok ? '✓ ' : '✗ '}{check.detail}
        </div>
      )}
      {err && <div className="text-xs text-red-300">{err}</div>}
      <div className="text-[11px] text-gray-600">
        Qua chat: dán khóa cho agent, nó chạy{' '}
        <code className="font-mono text-gray-500">bomclaw secrets set {s.id} {s.fields.filter(f => f.required).map(f => `${f.env}=…`).join(' ') || `${s.fields[0].env}=…`}</code>
      </div>
    </section>
  )
}
