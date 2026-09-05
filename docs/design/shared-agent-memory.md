# Design: Bộ nhớ & điều phối dùng chung cho nhiều agent

> Trạng thái: DRAFT — thiết kế, chưa triển khai.
> Phạm vi: agent 1 (`bomclaw`, :18789) và agent 2 (`bomclaw2`, :18790) chạy song song trên cùng một máy Mac, cùng user, cùng binary.
> Mục tiêu cuối: hai agent **thấy chung một bộ nhớ** và **giao việc được cho nhau**, thay vì hai ốc đảo như hôm nay.

---

## 1. Mục tiêu & phi mục tiêu

**Mục tiêu**

1. Một agent ghi nhận điều gì đó (fact, quyết định, kết quả) → agent kia đọc được, không cần copy tay.
2. Agent A giao việc cho agent B, theo dõi được trạng thái, nhận lại kết quả — có thể kiểm toán.
3. Không mất tính riêng tư của từng agent: hội thoại Telegram riêng vẫn phải tách bạch.
4. Sống được với ràng buộc thật: SQLite, hai **tiến trình OS khác nhau**, không có server điều phối.

**Phi mục tiêu (lần này)**

- Không làm multi-tenant / cho thuê (đã có `docs/design/agents-as-a-service.md`).
- Không làm agent chạy trên nhiều máy — thiết kế này giả định **một máy, một file DB**.
- Không thay memory markdown bằng vector DB. Semantic search là issue riêng (#13).

---

## 2. Hiện trạng (đã verify 2026-09-05)

| Thành phần | Agent 1 | Agent 2 | Vấn đề |
|---|---|---|---|
| DB | `~/.goterm/data/goterm.db` | `~/.goterm2/data/goterm.db` | **Hai file tách rời**, không có đường nào nhìn thấy nhau |
| Memory | `~/goterm-workspace/{MEMORY.md, memory/*.md}` | `~/goterm-workspace-2/...` | Markdown, ngoài DB, cũng tách rời |
| Workspace | `~/goterm-workspace` | `~/goterm-workspace-2` | Tách — đúng, giữ nguyên |
| Kênh agent↔agent | `bomclaw send -addr ws://127.0.0.1:18790/ws "..."` | ngược lại | Có, nhưng **fire-and-forget**: không trạng thái, không kết quả, không retry |
| Mailbox file | `~/goterm-shared/{mailbox,tasks}` (git repo) | dùng chung | **Rỗng — chưa bao giờ dùng.** Chỉ tồn tại trong system prompt |
| Định danh agent | không có | không có | DB key theo `chat_id` Telegram, **không có khái niệm `agent_id`** |
| Backend model | Claude CLI | Claude CLI *(chuyển được sang Codex CLI từ 2026-09-05)* | Đã tách được auth giữa hai agent; phần bộ nhớ/điều phối vẫn chưa chung |

Chi tiết code liên quan:

- `internal/storage/schema.go:5` — `schemaVersion = 4`; bảng: `meta`, `sessions`, `chat_state`, `messages`, `users`, `web_sessions`.
- `internal/storage/db.go:27` — `sql.Open("sqlite", path)`, driver `modernc.org/sqlite`.
- `internal/gateway/methods.go:49` — RPC `send` qua WebSocket; đây là kênh đánh thức agent đã có sẵn.
- `internal/memory/memory.go` — memory là **file markdown trong workspace**, không nằm trong SQLite.

**Kết luận hiện trạng**: không phải "thiếu tính năng chia sẻ" mà là **thiếu hẳn lớp mô hình dữ liệu chung**. Không có `agent_id`, không có task, không có inbox. Cái đã có (`send`, `~/goterm-shared`) là kênh truyền tin trần, không phải giao thức.

---

## 3. Khảo sát thiết kế tham khảo

### 3.1 Blackboard (bảng đen)

Kho dữ liệu trung tâm; agent đăng kết quả trung gian lên đó, các agent khác quan sát và tự quyết định khi nào đóng góp dựa trên năng lực của mình. Đây là mô hình cổ điển (HEARSAY-II) đang được dùng lại cho LLM agent.

- [`claudioed/agent-blackboard`](https://github.com/claudioed/agent-blackboard) — 9 agent chuyên biệt phối hợp qua blackboard dùng chung. Đáng chú ý: **backend mặc định là SQLite** (`BLACKBOARD_PERSISTENCE=sqlite`, `BLACKBOARD_DB_PATH=./data/blackboard.db`) — đúng bối cảnh của ta. Điểm yếu của repo này: dựa vào một "Coordinator" phân việc, **không mô tả cơ chế claim chống trùng** — phần đó ta phải tự thiết kế (§7).
- [Exploring Advanced LLM Multi-Agent Systems Based on Blackboard Architecture (arXiv 2507.01701)](https://arxiv.org/pdf/2507.01701).

**Rút ra**: blackboard hợp với ta vì hai agent **ngang hàng**, không có ai là orchestrator. Nhưng blackboard thuần (ai cũng đọc, ai cũng ghi) sẽ đẻ ra làm-trùng-việc nếu không có claim.

### 3.2 A2A (Agent2Agent) — vòng đời task

[Spec A2A](https://a2a-protocol.org/latest/specification/) (Google, 2025, nay thuộc Linux Foundation) định nghĩa 8 trạng thái task:

`submitted` → `working` → `completed` | `failed` | `canceled` | `rejected`, cộng hai trạng thái gián đoạn `input-required` và `auth-required`.

Đối tượng lõi: **Task** (id, contextId, status, artifacts, history), **Message** (role user/agent, parts), **Part** (text/file/data), **Artifact** (kết quả), **AgentCard** (mô tả năng lực + endpoint).

**Rút ra**: lấy nguyên **bộ tên trạng thái** và cặp `Task`/`Artifact`. Không lấy transport (JSON-RPC over HTTP + SSE) — ta đã có WebSocket nội bộ, thêm HTTP server nữa là thừa. `AgentCard` rút gọn thành bảng `agents`.

### 3.3 Letta / MemGPT — shared memory blocks

[Letta](https://github.com/letta-ai/letta) cho phép **gắn một memory block vào nhiều agent**: mọi agent gắn block đó đều đọc/ghi được, tạo thành workspace chung. Hai chi tiết quan trọng:

- Với hệ nhiều agent, **`memory_insert` (append) an toàn hơn `memory_replace`**: nhiều agent chèn đồng thời không đụng nhau, còn replace thì ghi đè lẫn nhau.
- Letta có sẵn tool giao tiếp: `send_message_to_agent_async` (bắn đi, không chờ) và `send_message_to_agent_and_wait_for_reply` (đồng bộ).

Xem thêm [Shared memory blocks | Letta Docs](https://docs.letta.com/tutorials/shared-memory-blocks/) và issue [GNAP: git-native coordination](https://github.com/letta-ai/letta/issues/3226) — tách bạch **coordination state** (git board dùng chung) khỏi **cognitive state** (memory riêng từng agent). Đây chính là ranh giới ta cần.

**Rút ra**: (1) bộ nhớ chung phải **append-only** ở tầng ghi, việc "gọn hoá" là một bước riêng có chủ đích; (2) tách coordination state khỏi memory.

### 3.4 SQLite làm hàng đợi — claim/lease

- [Building a Durable Message Queue on SQLite for AI Agent Orchestration](https://dev.to/minnzen/building-a-durable-message-queue-on-sqlite-for-ai-agent-orchestration-335m) — schema và câu SQL claim nguyên tử:

  ```sql
  update sqliteq
  set timeout = ?, received = received + 1
  where id = (
    select id from sqliteq
    where queue = ? and ? >= timeout and received < ?
    order by priority desc, created
    limit 1
  )
  returning id, body, received
  ```

  Ý chính: **`timeout` chính là trạng thái**. Quá khứ = rảnh, tương lai = đang bị giữ. Worker chết → lease hết hạn → job tự quay lại hàng đợi. Cột `received` làm **fencing token**: `DELETE ... WHERE id=? AND received=?` — nếu job đã bị giao lại cho worker khác thì `received` lệch, DELETE trúng 0 dòng.

- [`litements/litequeue`](https://github.com/litements/litequeue), [`bewt85/jobqueue`](https://github.com/bewt85/jobqueue) — cùng họ.

**Rút ra**: lấy nguyên mô hình lease + fencing. Đây là phần blackboard thiếu.

### 3.5 LangGraph — checkpointer vs store

[LangGraph persistence](https://docs.langchain.com/oss/python/langgraph/persistence) tách hai thứ: **checkpointer** giữ state của một thread (ngắn hạn), **store** giữ dữ liệu bền vượt qua mọi thread (dài hạn), tổ chức theo namespace/key.

**Rút ra**: ánh xạ thẳng vào ta — `sessions`/`messages` hiện tại **là** checkpointer (per-chat); cái đang thiếu là **store** dùng chung.

### 3.6 Bảng tổng hợp

| Nguồn | Lấy gì | Bỏ gì |
|---|---|---|
| Blackboard | Không gian chung, agent ngang hàng, backend SQLite | Coordinator tập trung |
| A2A | Tên trạng thái task, cặp Task/Artifact, AgentCard | JSON-RPC/HTTP/SSE transport |
| Letta | Shared block, ghi append-only, tách coordination/cognitive | Server Letta, mô hình agent |
| SQLite queue | Claim nguyên tử, lease, fencing token | Dead-letter phức tạp |
| LangGraph | Tách checkpointer (đã có) / store (cần thêm) | Toàn bộ runtime graph |

---

## 4. Nguyên tắc thiết kế

1. **Một file DB, ba vùng.** Riêng tư (per-agent) / điều phối (tasks, inbox) / tri thức chung (shared notes). Ranh giới bằng `agent_id` + `scope`, không bằng file.
2. **Append-only ở tầng ghi.** Không agent nào được `UPDATE` đè một fact của agent khác. Sửa = ghi bản mới + đánh dấu bản cũ `superseded_by`.
3. **Không có orchestrator.** Hai agent ngang hàng. Việc được **claim**, không được **assign cứng** — trừ khi chỉ đích danh.
4. **Mọi giao việc phải có lease.** Agent chết giữa chừng là chuyện bình thường (launchd restart, OAuth hết hạn, TCC dialog). Việc phải tự quay lại hàng đợi.
5. **DB là nguồn sự thật, WebSocket chỉ là chuông báo.** Mất gói tin `send` không được làm mất việc — agent phải tìm lại được việc bằng cách quét DB.
6. **Idempotent.** Lease hết hạn → giao lại → có thể chạy hai lần. Handler phải chịu được.

---

## 5. Kiến trúc đề xuất

```
                 ┌──────────────────────── goterm.db (MỘT file, WAL) ─────────────────────────┐
                 │                                                                            │
  agent 1        │  VÙNG RIÊNG                VÙNG ĐIỀU PHỐI            VÙNG TRI THỨC CHUNG    │        agent 2
 (:18789)        │  sessions                  agents                    shared_notes           │       (:18790)
    │            │  chat_state                tasks                     note_links             │           │
    ├── đọc/ghi ─┤  messages                  task_events                                      ├─ đọc/ghi ─┤
    │  (agent_id)│  (lọc theo agent_id)       agent_messages                                   │ (agent_id)│
    │            │                                                                             │           │
    └────────────┴─────────────────────────────────────────────────────────────────────────────┴───────────┘
           │                                                                                        │
           └───────────── chuông báo: RPC `send` qua ws://127.0.0.1:1879x/ws ────────────────────────┘
                          (chỉ để đánh thức; nội dung thật nằm trong DB)
```

**Vùng riêng** — `sessions`, `chat_state`, `messages` giữ nguyên schema, chỉ thêm cột `agent_id`. Mỗi agent chỉ query dòng của mình. Đây là "checkpointer" theo cách gọi của LangGraph.

**Vùng điều phối** — task + inbox. Đây là chỗ hai agent giao việc. Tương đương "git board" của GNAP.

**Vùng tri thức chung** — `shared_notes`: fact/quyết định/kết quả mà cả hai cùng đọc. Tương đương shared memory block của Letta, và là "store" của LangGraph. *(Chưa làm — P3.)*

**Vùng quan sát** *(thêm khi triển khai, 2026-09-05)* — bảng `runs`: cây trace theo mô hình LangSmith. Một lượt = root run, mỗi lời gọi model = con, mỗi tool model chạy = con của nó. `dotted_order` mang toàn bộ ancestry nên cả cây lấy về đúng thứ tự lồng bằng một truy vấn có index. Ghi bất đồng bộ, được phép rơi khi queue đầy — tracing không bao giờ được là lý do một lượt chậm đi hay hỏng.

---

## 6. Schema đề xuất (schema v5)

```sql
-- ── Định danh agent (AgentCard rút gọn) ────────────────────────────────
CREATE TABLE agents (
  id           TEXT PRIMARY KEY,          -- 'bomclaw', 'bomclaw2'
  display_name TEXT NOT NULL,
  ws_addr      TEXT NOT NULL,             -- ws://127.0.0.1:18789/ws
  workspace    TEXT NOT NULL,
  skills       TEXT DEFAULT '',           -- JSON array, để routing sau này
  last_seen_at TEXT NOT NULL,             -- heartbeat, dùng để phát hiện agent chết
  created_at   TEXT NOT NULL
);

-- ── Giao việc ──────────────────────────────────────────────────────────
CREATE TABLE tasks (
  id            TEXT PRIMARY KEY,         -- 't_' || hex(randomblob(16))
  context_id    TEXT NOT NULL,            -- gom nhiều task cùng một luồng việc (A2A contextId)
  created_by    TEXT NOT NULL REFERENCES agents(id),
  assigned_to   TEXT REFERENCES agents(id),   -- NULL = ai claim cũng được
  claimed_by    TEXT REFERENCES agents(id),   -- ai đang thực sự giữ
  state         TEXT NOT NULL DEFAULT 'submitted',
                -- submitted|working|completed|failed|canceled|rejected|input-required
  priority      INTEGER NOT NULL DEFAULT 0,
  title         TEXT NOT NULL,
  body          TEXT NOT NULL,            -- mô tả việc, markdown
  result        TEXT DEFAULT '',          -- artifact: kết quả trả về
  lease_until   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ')),
                -- quá khứ = rảnh, tương lai = đang bị giữ
  attempts      INTEGER NOT NULL DEFAULT 0,   -- fencing token + đếm lần giao
  max_attempts  INTEGER NOT NULL DEFAULT 3,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
) STRICT;

CREATE INDEX idx_tasks_claimable ON tasks(state, lease_until, priority DESC, created_at);
CREATE INDEX idx_tasks_context   ON tasks(context_id);

-- ── Nhật ký task (audit, không bao giờ sửa) ─────────────────────────────
CREATE TABLE task_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id    TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  agent_id   TEXT NOT NULL,
  from_state TEXT DEFAULT '',
  to_state   TEXT NOT NULL,
  note       TEXT DEFAULT '',
  created_at TEXT NOT NULL
) STRICT;

-- ── Hòm thư (tin nhắn không phải là việc) ───────────────────────────────
CREATE TABLE agent_messages (
  id          TEXT PRIMARY KEY,
  from_agent  TEXT NOT NULL REFERENCES agents(id),
  to_agent    TEXT NOT NULL REFERENCES agents(id),
  task_id     TEXT REFERENCES tasks(id) ON DELETE SET NULL,  -- NULL = chat thuần
  body        TEXT NOT NULL,
  read_at     TEXT DEFAULT '',
  created_at  TEXT NOT NULL
) STRICT;

CREATE INDEX idx_msgs_unread ON agent_messages(to_agent, read_at, created_at);

-- ── Tri thức dùng chung (append-only) ───────────────────────────────────
CREATE TABLE shared_notes (
  id            TEXT PRIMARY KEY,
  author        TEXT NOT NULL REFERENCES agents(id),
  scope         TEXT NOT NULL DEFAULT 'shared',   -- 'shared' | agent_id (riêng)
  kind          TEXT NOT NULL,   -- 'fact' | 'decision' | 'result' | 'gotcha'
  title         TEXT NOT NULL,
  body          TEXT NOT NULL,
  tags          TEXT DEFAULT '',                  -- JSON array
  superseded_by TEXT REFERENCES shared_notes(id), -- sửa = ghi bản mới, trỏ ngược
  created_at    TEXT NOT NULL
) STRICT;

CREATE INDEX idx_notes_live ON shared_notes(scope, superseded_by, created_at DESC);
CREATE VIRTUAL TABLE shared_notes_fts USING fts5(
  title, body, content='shared_notes', content_rowid='rowid'
);
```

Vùng riêng chỉ cần thêm một cột:

```sql
ALTER TABLE sessions   ADD COLUMN agent_id TEXT NOT NULL DEFAULT 'bomclaw';
ALTER TABLE chat_state ADD COLUMN agent_id TEXT NOT NULL DEFAULT 'bomclaw';
```

> Vì sao `superseded_by` chứ không `UPDATE`: hai agent cùng sửa một note sẽ ghi đè lẫn nhau âm thầm. Append + trỏ ngược giữ được cả lịch sử lẫn khả năng đọc "bản mới nhất" (`WHERE superseded_by IS NULL`). Đúng lý do Letta khuyên `memory_insert` thay vì `memory_replace`.

---

## 7. Giao thức giao việc

### 7.1 Claim nguyên tử

Một câu, không có khoảng hở SELECT-rồi-UPDATE:

```sql
UPDATE tasks
SET    state       = 'working',
       claimed_by  = :me,
       lease_until = :now_plus_10min,
       attempts    = attempts + 1,
       updated_at  = :now
WHERE  id = (
         SELECT id FROM tasks
         WHERE  state IN ('submitted', 'working')
           AND  :now >= lease_until              -- rảnh, hoặc lease đã chết
           AND  attempts < max_attempts
           AND  (assigned_to IS NULL OR assigned_to = :me)
         ORDER  BY priority DESC, created_at
         LIMIT  1
       )
RETURNING id, title, body, attempts;
```

Bọc trong `BEGIN IMMEDIATE` để giành write-lock **trước khi** subquery chạy.

### 7.2 Lease và agent chết

- Agent giữ task phải **gia hạn lease** định kỳ (mỗi 2 phút, hạn 10 phút) trong lúc chạy.
- Agent chết (launchd restart, OAuth hết hạn, TCC dialog treo) → không gia hạn → `lease_until` thành quá khứ → agent kia claim lại được.
- `attempts` là fencing token. Khi agent xong việc:

  ```sql
  UPDATE tasks SET state='completed', result=:artifact, updated_at=:now
  WHERE id=:id AND claimed_by=:me AND attempts=:attempts_luc_claim;
  ```

  Trúng 0 dòng ⇒ task đã bị giao cho agent khác trong lúc mình chạy ⇒ **vứt kết quả đi, ghi `task_events`**, đừng ghi đè.

- Quá `max_attempts` → để nguyên ở `submitted` nhưng không claim được nữa (dead-letter mềm), hiện lên dashboard cho người xử lý.

### 7.3 Vòng đời

```
submitted ──claim──> working ──done──> completed
    ▲                   │
    │                   ├──error────> failed
    └──lease hết hạn────┤
                        ├──cần người─> input-required
                        └──từ chối───> rejected
```

`canceled` đến từ người dùng qua dashboard/Telegram, chấp nhận ở mọi trạng thái chưa kết thúc.

---

## 8. Đồng thời trên SQLite đa tiến trình — RỦI RO ĐÃ XÁC MINH

Đây là phần dễ làm hỏng nhất, vì **hai tiến trình OS khác nhau** cùng mở một file. Ba vấn đề, đã kiểm chứng trên chính repo này:

### 8.1 Pragma đang bị áp sai chỗ — phải sửa trước

`internal/storage/db.go:33-42` chạy pragma qua `conn.Exec(...)` trên `*sql.DB`, mà `*sql.DB` là **pool**, không phải một connection:

```go
conn, err := sql.Open("sqlite", path)
for _, pragma := range []string{
    "PRAGMA journal_mode=WAL",
    "PRAGMA busy_timeout=5000",
    "PRAGMA foreign_keys=ON",
    "PRAGMA synchronous=NORMAL",
} {
    if _, err := conn.Exec(pragma); err != nil { ... }
}
```

`journal_mode=WAL` **ghi vào header file DB** nên bền vững — cái này may mắn đúng. Ba pragma còn lại là **per-connection**: chúng chỉ dính vào đúng connection đầu tiên trong pool. Mọi connection mà `database/sql` mở thêm sau đó sẽ có `busy_timeout = 0` → gặp tranh chấp là trả `SQLITE_BUSY` **ngay lập tức**, không chờ. Với một agent một file thì hiếm khi lộ; với hai agent một file thì đây là lỗi thường trực.

**Sửa**: đưa pragma vào DSN để mọi connection đều nhận:

```go
dsn := "file:" + path +
    "?_pragma=busy_timeout(10000)" +
    "&_pragma=journal_mode(WAL)" +
    "&_pragma=foreign_keys(1)" +
    "&_pragma=synchronous(NORMAL)"
conn, _ := sql.Open("sqlite", dsn)
```

### 8.2 Chỉ một writer tại một thời điểm

WAL cho phép nhiều reader song song **với** một writer, nhưng vẫn **chỉ một writer**. Hai gateway ghi cùng lúc là chuyện chắc chắn xảy ra. Biện pháp:

- `busy_timeout` đủ dài (10s) — writer thứ hai chờ thay vì lỗi.
- Transaction ghi phải **ngắn**. Tuyệt đối không giữ transaction mở trong lúc gọi Claude CLI (subprocess sống hàng chục giây).
- Dùng `BEGIN IMMEDIATE` cho mọi transaction có ghi, để giành lock ngay từ đầu thay vì bị nâng cấp lock giữa chừng rồi deadlock.
- Cân nhắc `SetMaxOpenConns(1)` cho **connection ghi** (hiện tại chưa set gì cả) và một pool riêng chỉ đọc.

### 8.3 Rủi ro riêng của driver

Repo dùng `modernc.org/sqlite v1.48.2` (pure-Go). Có báo cáo `SQLITE_BUSY` khi SELECT đồng thời ở WAL mode với driver này, trong khi `mattn/go-sqlite3` không gặp — xem [cznic/sqlite issue #115](https://gitlab.com/cznic/sqlite/-/issues/115).

**Việc phải làm trước khi chốt**: viết một test tải hai tiến trình (không phải hai goroutine) cùng ghi `tasks` và `shared_notes` trong 60 giây, đếm `SQLITE_BUSY`. Nếu driver không chịu nổi, hai lối thoát:
- (a) đổi sang `mattn/go-sqlite3` (kéo theo cgo — cân nhắc với việc code-sign hiện tại);
- (b) mọi ghi vào vùng chung đi qua **một** tiến trình (agent 1 làm chủ file, agent 2 ghi qua RPC). Mất tính đối xứng nhưng loại bỏ hẳn tranh chấp ghi.

> Đây là câu hỏi mở lớn nhất của thiết kế. Không nên viết code schema trước khi đo xong.

---

## 9. Đánh thức agent

Nguyên tắc §4.5: DB là sự thật, WebSocket chỉ là chuông.

1. Agent A tạo task → commit vào DB.
2. Agent A gọi RPC `send` tới `ws://127.0.0.1:18790/ws` với nội dung ngắn: *"Có task mới `t_abc`, kiểm tra hàng đợi."* (`internal/gateway/methods.go:49`, đã có sẵn).
3. Agent B nhận, quét hàng đợi, claim.
4. **Nếu bước 2 thất bại** (agent B đang tắt, mất gói) — không sao: agent B có **vòng quét định kỳ** (60s) tự tìm task chưa claim.

Vòng quét là thứ làm hệ thống bền, `send` chỉ làm nó nhanh. Không được thiết kế ngược lại.

Heartbeat: mỗi agent cập nhật `agents.last_seen_at` mỗi 30s. Agent quá 5 phút không heartbeat được coi là chết — dashboard hiện đỏ, và task `assigned_to` đích danh nó được nới thành `NULL` để agent kia gánh.

---

## 10. Tool cho agent

Agent chạy qua Claude CLI với `bypassPermissions`, nên cách rẻ nhất và không cần đụng vào tool loop: **thêm subcommand cho `bomclaw`**, rồi dạy agent gọi chúng trong system prompt (giống cách `bomclaw send` đang được dạy hôm nay).

```
bomclaw task new    --title T --body B [--to bomclaw2] [--priority N]  → in ra task id
bomclaw task claim  [--id ID]                                          → claim, in ra việc
bomclaw task done   --id ID --result R
bomclaw task fail   --id ID --reason R
bomclaw task list   [--state working] [--mine]
bomclaw inbox       [--unread]
bomclaw note add    --kind fact --title T --body B [--tags a,b]
bomclaw note search "query"                                            → FTS5
```

Ưu điểm: không phải sửa `internal/agent` tool loop, dùng lại được cho cả provider Claude lẫn Codex sau này, và người dùng cũng gõ tay được để debug.

---

## 11. Bộ nhớ markdown dùng chung

Hôm nay memory là file markdown trong workspace riêng (`internal/memory/memory.go`), không nằm trong DB. Không nên bê hết vào SQLite — markdown đang chạy tốt và Claude đọc file dễ hơn đọc DB.

Đề xuất **hai tầng**:

| Tầng | Nơi ở | Ai ghi | Dùng cho |
|---|---|---|---|
| Riêng | `~/goterm-workspace{,-2}/MEMORY.md` + `memory/*.md` | agent sở hữu | Thói quen, ngữ cảnh hội thoại riêng — giữ nguyên như hiện tại |
| Chung | bảng `shared_notes` + `~/goterm-shared/NOTES.md` (render ra) | cả hai, append-only | Fact, quyết định, kết quả, cạm bẫy đã gặp |

`~/goterm-shared/NOTES.md` là **bản render một chiều** từ `shared_notes` (chạy sau mỗi lần ghi), để agent đọc bằng file tool quen thuộc mà không phải query SQL. Nguồn sự thật vẫn là DB — không bao giờ sửa tay file render.

Việc "gọn hoá" (dồn nhiều note vụn thành một note tốt, đánh `superseded_by`) là một **task định kỳ** trong chính hệ thống task ở trên, không phải một hàm chạy ngầm.

---

## 12. Lộ trình

> **Cập nhật 2026-09-05** — P0, P2, P3 và P4 đã làm (PR #66–#70). **P1 (gộp bảng session vào một DB) vẫn chưa** và có thể không cần nữa: vùng điều phối đã tách sang file riêng `~/.goterm-shared/data/coord.db` và giải quyết được toàn bộ nhu cầu "hai agent thấy chung", mà không phải đụng vào bảng session vốn gánh lượng ghi lớn. Nếu chốt bỏ P1 thì cũng bỏ luôn việc đổi tên `claude_session_id` → `provider_session_id` (§13 Q1) vì lý do duy nhất để đổi là gộp bảng.
>
> Chạy thật đã lộ ra hai lỗi mà bản thiết kế không lường: gateway không export `BOMCLAW_AGENT_ID` nên CLI ghi nhầm danh tính agent (fencing token chặn được hỏng dữ liệu — xem §7.2), và binary `bomclaw` không nằm trong PATH của launchd nên agent gọi lệnh không được. Cả hai đã sửa (PR #70) và ghi vào NOTES.md.

| Giai đoạn | Nội dung | Điều kiện hoàn thành |
|---|---|---|
| ~~**P0 — Đo trước**~~ ✅ | Test tải hai tiến trình ghi đồng thời; sửa pragma DSN (§8.1) | **Xong.** `TestTwoProcessesShareTheDatabase` re-exec test binary thành tiến trình OS thứ hai cùng ghi một file — **pass**, không có `SQLITE_BUSY`. `modernc.org/sqlite` chịu được, **không cần đổi driver**. Pragma đã chuyển vào DSN |
| **P1 — Nền** | Gộp về một file DB `~/.goterm/data/goterm.db`; schema v5; thêm `agent_id` vào `sessions`/`chat_state`; migrate DB agent 2 vào | Hai agent chạy chung file, hội thoại vẫn tách bạch, không mất lịch sử |
| ~~**P2 — Giao việc**~~ ✅ | Bảng `tasks`/`task_events`/`agent_messages`; claim+lease+fencing; `bomclaw task/inbox/msg`; heartbeat; vòng quét 60s + chuông `tasks.poke` | **Xong (PR #69).** `tasks.auto_claim` mặc định TẮT — task được claim chạy với đúng quyền truy cập máy của backend chat, không ai giám sát |
| ~~**P3 — Tri thức chung**~~ ✅ | `shared_notes` + FTS5; `bomclaw note *`; render `NOTES.md`; đưa vào system prompt | **Xong (PR #69).** Append-only, sửa bằng `--supersedes`. Pane Notes trên dashboard |
| ~~**P4 — Nhìn thấy**~~ ✅ | Tab Admin: Overview / Traces (waterfall) / Tasks / Messages; trạng thái agent theo heartbeat | **Xong.** Thêm cả tầng trace kiểu LangSmith mà bản thiết kế đầu chưa có |

P1 phụ thuộc P0. Không đảo thứ tự.

---

## 13. Rủi ro & câu hỏi mở

| Rủi ro | Mức | Xử lý |
|---|---|---|
| `modernc.org/sqlite` không chịu nổi hai tiến trình ghi | **Cao** | P0 phải đo. Lối thoát: đổi driver, hoặc một-writer qua RPC (§8.3) |
| Hai agent giao việc qua lại vô hạn (ping-pong) | **Cao** | Giới hạn độ sâu: task có `context_id`, chặn khi một `context_id` sinh quá N task; system prompt cấm tự khởi tạo vòng lặp (đã có sẵn dòng này) |
| Gộp DB làm lộ hội thoại giữa hai agent | Trung bình | Mọi truy vấn vùng riêng bắt buộc lọc `agent_id`; viết test khẳng định agent 2 không đọc được `messages` của agent 1 |
| Migrate làm mất lịch sử chat agent 2 | Trung bình | Migrate là copy có kiểm chứng số dòng, giữ file cũ; chỉ xoá sau khi chạy ổn 1 tuần |
| Task chạy hai lần do lease hết hạn | Thấp | Fencing `attempts` (§7.2) + yêu cầu handler idempotent |
| `shared_notes` phình to, nhiễu | Thấp | Task gọn hoá định kỳ; `superseded_by` giữ bản mới nhất |

**Câu hỏi mở**

1. ~~**Agent 2 chạy Claude hay Codex?**~~ **Đã chốt (2026-09-05)**: backend chọn bằng `provider: claude|codex` trong config; schema v5 thêm cột `sessions.provider` ghi nhận CLI nào tạo ra session id, nên đổi backend sẽ mở thread mới thay vì resume nhầm. Cột `claude_session_id` **chưa** đổi tên thành `provider_session_id` — nó xuất hiện trong JSON của gateway RPC và trong `dashboard/src/stores/store.ts`, nên việc đổi tên gộp vào đợt migrate P1 cho khỏi phá wire format hai lần. Ràng buộc kèm theo: agent chạy Codex phải trỏ vào model có `api: codex-cli` (hiện chỉ `gpt-6-astra` được tài khoản ChatGPT chấp nhận).
2. **Có cho agent tự tạo task cho chính mình không?** Tiện cho việc dài hơi, nhưng là mầm của vòng lặp vô hạn. Nghiêng về: có, nhưng chặn độ sâu theo `context_id`.
3. **Ai sở hữu file DB khi cả hai cùng ghi?** Nếu P0 cho kết quả xấu, phải chọn agent 1 làm chủ — mất tính đối xứng, cần quyết định sớm vì ảnh hưởng toàn bộ tool surface §10.
4. **`~/goterm-shared` git repo giữ hay bỏ?** Hiện rỗng. Nếu `shared_notes` thay được vai trò của nó thì bỏ để đỡ hai nguồn sự thật; nếu giữ thì chỉ dùng để chứa **code/patch** trao đổi, không chứa tri thức.

---

## Tham khảo

- [Agent2Agent (A2A) Protocol Specification](https://a2a-protocol.org/latest/specification/)
- [claudioed/agent-blackboard](https://github.com/claudioed/agent-blackboard) — blackboard đa agent, backend SQLite
- [Exploring Advanced LLM Multi-Agent Systems Based on Blackboard Architecture (arXiv 2507.01701)](https://arxiv.org/pdf/2507.01701)
- [letta-ai/letta](https://github.com/letta-ai/letta) · [Shared memory blocks](https://docs.letta.com/tutorials/shared-memory-blocks/) · [GNAP: git-native coordination (issue #3226)](https://github.com/letta-ai/letta/issues/3226)
- [Building a Durable Message Queue on SQLite for AI Agent Orchestration](https://dev.to/minnzen/building-a-durable-message-queue-on-sqlite-for-ai-agent-orchestration-335m)
- [litements/litequeue](https://github.com/litements/litequeue) · [bewt85/jobqueue](https://github.com/bewt85/jobqueue)
- [LangGraph Persistence — checkpointer vs store](https://docs.langchain.com/oss/python/langgraph/persistence)
- [SQLite: Write-Ahead Logging](https://www.sqlite.org/wal.html) · [SQLite concurrent writes and "database is locked" errors](https://tenthousandmeters.com/blog/sqlite-concurrent-writes-and-database-is-locked-errors/)
- [cznic/sqlite issue #115 — SQLITE_BUSY on concurrent SELECT in WAL](https://gitlab.com/cznic/sqlite/-/issues/115)
- [sqliteai/sqlite-memory](https://github.com/sqliteai/sqlite-memory) · [akitaonrails/ai-memory](https://github.com/akitaonrails/ai-memory) — memory chia sẻ giữa các CLI agent
