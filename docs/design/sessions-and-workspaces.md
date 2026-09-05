# Design: Hợp nhất session, và không gian làm việc theo session

> Trạng thái: DRAFT — thiết kế, chưa triển khai.
> Phạm vi: hai kênh vào của agent (Telegram và dashboard web), và chỗ đứng của **session** trong quan hệ với **task** (`docs/design/shared-agent-memory.md`) và **trace**.
> Tham chiếu: [paperclipai/paperclip](https://github.com/paperclipai/paperclip) — đã clone và đọc để lấy mô hình heartbeat, atomic checkout, và execution workspace.

---

## 1. Mục tiêu & phi mục tiêu

**Mục tiêu**

1. **Một hội thoại duy nhất.** Nhắn trên Telegram rồi mở web (hoặc ngược lại) phải thấy cùng lịch sử, cùng ngữ cảnh, cùng session.
2. **Mỗi session có thư mục riêng** để chứa sản phẩm của nó — file, báo cáo, patch, screenshot — thay vì rải vào workspace chung.
3. **Nối được ba thứ đang rời nhau**: message (theo session) ↔ task (hàng đợi chung) ↔ run/trace (đã có).
4. Agent tự thức dậy đúng lúc, không chỉ theo đồng hồ.

**Phi mục tiêu (lần này)**

- Không làm org chart / budget / approval như Paperclip. Ta có hai agent, không phải một công ty.
- Không đổi `~/goterm-workspace` thành thư mục dùng một lần. Đó là nơi code và project sống lâu dài.
- Không đồng bộ với hệ thống task bên ngoài (Linear/Jira).

---

## 2. Hiện trạng (đã verify 2026-09-05)

Đây không phải "thiếu tính năng đồng bộ". Web và Telegram hiện là **hai hệ thống khác nhau chạy song song**, chỉ tình cờ dùng chung một binary.

| | Telegram | Dashboard web |
|---|---|---|
| Khoá hội thoại | `chat_id` thật của Telegram | hằng số `dashboardChatID = 1` |
| Id session | `chat_<tgid>_<seq>` (có seq, rotate hằng ngày) | `chat_1`, **không bao giờ rotate** |
| Đường thực thi | `bot.Handler.runClaude` → `chat.Client.SendMessage` | `gateway.NewStreamSendHandler` → `agent.RunAgent` |
| Vòng lặp tool | **CLI tự chạy** (claude/codex sở hữu tool loop) | **của ta** (`internal/agent` + `internal/tools`) |
| Lịch sử nạp lại | CLI tự resume theo session id của nó | đọc file transcript, **cắt còn 40 message** |
| Bảng `messages` | có ghi | **không ghi** |
| Memory dài hạn | có inject (`internal/memory`) | **không có** |
| Auto-continue | có | không |
| Lưu tạm khi đang chạy | có | có |

Hệ quả cụ thể, kiểm chứng được trên máy này:

```
$ sqlite3 ~/.goterm/data/goterm.db "SELECT * FROM chat_state;"
1|chat_1|1                        ← web, một session duy nhất, seq đứng yên
5531591024|chat_5531591024_27|28  ← telegram, 27 session, seq 28
```

**Kết luận nền tảng**: gộp `chat_id` lại là phần dễ. Phần thật là **hai đường thực thi khác nhau** — cùng một câu hỏi gửi từ hai kênh sẽ chạy bằng hai bộ máy khác nhau, có/không memory, có/không auto-continue, và tool loop nằm ở hai nơi. Đồng bộ lịch sử mà không hợp nhất đường thực thi chỉ tạo ra một hội thoại nhìn thì liền mạch nhưng hành xử bất nhất.

---

## 3. Khảo sát Paperclip

Clone `paperclipai/paperclip` (190MB, monorepo TS). Những gì đáng lấy:

### 3.1 Heartbeat — agent không chạy liên tục

> *"Agents don't run continuously. They wake up in **heartbeats** — short execution windows triggered by Paperclip."*

Bốn nguồn đánh thức (`docs/agents-runtime.md` §2):

| Nguồn | Ý nghĩa |
|---|---|
| `timer` | hẹn giờ định kỳ |
| `assignment` | có việc được giao/checkout cho agent đó |
| `on_demand` | người bấm nút, hoặc gọi API |
| `automation` | hệ thống tự kích |

Và một chi tiết quan trọng: **"If an agent is already running, new wakeups are merged (coalesced) instead of launching duplicate runs."**

Cấu hình heartbeat theo từng agent: `enabled`, `intervalSec`, `wakeOnAssignment`, `wakeOnOnDemand`, `wakeOnAutomation`.

**So với ta**: `internal/taskrunner` hiện chỉ có `timer` + một chuông `tasks.poke` (tương đương `on_demand`). Thiếu `assignment` như một nguồn riêng, và thiếu khái niệm "agent đang bận" — hiện ta rút cạn hàng đợi tuần tự.

### 3.2 Vòng đời task và atomic checkout

```
backlog → todo → in_progress → in_review → done
                      ↓
                   blocked
```

Terminal: `done`, `cancelled`.

> *"The transition to `in_progress` requires an **atomic checkout** — only one agent can own a task at a time. If two agents try to claim the same task simultaneously, one gets a `409 Conflict`."*

**So với ta**: cơ chế giống hệt (một câu `UPDATE ... RETURNING`), nhưng **thiếu hai trạng thái**:
- `in_review` — agent làm xong nhưng cần người/agent khác duyệt trước khi đóng. Ta hiện nhảy thẳng `working → completed`, không có chỗ cho "xong rồi nhưng chờ xác nhận".
- `blocked` — đang làm thì vướng, không phải fail. Ta hiện chỉ có `failed`, làm mất phân biệt giữa "thử rồi không được" và "đang chờ thứ khác".

### 3.3 Execution workspace — tách khỏi task

`docs/guides/board-operator/execution-workspaces-and-runtime-services.md`:

- Workspace là **khái niệm riêng**, không phải thuộc tính của issue. Một issue **có thể** tạo workspace cô lập mới, **có thể** dùng lại cái có sẵn, và **nhiều issue có thể cùng dùng một workspace** khi muốn làm trên cùng một branch.
- **"Execution workspaces are durable until a human closes them."** Không tự xoá khi run kết thúc.
- Đóng workspace mới dừng service và dọn artifact — *"when allowed"*. Workspace trỏ vào checkout chính được xử lý **thận trọng hơn** workspace cô lập dùng một lần.
- Heartbeat resolve workspace cho run (vừa là vị trí code, vừa là **tính liên tục của session**), rồi lưu metadata: path, ref, provisioning.

**Đây là bài học chính cho yêu cầu "folder riêng cho mỗi session"**: đừng gắn cứng thư mục vào một lần chạy rồi xoá. Cho nó là một bản ghi có vòng đời riêng, và phân biệt thư mục dùng-một-lần với thư mục dùng-chung.

### 3.4 Run log để ngoài bảng chính

`server/src/services/run-log-store.ts`: log đầy đủ của mỗi run nằm ở `data/run-logs/` trên đĩa (hoặc S3), **không nằm trong row của run**. Bảng chỉ giữ con trỏ và trích đoạn.

Trạng thái run: `queued`, `running`, `succeeded`, `failed`, `timed_out`, `cancelled`.

**So với ta**: bảng `runs` đang nhét cả `inputs`/`outputs` vào cột TEXT. Với chat dài và output tool lớn, DB dùng chung sẽ phình. Trạng thái run của ta cũng chỉ có `pending|success|error` — thiếu `timed_out` và `cancelled`, mà hai cái đó ta **đã** phân biệt được ở tầng code (context deadline vs cancel) rồi lại làm phẳng khi ghi.

### 3.5 Session resume

> *"Paperclip stores session IDs for resumable adapters. Next heartbeat reuses the saved session automatically. You can reset a session if context gets stale or confused."*

Giống ta (`sessions.claude_session_id` + `provider`). Cái ta thiếu là **reset có chủ đích ở tầng UI** — hiện có nút Reset trên dashboard, nhưng không có khái niệm "session này đã hỏng ngữ cảnh, mở cái mới nhưng giữ nguyên hội thoại".

### 3.6 Những gì cố ý KHÔNG lấy

| Của Paperclip | Vì sao bỏ |
|---|---|
| Org chart, CEO, hierarchy | Hai agent ngang hàng, không có chuỗi chỉ huy để escalate |
| Budget per agent (cents) | Auth theo subscription, không tính tiền theo token |
| Approval / governance | Một người dùng, chính là chủ máy — cổng duyệt là chi phí không đổi lấy gì |
| Adapter plugin system | Ta có đúng hai backend, `chat.Client` đã đủ trừu tượng |

---

## 4. Nguyên tắc thiết kế

1. **Kênh là phương tiện, không phải danh tính hội thoại.** Telegram và web là hai cách gõ vào cùng một session, không phải hai hội thoại.
2. **Một đường thực thi.** Cùng một câu hỏi phải chạy qua cùng một bộ máy bất kể vào từ đâu. Hai đường là hai hành vi, và bug chỉ xuất hiện ở một nửa.
3. **Thư mục session là bản ghi, không phải hiệu ứng phụ.** Có id, có trạng thái, có người đóng. Không tự sinh tự xoá theo run.
4. **DB giữ con trỏ, đĩa giữ khối lớn.** Transcript, log, artifact ra file; bảng giữ metadata và đường dẫn.
5. **Workspace chung giữ nguyên.** `~/goterm-workspace` là nơi project sống. Thư mục session là nơi *sản phẩm của một cuộc trò chuyện* sống.
6. **Trạng thái phải phân biệt được nguyên nhân.** `blocked` ≠ `failed`, `timed_out` ≠ `cancelled`. Làm phẳng chúng là vứt đi thông tin đã có sẵn.

---

## 5. Mô hình đối tượng đề xuất

Năm khái niệm, quan hệ rõ ràng:

```
Conversation ──1:N── Session ──1:N── Run ──1:1── Trace
     │                   │                        (runs table, đã có)
     │                   └──1:1── SessionWorkspace  (thư mục trên đĩa)
     │
     └── Channel bindings: telegram:<chat_id>, web:<user>
                                    ▲
                                    │ cùng một Conversation
Task ──(0:1)── Session          nhìn từ hai kênh
 (hàng đợi chung, đã có)
```

**Conversation** — *mới*. Một luồng trao đổi liên tục với agent. Có nhiều **binding kênh** trỏ vào nó. Đây là thứ thay thế cho `chat_id` đang bị dùng lẫn lộn vừa làm khoá Telegram vừa làm hằng số web.

**Session** — như hiện tại: một cửa sổ ngữ cảnh, có `provider_session_id` để CLI resume, rotate theo ngày/ngưỡng token. Thuộc về đúng một Conversation.

**Run** — một lượt thực thi. Đã có trong bảng `runs` dưới dạng root run.

**SessionWorkspace** — *mới*. Thư mục chứa sản phẩm của session.

**Task** — đã có. Một task **có thể** gắn vào một session (session đã sinh ra nó, hoặc session được tạo để thực thi nó), nhưng không bắt buộc.

---

## 6. Hợp nhất hai kênh

### 6.1 Việc phải làm theo đúng thứ tự

**Bước 1 — hợp nhất đường thực thi (bắt buộc trước).**

Cho dashboard đi qua **cùng đường Telegram đang đi**: `chat.Client.SendMessage`, tức để CLI sở hữu tool loop. Đổi `NewStreamSendHandler` từ `agent.RunAgent` sang gọi vào cùng handler mà bot dùng.

Được gì: web tự động có memory, auto-continue, resume đúng cách, và **tool span trong trace** (hiện đường web không có, vì CLIProvider nuốt tool call bên trong).
Mất gì: `internal/agent` + `internal/tools` không còn phục vụ đường chat. Chúng vẫn cần cho `bomclaw chat` (chế độ không gateway) — hoặc bỏ luôn nếu xác nhận không ai dùng.

> ⚠️ Đây là thay đổi lớn nhất trong tài liệu này và nên là một PR riêng, có test so sánh hai đường trước/sau.

**Bước 2 — Conversation + channel binding.**

```sql
CREATE TABLE conversations (
  id         TEXT PRIMARY KEY,
  agent_id   TEXT NOT NULL,
  title      TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE channel_bindings (
  channel         TEXT NOT NULL,   -- 'telegram' | 'web'
  external_id     TEXT NOT NULL,   -- telegram chat id, hoặc web user id
  conversation_id TEXT NOT NULL REFERENCES conversations(id),
  created_at      TEXT NOT NULL,
  PRIMARY KEY (channel, external_id)
) STRICT;
```

`sessions` thêm `conversation_id`. Migrate: mỗi `chat_id` hiện có thành một conversation; binding `telegram:<id>` và `web:1` **trỏ vào cùng một conversation** nếu chủ máy xác nhận đó là mình (mặc định: có, vì single-user từ PR #63).

**Bước 3 — một nguồn lịch sử.**

Hiện web đọc transcript (cắt 40 message), Telegram dùng bảng `messages` + CLI resume. Chốt **bảng `messages` là nguồn sự thật cho hiển thị**, transcript giữ vai trò nhật ký kiểm toán append-only. Cả hai kênh cùng ghi cả hai.

### 6.2 Một điểm phải xử lý: hai kênh nói cùng lúc

Người dùng gõ trên web trong khi Telegram đang chạy một lượt. Hiện `msgqueue` đã debounce + gom tin nhắn ở đường Telegram; đường web không qua queue.

Sau khi hợp nhất, cả hai vào chung `msgqueue`. Tin nhắn từ kênh này sẽ được gom vào lượt đang chạy của kênh kia — đúng hành vi mong muốn, và là lý do nữa để hợp nhất đường thực thi trước.

---

## 7. Thư mục làm việc theo session

### 7.1 Bố cục

```
~/.goterm/sessions/<session_id>/
├── meta.json           # id, conversation, agent, provider, mở/đóng lúc nào
├── artifacts/          # SẢN PHẨM — agent ghi vào đây
│   └── ...
└── runs/
    └── <run_id>/
        ├── prompt.md   # prompt đầy đủ đã gửi
        ├── output.md   # câu trả lời cuối
        └── tools.jsonl # tool call + kết quả (đầy đủ, không cắt)
```

`artifacts/` là của **session**, sống qua nhiều run — đúng như Paperclip để workspace tồn tại qua nhiều issue run.
`runs/<run_id>/` là của **một lượt**, và là chỗ chứa những khối lớn hiện đang bị nhét vào cột TEXT trong bảng `runs`.

### 7.2 Agent biết về nó thế nào

Cùng cách memory đang làm (`internal/memory/prompts.go`): chèn **đường dẫn tuyệt đối** vào system prompt của session mới.

```
## Thư mục của phiên này
Sản phẩm của cuộc trò chuyện này đặt tại:
  /Users/<user>/.goterm/sessions/<id>/artifacts/
- File giao cho người dùng (báo cáo, patch, ảnh) → ghi vào đây.
- Code và project vẫn ở ~/goterm-workspace như cũ. KHÔNG chuyển chúng vào đây.
- KHÔNG tự tạo thư mục session khác.
```

**Quyết định: `cwd` KHÔNG đổi.** Vẫn là `~/goterm-workspace`.

Lý do: Paperclip đổi cwd được vì workspace của nó là **git checkout của một project** — cô lập theo branch là đúng. Workspace của ta là nơi người dùng để project lâu dài, đổi cwd theo từng session sẽ cắt đứt agent khỏi chính thứ nó đang làm. Ta chỉ cần chỗ đổ **output**, không cần cô lập code.

### 7.3 Vòng đời

Theo Paperclip: **durable cho tới khi người đóng**.

| Trạng thái | Nghĩa |
|---|---|
| `open` | session còn sống, agent ghi được |
| `closed` | session đã rotate/reset; thư mục còn nguyên, chỉ đọc |
| `archived` | đã nén thành `.tar.gz`, xoá thư mục gốc |

Dọn dẹp: chỉ **archive** session `closed` quá N ngày (mặc định 30) **và** `artifacts/` rỗng. Có file thì giữ — người dùng đặt chúng ở đó là có lý do. Đây là chỗ áp dụng "shared workspaces treated more conservatively" của Paperclip: thư mục có sản phẩm được đối xử thận trọng hơn thư mục rỗng.

### 7.4 Task chạy trong đâu

`taskrunner` hiện tạo session tạm `task_<id>` không đăng ký. Sau thiết kế này nó tạo **session thật** thuộc một conversation hệ thống (`conversation: tasks`), nên task cũng có thư mục artifact riêng — và `~/goterm-shared/mailbox/` (nơi agent 2 tự đặt patch vào hôm nay vì không có chỗ nào tốt hơn) trở thành không cần thiết.

---

## 8. Nối Session ↔ Task ↔ Run

Ba thứ đã tồn tại nhưng rời nhau. Nối bằng khoá, không bằng bảng mới:

| Chiều | Cách |
|---|---|
| Run → Session | `runs.session_id` — **đã có** |
| Task → Trace | `tasks.trace_id` — **đã có** (PR #69) |
| Task → Session | thêm `tasks.session_id` |
| Session → Task | thêm `sessions.task_id` (session được tạo để chạy task đó) |
| Session → Workspace | suy ra từ `session_id`, không cần cột |

Có đủ chừng đó thì admin page trả lời được các câu hiện đang phải đoán:

- *Task này đã chạy ra cái gì?* → task → session → `artifacts/`
- *Câu trả lời này tốn bao nhiêu?* → session → runs → trace → token
- *Hội thoại này đã đẻ ra những task nào?* → conversation → sessions → tasks

---

## 9. Heartbeat mở rộng

Lấy mô hình bốn nguồn của Paperclip, bỏ `automation` (chưa có gì kích):

| Nguồn | Ta hiện có | Sau thiết kế |
|---|---|---|
| `timer` | ✅ poll 60s | giữ nguyên |
| `on_demand` | ✅ `tasks.poke` | giữ nguyên |
| `assignment` | ❌ | task giao đích danh → rung chuông ngay, không đợi poll |
| `comment` | ❌ | `bomclaw msg --to` đánh thức agent nhận |

Và thêm **coalescing**, thứ ta đang thiếu: nếu agent đang chạy một task, poke không xếp thêm run — nó đánh dấu "cần quét lại khi xong". Hiện `Runner.poke` là channel buffer 1 nên đã coalesce ở mức tín hiệu, nhưng chưa có khái niệm "đang bận" để báo ra ngoài (admin page nên thấy agent `busy` khác `idle`).

**Trạng thái task bổ sung** (§3.2):

```
submitted → working → in_review → completed
                ↓  ↘
            blocked  failed
```

- `in_review`: agent xong nhưng kết quả cần duyệt. Chính là tình huống hôm nay agent 2 review xong code và để patch lại chờ người áp — nó bị ép thành `completed` dù chưa ai xác nhận.
- `blocked`: vướng thứ ngoài tầm (thiếu quyền, chờ agent khác). Khác `failed` ở chỗ đáng thử lại khi điều kiện đổi.

---

## 10. Lộ trình

| Giai đoạn | Nội dung | Điều kiện hoàn thành |
|---|---|---|
| ~~**S0 — Hợp nhất đường thực thi**~~ ✅ | Dashboard dùng `chat.Client` thay `agent.RunAgent` | **Xong (PR #75).** Gateway gọi `bot.Handler.RunTurn` qua `chat.TurnSink`; đo thật trên dashboard: `turn → codex → Bash`, có dòng `messages`, có `tool_call` trong transcript. Sửa kèm: Manager ghi session mới đồng bộ — trước đó tin đầu của chat mới rơi vào khe debounce 1s và bị FK từ chối |
| ~~**S1 — Conversation**~~ ✅ | Bảng `conversations` + `channel_bindings`; migrate `chat_id` hiện có | **Xong (PR #76).** *Lệch so với bản thiết kế, có chủ ý*: **không** thêm `sessions.conversation_id` — khoá hội thoại chính là `sessions.chat_id` mà Manager đã dùng; `channel_bindings` ánh xạ `telegram:<id>` và `web:1` vào cùng khoá đó. Đổi ít hơn, không phải đụng Manager. Quy tắc single-user: đúng một chat Telegram → web gộp vào; nhiều hơn → web giữ riêng và log, người quyết định |
| **S2 — Một nguồn lịch sử** | Cả hai kênh ghi `messages` + transcript; UI đọc `messages` | Bỏ được giới hạn cắt 40 message của đường web |
| **S3 — Session workspace** | `~/.goterm/sessions/<id>/`, inject vào prompt, chuyển `inputs`/`outputs` lớn ra file | Agent đặt sản phẩm đúng chỗ; bảng `runs` ngừng phình |
| **S4 — Nối task** | `tasks.session_id`, `sessions.task_id`; taskrunner dùng session thật | Từ admin page bấm task → xem được artifact và trace của nó |
| **S5 — Heartbeat & trạng thái** | `assignment`/`comment` wake, cờ busy, `in_review` + `blocked` | Giao task đích danh → agent nhận trong vài giây, không đợi 60s |

S0 chặn tất cả. Làm S1 trước S0 sẽ được một hội thoại nhìn liền mạch nhưng hành xử khác nhau tuỳ chỗ gõ — tệ hơn hiện tại, vì hiện tại ít nhất người dùng biết đó là hai chỗ.

---

## 11. Rủi ro & câu hỏi mở

| Rủi ro | Mức | Xử lý |
|---|---|---|
| S0 làm hỏng đường Telegram đang chạy tốt | **Cao** | Đường Telegram không đổi; chỉ web chuyển sang dùng chung. Test so sánh trace hai kênh trước/sau |
| Web mất tính năng khi bỏ `agent.RunAgent` | Trung bình | Kiểm kê trước: tool nào `internal/tools` có mà CLI không có. Nghi ngờ có (browser tool) — phải đối chiếu |
| Migrate `chat_id` gộp nhầm hai người | Thấp | Single-user từ PR #63; vẫn nên hỏi xác nhận khi migrate thay vì gộp mặc định |
| `~/.goterm/sessions/` phình | Trung bình | Archive theo §7.3; chỉ nén session rỗng artifact |
| Agent ghi lung tung ngoài thư mục session | Trung bình | Chỉ là prompt, không cưỡng chế được. Chấp nhận như memory đang chấp nhận |

**Câu hỏi mở**

1. **`bomclaw chat` (không gateway) có ai dùng không?** Nó là lý do duy nhất `internal/agent` phải sống sau S0. Không ai dùng thì xoá được cả một tầng.
2. **Browser tool đi đường nào?** `internal/browser` hiện phục vụ `internal/tools`, tức chỉ đường web dùng được. Sau S0 nó thành mồ côi — hoặc phải phơi ra cho CLI qua MCP.
3. **Session `task_*` có nên hiện trong danh sách hội thoại không?** Nghiêng về: không mặc định, nhưng vào được từ trang task.
4. **Rotate session theo ngày có còn đúng khi hai kênh dùng chung?** Đang cắt lúc 04:00. Nếu người dùng đang nói dở trên web lúc đó thì cắt giữa chừng.

---

## Tham khảo

- [paperclipai/paperclip](https://github.com/paperclipai/paperclip) — đã clone và đọc:
  - `docs/start/core-concepts.md` — heartbeat, atomic checkout, vòng đời issue
  - `docs/agents-runtime.md` — bốn nguồn wake, coalescing, session resume, cwd/timeout per agent
  - `docs/guides/board-operator/execution-workspaces-and-runtime-services.md` — workspace tách khỏi issue, durable tới khi người đóng
  - `docs/specs/external-task-protocol.md` §6 — link state machine, quy tắc không tự xoá bản ghi phía kia
  - `server/src/services/run-log-store.ts` — log run để ngoài bảng, trên đĩa/S3
- [docs/design/shared-agent-memory.md](./shared-agent-memory.md) — tầng điều phối và trace đã triển khai (PR #66–#70)
