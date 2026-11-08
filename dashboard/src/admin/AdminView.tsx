import { useEffect, useState } from 'react'
import type { Overview as OverviewData } from './types'
import Overview from './Overview'
import TraceExplorer from './TraceExplorer'
import TaskBoard from './TaskBoard'
import ChannelView from './ChannelView'
import NotesPane from './NotesPane'
import SettingsPane from './SettingsPane'
import SchedulesPane from './SchedulesPane'
import PluginsPane from './PluginsPane'
import AccountsPane from './AccountsPane'
import EconomicsPane from './EconomicsPane'
import InfraPane from './InfraPane'
import ConnectionsPane from './ConnectionsPane'
import StatusBar from '../components/StatusBar'
import { type AdminPane as Pane } from '../lib/route'
import type { ReactNode } from 'react'

type Call = (method: string, params?: any) => Promise<any>

// Keyed by Pane rather than listed, so a pane added to the route table cannot
// reach the address bar without also getting an entry to click.
const LABELS: Record<Pane, string> = {
  messages: 'Messages',
  tasks: 'Tasks',
  schedules: 'Schedules',
  overview: 'Status',
  traces: 'Traces',
  economics: 'Economics',
  notes: 'Notes',
  plugins: 'Plugins',
  accounts: 'Accounts',
  connections: 'Kết nối',
  infra: 'Infra',
  settings: 'Settings',
}

// The sidebar's System section, in the order work reaches for them: what is
// being done, what will run, how the machine is, then configuration.
const TOOL_ORDER: Pane[] = ['tasks', 'schedules', 'overview', 'traces', 'economics', 'notes', 'plugins', 'accounts', 'connections', 'infra', 'settings']

// The whole dashboard is one screen: rooms, threads and messages, with every
// other page a tool in the same sidebar. It used to be four apps in a top bar
// (Messages, Chat, Status, Admin) and a second bar of panes under Admin, while
// the work itself had moved into channel > thread > message.
export default function AdminView({ call, pane, onPane, taskId, onTaskId, channelId, onChannelId, brand, footer }: {
  call: Call; pane: Pane; onPane: (p: Pane) => void
  /** taskId is the task whose detail is open, owned by the address bar. */
  taskId: string; onTaskId: (id: string) => void
  /** channelId is the room the Messages pane is showing, owned by the address
   *  bar the same way. */
  channelId: string; onChannelId: (id: string) => void
  brand?: ReactNode; footer?: ReactNode
}) {
  const [data, setData] = useState<OverviewData | null>(null)
  const [err, setErr] = useState<string | null>(null)
  // Work and the conversation it came out of live in two panes. A thread the
  // room should open is carried here and cleared once handed over, so returning
  // to a pane later does not reopen what you already closed. The task the board
  // should open is not: that one is the address bar's, so it can be linked.
  const [openThread, setOpenThread] = useState<string>('')
  // And a project whose folder the board wants opened: "where did this task's
  // work land" is one click from the task, not a hunt through the rooms.

  // The overview drives the agent list every other pane filters by, so it is
  // refreshed regardless of which pane is showing.
  useEffect(() => {
    let cancelled = false
    const load = async () => {
      try {
        const d = await call('admin.overview')
        if (!cancelled) { setData(d); setErr(null) }
      } catch (e: any) {
        if (!cancelled) setErr(String(e?.message ?? e))
      }
    }
    load()
    const id = setInterval(load, 8000)
    return () => { cancelled = true; clearInterval(id) }
  }, [call])

  if (err) {
    return (
      <div className="p-6">
        <div className="rounded-lg bg-red-500/10 ring-1 ring-red-500/30 p-4">
          <div className="text-sm text-red-300 font-medium">Coordination unavailable</div>
          <div className="mt-1 text-xs text-red-300/80">{err}</div>
          <div className="mt-3 text-xs text-gray-400">
            Traces, tasks and inter-agent messages live in the shared database. Set
            <code className="mx-1 font-mono text-gray-300">coord.enabled: true</code>
            in the gateway config and restart it.
          </div>
        </div>
      </div>
    )
  }

  const agentIDs = data?.agents.map(a => a.id) ?? []
  // Each agent answers on its own Telegram bot, so a direct room links to a
  // different chat per agent.
  const bots: Record<string, string> = {}
  for (const a of data?.agents ?? []) {
    if (a.telegram_bot) bots[a.id] = a.telegram_bot
  }
  const selfID = data?.agent_id ?? ''

  const tools = TOOL_ORDER.map(key => ({
    key, label: LABELS[key],
    badge: key === 'tasks' ? data?.stats.open_tasks || undefined : undefined,
  }))

  const toolPane = (
    <div className="h-full min-h-0">
      {pane === 'overview' && (
        <div className="h-full overflow-y-auto">
          <Overview data={data} />
          <StatusBar />
        </div>
      )}
      {pane === 'economics' && <EconomicsPane call={call} agents={agentIDs} />}
      {pane === 'traces' && <TraceExplorer call={call} agents={agentIDs} />}
      {pane === 'tasks' && (
        <TaskBoard
          call={call} agents={agentIDs}
          openTaskID={taskId} onOpenTask={onTaskId}
          onOpenThread={rootID => { setOpenThread(rootID); onPane('messages') }}
          onOpenChannel={id => { onChannelId(id); onPane('messages') }}
          // The project's editor page, with the running project beside it.
          onOpenFiles={channelID => window.open(`/files/${encodeURIComponent(channelID)}?preview=1`, '_blank', 'noopener')}
        />
      )}
      {pane === 'schedules' && <SchedulesPane call={call} agents={agentIDs} />}
      {pane === 'notes' && <NotesPane call={call} />}
      {pane === 'plugins' && <PluginsPane call={call} />}
      {pane === 'accounts' && <AccountsPane call={call} />}
      {pane === 'connections' && <ConnectionsPane call={call} />}
      {pane === 'infra' && <InfraPane call={call} />}
      {pane === 'settings' && <SettingsPane call={call} />}
    </div>
  )

  return (
    <ChannelView
      call={call} agents={agentIDs} selfID={selfID} bots={bots}
      channelID={channelId} onChannel={onChannelId}
      openThreadID={openThread} onOpenedThread={() => setOpenThread('')}
      onOpenTask={taskID => { onTaskId(taskID); onPane('tasks') }}
      tools={tools}
      tool={pane === 'messages' ? '' : pane}
      onTool={key => onPane((key || 'messages') as Pane)}
      toolPane={toolPane}
      brand={brand} footer={footer}
    />
  )
}
