// Mirrors internal/coord — keep field names in sync with the Go json tags.

export type RunType = 'chain' | 'llm' | 'tool' | 'memory' | 'task'
export type RunStatus = 'pending' | 'success' | 'error'

export interface Run {
  id: string
  trace_id: string
  parent_run_id?: string
  dotted_order: string
  agent_id: string
  session_id?: string
  chat_id?: number
  name: string
  run_type: RunType
  status: RunStatus
  started_at: string
  ended_at?: string
  duration_ms: number
  inputs?: string
  outputs?: string
  error?: string
  input_tokens: number
  output_tokens: number
  model?: string
  provider?: string
  tags?: string
  depth: number
}

export interface TraceSummary extends Run {
  span_count: number
  error_count: number
  tool_count: number
  total_tokens: number
}

export interface Agent {
  id: string
  display_name: string
  provider: string
  model: string
  ws_addr: string
  workspace: string
  started_at: string
  last_seen_at: string
  online: boolean
}

export interface Stats {
  task_counts: Record<string, number>
  open_tasks: number
  unread_messages: number
  traces_24h: number
  errors_24h: number
  tokens_24h: number
  tool_calls_24h: number
  avg_duration_ms: number
}

export interface Overview {
  agent_id: string
  agents: Agent[]
  stats: Stats
  db_path: string
}

export type TaskState =
  | 'submitted' | 'working' | 'completed'
  | 'failed' | 'canceled' | 'rejected' | 'input-required' | 'blocked'

// How ONE run of a task ended — a different vocabulary from TaskState on
// purpose. A task is where the work stands; a run is one bounded attempt at it.
export type RunLiveness =
  | 'running' | 'completed' | 'advanced' | 'plan_only' | 'empty'
  | 'blocked' | 'failed' | 'timed_out' | 'canceled'

export interface TaskRun {
  id: string
  task_id: string
  agent_id: string
  attempt: number
  liveness: RunLiveness
  trace_id?: string
  started_at: string
  ended_at?: string
  note?: string
}

export interface Task {
  id: string
  context_id: string
  created_by: string
  assigned_to?: string
  claimed_by?: string
  state: TaskState
  priority: number
  title: string
  body?: string
  result?: string
  trace_id?: string
  lease_until: string
  attempts: number
  max_attempts: number
  depth: number
  created_at: string
  updated_at: string
  // A task is many runs; these survive between them.
  parent_id?: string
  kind: 'manual' | 'scheduled' | 'heartbeat' | 'sub'
  schedule_id?: string
  checkpoint?: string
  session_ref?: string
  continuations: number
  max_continuations: number
  blocked_on?: 'children' | 'human' | ''
  fail_reason?: 'exhausted' | 'continuations-exhausted' | 'empty-exhausted' | ''
}

export interface TaskEvent {
  id: number
  task_id: string
  agent_id: string
  from_state?: string
  to_state: string
  note?: string
  created_at: string
}

export interface TaskDetail {
  task: Task
  events: TaskEvent[]
  runs: TaskRun[]
}

export interface Message {
  id: string
  from_agent: string
  to_agent: string
  task_id?: string
  body: string
  read_at?: string
  created_at: string
}

export interface Note {
  id: string
  author: string
  scope: string
  kind: string
  title: string
  body?: string
  tags?: string
  superseded_by?: string
  created_at: string
}

// --- schedules (design §5.5/§5.6) -------------------------------------------
// A schedule decides WHEN; a schedule run records WHAT happened. `agent`
// payloads materialise a task; `command` payloads run a shell command inside
// the gateway; `heartbeat` is the gateway's own row for one agent.

export type ScheduleKind = 'at' | 'every' | 'cron'
export type PayloadKind = 'agent' | 'command' | 'heartbeat'
export type ScheduleRunStatus = 'pending' | 'ok' | 'failed' | 'skipped'

export interface AgentPayload { title: string; body?: string; to?: string; quiet?: boolean }
export interface CommandPayload { cmd: string; cwd?: string; timeout_s?: number }
export interface HeartbeatPayload { active_hours?: string }

export interface Schedule {
  id: string
  name: string
  created_by: string
  owner_agent?: string
  kind: ScheduleKind
  spec: string
  tz: string
  payload_kind: PayloadKind
  payload: AgentPayload | CommandPayload | HeartbeatPayload
  enabled: boolean
  system: boolean
  skip_missed: boolean
  next_run_at: string
  last_run_at?: string
  last_status?: ScheduleRunStatus | ''
  consecutive_failures: number
  created_at: string
  updated_at: string
  when: string // "every 10m", "cron 0 8 * * 1-5 (Asia/Ho_Chi_Minh)" — rendered by the gateway
}

export interface ScheduleRun {
  id: string
  schedule_id: string
  task_id?: string
  started_at: string
  ended_at?: string
  status: ScheduleRunStatus
  exit_code: number
  output?: string
}

export interface ScheduleView {
  schedule: Schedule
  runs: ScheduleRun[]
  when: string
}
