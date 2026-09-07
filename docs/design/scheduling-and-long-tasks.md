# Design: Việc theo lịch, và nhiều agent cùng làm một việc dài

> Trạng thái: DRAFT — thiết kế, chưa triển khai. Kèm kế hoạch P0 chi tiết ở §10.
> Phạm vi: tầng điều phối (`internal/coord`, `internal/taskrunner`) và gateway. Kế thừa trực tiếp `docs/design/shared-agent-memory.md` (đã ship, PR #66–#70) và **là bản triển khai của S5 "Heartbeat & trạng thái"** trong `docs/design/sessions-and-workspaces.md` §10 — cộng thêm hai thứ S5 chưa có tên: lịch, và task cha–con.
> Tham chiếu: OpenClaw (heartbeat, automations, background tasks, sub-agents) và Paperclip (heartbeat protocol, tách trạng thái việc khỏi trạng thái run). Chi tiết §3.

---

## 1. Mục tiêu & phi mục tiêu

**Mục tiêu**

1. **Việc chạy theo lịch** — `at` / `every` / `cron` — sống qua restart gateway, không phụ thuộc timer trong bộ nhớ. Đa số việc theo lịch là một lệnh shell; không được tốn một lượt model cho `df -h`.
2. **Một việc lớn kéo dài nhiều giờ vẫn xong.** Hôm nay task có trần cứng 15 phút × 3 lần thử ≈ 45 phút rồi kẹt vĩnh viễn. Cần vượt trần bằng *checkpoint + tiếp tục*, và **giữ nguyên ngữ cảnh** giữa các lần tiếp tục thay vì bắt đầu lại từ prompt gốc.
3. **Nhiều agent chia một việc lớn** — task cha tự tách thành task con, peer nào cũng claim được, cha thức dậy khi con xong. **Không có orchestrator** (giữ nguyên quyết định `shared-agent-memory.md` §4.3; đã xác nhận lại với người dùng 2026-09-06).
4. **Heartbeat theo nghĩa OpenClaw**: một lượt tự-kiểm định kỳ có "scratch" để agent tự nhớ việc cần theo dõi — và **bỏ qua** khi không có gì, để không đốt token.
5. **Nhìn thấy được.** Task đang chạy phải hiện trên `bomclaw status`, tray, dashboard. Mac **không được ngủ** giữa một task đang chạy — hôm nay nó ngủ.

**Phi mục tiêu (lần này)**

- Không làm org chart / budget / approval gate như Paperclip. Hai agent ngang hàng, không phải một công ty.
- Không làm payload `script` / `systemEvent` như OpenClaw. Hai kiểu `agent` và `command` là đủ (đã chốt với người dùng). Thêm khi có việc thật cần.
- Không webhook ra ngoài. Kết quả đi vào Telegram của chủ, hoặc nằm trong `tasks.result`.
- Không đổi cách chat thường chạy. Toàn bộ thiết kế này sống ở tầng task; `RunTurn` cho Telegram/web không đổi.

---

## 2. Hiện trạng (đã verify 2026-09-06)

Không có scheduler. Có hàng đợi task với lease + fencing chạy đúng (`internal/coord/tasks.go`), nhưng mọi thứ cần cho "dài" và "nhiều agent" đều thiếu hoặc chết trong thực tế.

| Cần | Có gì | Thiếu gì |
|---|---|---|
| Chạy theo giờ | Duy nhất `memory.ShouldRotate` 04:00 (`internal/memory/schedule.go:16`) — **đánh giá lười, chỉ khi có tin nhắn đến** (`bot/handler.go:524`). 04:00 không có gì xảy ra thật | Không cột `run_at`/`next_run_at`, không parser cron, không catch-up sau downtime, không dedupe giữa hai gateway |
| Task dài | `taskrunner.Config.Timeout` mặc định 15 phút (`taskrunner/runner.go:134`), `MaxAttempts=3` hardcode (`coord/tasks.go:103`) | Hết 3 lần → **kẹt ở `working` với lease quá hạn, không bao giờ về `failed`** (`ClaimTask` lọc ra, không gì chuyển trạng thái). `TaskBoard.tsx:41` vẽ "lease expired — will be reclaimed": **sai**. Mỗi lần thử lại **bắt đầu từ prompt gốc**, mất hết những gì lần trước học được |
| Ngữ cảnh giữa các lần | CLI session id có trong `sess` lúc chạy | `taskrunner` tạo session **không đăng ký** `task_<id>` mỗi lần (`runner.go:153-156`) và **không lưu** CLI session id → không `--resume` được |
| Phân rã | `context_id`, `depth`, `MaxDepth=5` có trong schema và `NewTask` | **Chết trong thực tế**: `handleTaskCreate` hardcode depth 0 / context rỗng (`gateway/admin.go:169-177`); `taskPrompt` không cho agent biết context/depth (`runner.go:217-231`) → agent tạo việc tiếp theo luôn ở depth 0, chain mới → guard chống ping-pong **không có hiệu lực** ở đường chạy thật. Không `parent_id`, không gom kết quả con |
| Tiến độ | `result` ghi một lần lúc kết thúc | Không `progress`/checkpoint; `appendEvent` unexported (`tasks.go:301`) → agent không ghi được ghi chú lên task |
| Bàn giao | `agent_messages` + `bomclaw inbox` | `SendMessage` **không đánh thức ai**; inbox không ai đọc tự động; `input-required` là hố đen — `FinishTask` nhận nhưng `ClaimTask` không bao giờ lấy lại |
| Doorbell | `PokeAgent` dial `/ws` gửi `tasks.poke` (`gateway/notify.go:21`) | **Hỏng dưới config mặc định**: `/ws` từ chối kết nối không cookie khi `gateway.auth.enabled` (`server.go:163-166`), mà `config.yaml` ship `enabled: true` → mọi poke chéo agent 401 → độ trễ bàn giao luôn = poll 60s |
| Agent chết | `agents.last_seen_at`, `StaleAfter=3m` (`coord/agents.go:10`; doc cũ nói 5m, code thắng) | Task `assigned_to` một agent chết **không bao giờ được nới** về ai-cũng-được (doc §9 nói có, code không có) |
| Nhìn thấy | `StatusResult.Runs` từ session chat (`main.go:394-412`) | Task run **vô hình**: không vào `Runs`, không `TurnEvent` → dashboard không biết, `bomclaw status` không thấy, và **tray `awakeAuto` để Mac ngủ giữa task** dù chuỗi assertion ghi "agent task in progress" (`bomtray/awake_darwin.go:14`) |
| Song song | `claimAndRun` chạy đồng bộ trong goroutine vòng lặp (`runner.go:90`) | Một agent = một task tại một thời điểm; một task dài chặn mọi thứ xếp sau |
| An toàn | `tasks.auto_claim: false` mặc định, lý do ghi rõ: chạy không giám sát với toàn quyền máy (`config.yaml:27-29`) | Scheduler = auto_claim gắn đồng hồ. Mặc định này là chuẩn mà thiết kế mới phải được xét theo |

Điểm cần nói thẳng: hạ tầng nền — atomic claim, lease, fencing, trace tree, NOTES render — **đúng và đủ tốt**. Việc lần này là xây *trên* nó, không thay nó.

---

## 3. Học từ đâu

### 3.1 OpenClaw — heartbeat không phải là liveness ping

Nguồn: [docs.openclaw.ai/gateway/heartbeat](https://docs.openclaw.ai/gateway/heartbeat), [automation/cron-jobs](https://docs.openclaw.ai/automation/cron-jobs), [automation/tasks](https://docs.openclaw.ai/automation/tasks), [tools/subagents](https://docs.openclaw.ai/tools/subagents).

> ⚠️ Sửa trích dẫn: `browser-control.md` ghi "deepwiki openclaw/openclaw §3.4.4" là kiến trúc tham chiếu. §3.4.4 là **Browser Automation**. Phần liên quan ở đây là **§3.7 Automation & Cron** và **§3.8 ACP & Sub-Agents**.

Điều quan trọng nhất phải hiểu đúng: **heartbeat của OpenClaw là một prompt đánh-thức đẩy *xuống* agent theo chu kỳ**, để nó tự kiểm — không phải tín hiệu sống đẩy *lên*. Ngược hoàn toàn với nghĩa thường của từ này trong hệ phân tán. Liveness của ta (`agents.last_seen_at`) là thứ khác và **giữ riêng**.

Lấy:
- **Heartbeat = một automation job hệ thống sở hữu, mỗi agent một job**, mặc định `every 30m` (1h với OAuth). Prompt mặc định: *"Follow the heartbeat monitor scratch context when provided… If nothing needs attention, reply NO_REPLY."* **Scratch** là chỗ agent tự ghi việc cần theo dõi; **scratch rỗng → bỏ qua ngay, không gọi API**. Còn bỏ qua khi lane đang bận, ngoài `activeHours`.
- **Payload không phải lúc nào cũng là model**: `agentTurn` / `command` / `script` / `systemEvent`. Ta lấy hai đầu.
- **Bậc thang backoff 30s→60s→5m→15m→60m, tự tắt sau 10 lần lỗi liên tiếp**, báo sau 2 lần. Job hỏng không được đốt token mãi.
- **`cron.skipMissedJobs`**: mặc định chạy bù sau downtime (một lần, không N lần).
- **Hai giá trị wake**: `now` vs `next-heartbeat`. Người gọi nói được mức khẩn mà không ép mọi sự kiện thành một lượt đắt.
- **Sổ cái task** với trạng thái `queued|running|succeeded|failed|timed_out|cancelled|lost`; câu định vị đáng lấy nguyên: *"Automations and heartbeat decide **when** work runs; tasks track **what happened**."*
- **Hồi phục sau restart**: sub-agent bị ngắt **tự resume từ transcript con**, giữ requester + task identity + idempotency key. Ta có đúng thứ đó trong tay: CLI session id.
- **Trạng thái con lấy từ runtime, không từ text model.** Model nói "done!" không phải tín hiệu hoàn thành.
- Cạm bẫy đã báo (issue #64103): trạng thái session `failed/timeout/done` chỉ mô tả *lượt cuối* nhưng tên gợi ý kết thúc vĩnh viễn → orchestrator spawn trùng thay vì resume. **Đây là bẫy đặt tên ta phải tránh.**
- Keep-awake: OpenClaw không làm trong core; là chuyện supervisor + `caffeinate`/IOKit. Ta đã có IOKit trong tray — chỉ cần nó *nhìn thấy* task.

### 3.2 Paperclip — tách "trạng thái việc" khỏi "trạng thái run"

Nguồn: [heartbeat-protocol.md](https://github.com/paperclipai/paperclip/blob/master/docs/guides/agent-developer/heartbeat-protocol.md), [docs.paperclip.ing/guides/org/agents](https://docs.paperclip.ing/guides/org/agents/). (Người dùng viết "pageclip"; đối chiếu ngữ cảnh — *"If OpenClaw is an employee, Paperclip is the company"* — đây là thứ được nhắc tới.)

Lấy:
- **Hai mặt phẳng trạng thái, trực giao.** *Issue status* (`todo|in_progress|in_review|blocked|done`) là trạng thái việc, có thẩm quyền. *Run liveness* (`completed|advanced|plan_only|empty_response|blocked|failed|needs_followup`) là metadata của **một lần chạy**, *không* thay máy trạng thái việc. Gộp hai cái là nguyên nhân đúng của bẫy #64103 ở trên.
- **`plan_only` / `empty_response` là lý do tiếp tục tự động.** Agent CLI rất hay trả về một *kế hoạch* thay vì làm. Phát hiện bằng cấu trúc, tiếp tục có giới hạn, và khi hết lượt thì **để lại comment audit cho người** thay vì im lặng bỏ.
- **Atomic checkout, "never retry a 409".** Ta đã có (`UPDATE … RETURNING`).
- **`X-Paperclip-Run-Id` trên mọi lệnh ghi** — idempotency + truy vết trong một nước.
- **Heartbeat interval là sàn, không phải cam kết**; agent bận chạy dày hơn.
- Protocol 9 bước lúc thức: me → assignments → pick (in-progress trước) → checkout → hiểu ngữ cảnh → **làm việc ngay trong heartbeat này** → cập nhật → delegate. Bước 7 là điểm ta cần đưa vào prompt.

### 3.3 Không lấy

Org chart, budget theo cent, approval gate, adapter plugin (Paperclip); webhook delivery, condition-trigger script, `script`/`systemEvent` payload, ~200 config field (OpenClaw). Lý do như `sessions-and-workspaces.md` §3.6 và định vị "~15 field vs ~200" trong README.

---

## 4. Nguyên tắc

Kế thừa (đã chốt, không bàn lại):

1. **DB là sự thật; chuông chỉ làm nhanh.** Scheduler phải là *hàng trong DB mà vòng quét tìm thấy*, không phải timer trong bộ nhớ. Restart không được mất một job nào. (`shared-agent-memory.md` §4.5, §9.)
2. **Lease + fencing, handler idempotent.** Việc có thể chạy hai lần; kết quả của người mất lease bị bỏ. (§4.4, §4.6.)
3. **Không orchestrator.** Cha–con là cấu trúc dữ liệu, không phải chuỗi chỉ huy. Con do peer claim. (§4.3.)
4. **Bề mặt là `bomclaw <verb>`**, dạy trong system prompt; người gõ được để debug. (§10.)
5. **Trace best-effort, không bao giờ làm chậm lượt.** (§5.)
6. **Trạng thái giữ nguyên nhân**: `timed_out` ≠ `failed` ≠ `canceled`. (`sessions-and-workspaces.md` §4.6.)

Mới (lần này):

7. **Tách trạng thái việc khỏi trạng thái run.** `tasks.state` là trạng thái việc. Mỗi lần claim là một **run** với liveness riêng. Không bao giờ ghi `failed` lên việc chỉ vì *một run* hết giờ.
8. **Scheduler sở hữu retry và backoff, không phải agent.** Agent không được tự lặp; nó ghi checkpoint và trả về. Hệ thống quyết định có gọi lại không.
9. **Hợp đồng chạy không giám sát viết thẳng vào prompt**: câu trả lời cuối là *sản phẩm*, không phải kế hoạch; `NO_REPLY` khi không có gì; có ngân sách thời gian, ghi tiến độ trước khi hết.
10. **Không tốn model cho việc không cần model.** `command` là công dân hạng nhất.
11. **Mọi thứ đang chạy đều phải nhìn thấy được** từ một nơi (`StatusResult.Runs`), vì awake, tray, dashboard đều đọc từ đó.
12. **Tự động không có nghĩa là mặc định.** `tasks.auto_claim` giữ `false`; scheduler và heartbeat **tắt** cho tới khi bật rõ. Bật là quyết định của người, ghi trong config.

---

## 5. Thiết kế

### 5.1 Từ vựng

| Từ | Nghĩa |
|---|---|
| **Task** | một việc trong hàng đợi chung (như cũ). Có thể có **cha** (`parent_id`). |
| **Run** | **một lần claim → chạy → trả về** của một task. Một task dài có nhiều run. Trước đây hai khái niệm này bị gộp. |
| **Liveness** (của run) | `completed` · `advanced` · `plan_only` · `empty` · `blocked` · `failed` · `timed_out` · `canceled`. Lấy từ Paperclip, cộng `timed_out`/`canceled` để giữ nguyên nhân. |
| **Checkpoint** | đoạn text agent ghi lên task bằng `bomclaw task progress` — "đã tới đâu, còn gì". Được nạp vào prompt của run kế tiếp. |
| **Continuation** | run kế tiếp của cùng một task sau `advanced`/`plan_only`/`empty`/`timed_out`. Đếm riêng khỏi `attempts` (attempts là *lỗi*, continuation là *chưa xong*). |
| **Session ref** | CLI session id + provider + account mà task đang dùng, lưu trên task để continuation `--resume` **giữ nguyên ngữ cảnh**. |
| **Schedule** | một hàng khai báo "khi nào, làm gì". Khi đến giờ nó **vật chất hoá** thành một task (kiểu `agent`) hoặc chạy thẳng (kiểu `command`). |
| **Heartbeat** | một schedule hệ thống sở hữu, mỗi agent một, chạy prompt tự-kiểm với **scratch**. |
| **Wake** | tín hiệu đánh thức vòng quét: `now` (poke ngay) hay `next` (để poll tự thấy). |

### 5.2 Task cha–con, ngang hàng

```
tasks.parent_id  → cha (NULL = task gốc)
tasks.context_id → gốc của cả cây (giữ nguyên nghĩa A2A; con kế thừa của cha)
tasks.depth      → depth cha + 1, cap MaxDepth=5 (đã có, giờ có hiệu lực)
```

**Tạo con.** Agent đang giữ task T gõ `bomclaw task sub --parent T --title … [--to agent]`. Con sinh ra `submitted`, ai claim cũng được (hoặc đích danh). `depth = T.depth+1`, `context_id = T.context_id`. Quá `MaxDepth` → từ chối như hiện tại, và lần này **có hiệu lực thật** vì prompt cho agent biết depth của mình.

**Cha chờ con.** Sau khi tạo đủ con, agent trả về run với liveness `blocked` (lệnh `bomclaw task block --id T --on children`). Task T sang trạng thái `blocked`, lease thả, **không claim được** cho tới khi điều kiện gỡ.

**Con xong → cha thức.** Trong `FinishTask` của con, cùng transaction: nếu `parent_id` có và **mọi anh chị em đã terminal** → cha `blocked → submitted`, `lease_until = now`, `task_events` ghi `children-done`, và **poke** agent gần nhất đã giữ cha (soft affinity, §5.4). Run kế tiếp của cha nhận **kết quả của từng con** trong prompt để gom.

**Không có manager.** Cha không "điều phối" con — nó chỉ *tạo* con rồi *ngủ*, và *gom* khi được gọi lại. Bất kỳ peer nào cũng có thể là người claim cha lần sau (nếu session ref cho phép, §5.3). Đây là điểm khác Paperclip/OpenClaw và đúng ý §4.3.

**Chống vòng lặp** (rủi ro Cao trong `shared-agent-memory.md` §13): (a) `MaxDepth` giờ có răng; (b) prompt cấm tạo con để "tiếp tục việc của chính mình" — muốn tiếp tục thì ghi checkpoint và `advanced`, không tạo con; (c) giới hạn số con mở cùng lúc mỗi cha (`max_open_children`, mặc định 8, như OpenClaw `maxChildrenPerAgent`).

### 5.3 Chạy dài: run có trần, task không có trần

Trần 15 phút **giữ nguyên** cho *một run* — đó là van an toàn cho CLI treo. Cái đổi là chuyện gì xảy ra khi chạm trần.

```
claim → run (≤ timeout) ──┬── completed  → task completed
                          ├── advanced   → task submitted lại, continuations+1, lease=now, affinity
                          ├── plan_only  → như advanced, prompt lần sau: "đừng lập kế hoạch nữa, làm"
                          ├── empty      → như advanced (giới hạn riêng nhỏ hơn: 2)
                          ├── timed_out  → có checkpoint mới? advanced : attempts+1
                          ├── blocked    → task blocked (chờ con / chờ người)
                          ├── failed     → attempts+1; ≥ max_attempts → task failed (reason: exhausted)
                          └── canceled   → không đổi attempts/continuations
```

**Phân loại liveness lấy từ runtime, không từ text** (OpenClaw §3.1): `timed_out` từ ctx deadline; `canceled` từ ctx cancel; `completed`/`advanced`/`blocked` từ **lệnh agent gõ** (`bomclaw task done|progress|block`) — không phải từ việc model *nói* "xong". Nếu run kết thúc mà agent không gõ lệnh nào và có text → `plan_only` nếu text không chứa tín hiệu hoàn thành và có TodoWrite pending (tái dùng heuristic `autocontinue.go`), ngược lại `completed` với result = text (giữ hành vi cũ cho task ngắn).

**Giữ ngữ cảnh — điểm quyết định.** Run đầu tạo CLI session; ta lưu `session_ref = {provider, cli_session_id, account}` lên task (`sess.GetSessionID()` đã có trong tay lúc chạy, `runner.go:153-169`). Run kế tiếp **`--resume`** đúng session đó → agent nhớ toàn bộ những gì đã làm, không phải đọc lại từ prompt gốc. Đây là "session resume across heartbeats" của Paperclip và "resume child transcript" của OpenClaw, và ta có nó **miễn phí** vì CLI đã lưu session trên đĩa.

Hệ quả bắt buộc (từ `docs/credential-pools.md`): **CLI session sống trong config dir của account**. Nên continuation phải chạy **cùng agent, cùng account** với run trước. Quy tắc:

- `advanced`/`plan_only`/`empty`/`timed_out` → đặt `assigned_to = claimed_by` (**soft affinity**). Peer khác thấy nhưng không claim.
- Agent đó **chết** (`last_seen_at` quá `StaleAfter`) → vòng quét nới `assigned_to = NULL` **và xoá `session_ref`**; peer claim với session mới, prompt nạp `checkpoint` thay cho ngữ cảnh. Đây chính là quy tắc "agent chết → nới về NULL" mà `shared-agent-memory.md` §9 đã hứa và code chưa có — nay có, và có lý do rõ.
- Hai agent khác CLI (claude vs codex) không bao giờ resume được session của nhau — affinity là điều kiện cần, không chỉ là tối ưu.

**Giới hạn.** `max_continuations` mặc định 20 (×15 phút ≈ 5 giờ làm việc thật, cộng thời gian chờ con). Hết → task `failed` với reason `continuations-exhausted`, `task_events` ghi checkpoint cuối, **báo Telegram cho chủ**: *"Task X đã chạy 20 lượt chưa xong. Tiến độ cuối: … Muốn tiếp tục: `bomclaw task resume X --more 10`."* Không bao giờ im lặng bỏ (Paperclip).

**Prompt run** (thay `taskPrompt`, `runner.go:217`) nói rõ: task id, context, depth, cha (nếu có), **checkpoint lần trước**, **kết quả các con** (nếu là cha thức lại), **ngân sách: N phút**, và hợp đồng:

> Bạn có N phút cho lượt này. Nếu chưa xong, ghi tiến độ bằng `bomclaw task progress --id T --note "…"` trước khi hết giờ rồi dừng — hệ thống sẽ gọi lại bạn với đúng ngữ cảnh này. Không tự lặp, không tạo task con để tiếp tục việc của mình. Xong thì `bomclaw task done`. Cần nhiều tay: `bomclaw task sub`. Cần chờ người: `bomclaw task block --on human --note "…"`. Câu trả lời cuối là sản phẩm, không phải kế hoạch. Không ai đang chờ để trả lời câu hỏi của bạn.

### 5.4 Wake: sửa chuông, thêm nguồn

Hôm nay chỉ có `timer` (60s) và `on_demand` (poke — **đang hỏng**). Thêm `assignment` và `comment` như S5 đã định, và **sửa chuông**:

- **Chuông đi đường HTTP loopback**, không đi `/ws`: `POST /api/tasks/poke` sau `RequireAuthExceptLocal` — đúng mẫu `/api/status` và `/api/browser/*` đã có. Hai agent cùng máy nên loopback là đủ; `/ws` giữ nguyên quy tắc cookie cho tunnel. `PokeAgent` đổi sang HTTP.
- **`assignment`**: `task new --to X` / `task sub --to X` / cha thức lại → poke X.
- **`comment`**: `bomclaw msg --to X` → poke X, và prompt của X lần sau có mục "Tin nhắn chưa đọc" (đọc `Inbox(unread)` rồi `MarkRead` — hôm nay không ai gọi `MarkRead`).
- **Hai giá trị**: `now` (poke, coalesce như hiện tại) / `next` (chỉ ghi DB). Mặc định `now` cho assignment và children-done; `next` cho comment không kèm task.
- Chuông vẫn best-effort. Vòng quét 60s vẫn là thứ bảo đảm. Không đổi nguyên tắc.

### 5.5 Lịch

```sql
schedules
  id, name, created_by,
  kind            'at' | 'every' | 'cron'
  spec            ISO time | duration | 5-field cron
  tz              IANA, mặc định TZ máy chủ (lưu tường minh, không để trống)
  payload_kind    'agent' | 'command'
  payload         JSON: {title, body, to?}  hoặc  {cmd, cwd?, timeout_s?}
  enabled
  next_run_at, last_run_at, last_status
  consecutive_failures
  skip_missed     0/1 (mặc định 0 = chạy bù một lần)
  owner_agent     '' = gateway nào cũng được nhận; 'bomclaw2' = chỉ gateway đó
```

**Vòng quét** trong gateway (chung goroutine với heartbeat agent, 30s): `UPDATE schedules SET next_run_at=?, last_run_at=now WHERE id=? AND next_run_at=<giá trị vừa đọc> AND next_run_at<=now` — **compare-and-set**: hai gateway cùng thấy đến giờ, đúng một cái thắng. Không cần lock, không cần leader.

Thắng thì:
- `agent` → **vật chất hoá một hàng `tasks`** (`kind='scheduled'`, `schedule_id`, `context_id` mới) → đi tiếp đường claim/lease/fencing **y như task thường**. Scheduler không chạy model; nó chỉ tạo việc. Đây là cách rẻ nhất để scheduler thừa hưởng toàn bộ độ bền đã có.
- `command` → chạy shell ngay trong gateway với timeout, ghi `schedule_runs` (id, schedule_id, started/ended, exit_code, output cắt 8KB). Lỗi/exit≠0 → `consecutive_failures++`. **Không tạo task, không gọi model.**

**Catch-up**: lúc khởi động, `next_run_at` trong quá khứ → chạy **một lần** rồi tính `next_run_at` từ *bây giờ*, không phải từ mốc cũ (tránh 8 lần "09:00" sau 8 giờ downtime). `skip_missed=1` thì chỉ tính lại, không chạy.

**Bậc thang lỗi** (OpenClaw): sau lỗi liên tiếp, `next_run_at` lùi 30s→1m→5m→15m→1h; **tự `enabled=0` ở lần 10**, Telegram báo ở lần 2 (cooldown 1h). Với `agent` payload, "lỗi" = task vật chất hoá kết thúc `failed`.

**Cron parser**: `github.com/robfig/cron/v3` parser (chỉ dùng parser, không dùng scheduler của nó — scheduler là DB). Ghi rõ trong docs cạm bẫy DOM/DOW OR-logic của chuẩn cron.

**Rotate 04:00** hiện tại (`session.reset.daily_at`) **không đụng** lần này; ghi ở câu hỏi mở §11 là ứng viên chuyển lên scheduler khi S3 xong.

### 5.6 Heartbeat

Mỗi agent bật heartbeat có **một hàng `schedules` hệ thống sở hữu** (`name = 'heartbeat:<agent>'`, `owner_agent = <agent>`, `kind='every'`, `spec` từ config, mặc định `30m`; `payload_kind='agent'`). Không xoá được từ CLI thường; đổi bằng config.

**Scratch**: cột `agents.scratch` (cap 64KB). Agent ghi `bomclaw heartbeat scratch --set "theo dõi PR #91 tới khi CI xanh"` / `--append` / `--clear`. Người cũng gõ được.

**Bỏ qua trước khi tốn token** (thứ tự): heartbeat tắt → ngoài `active_hours` → **scratch rỗng** → agent đang có run (`Runs` không rỗng) → mới tạo task heartbeat. Task heartbeat chạy trong **session cách ly** (không phải chat), prompt:

> Đây là heartbeat. Đọc ghi chú theo dõi dưới đây; chỉ hành động nếu có việc thật sự cần. Việc lặp là schedule — tạo bằng `bomclaw schedule add`, không ghi vào scratch. Không suy đoán lại việc cũ từ hội thoại trước. Xong thì cập nhật scratch (xoá mục đã xong). Không có gì cần làm → trả lời đúng `NO_REPLY`.
> ## Scratch
> …

`NO_REPLY` → task `completed`, không gửi gì. Khác → gửi vào Telegram của chủ. Heartbeat **không** kéo dài freshness của chat session (OpenClaw), và **không** đếm vào continuation của việc gì.

Config:
```yaml
heartbeat:
  enabled: false        # tắt cho tới khi bật rõ (nguyên tắc 12)
  every: "30m"
  active_hours: "08:00-23:00"
```

### 5.7 Nhìn thấy, và không ngủ

- `taskrunner` **đăng ký** session của run vào `session.Manager` (chat id âm `-1` như hiện tại, id `task_<id>`), gọi `MarkRunning`/`NoteTool`/`MarkIdle` → tự động vào `StatusResult.Runs`. `RunInfo` thêm `Kind: "task" | "chat"` và `TaskID`.
- Hệ quả tức thì: tray `awakeAuto` **giữ Mac thức** trong task (nó đọc `Runs`); `bomclaw status` thấy; tray hiện `⚡ task: <title>`.
- `taskrunner` phát `TurnEvent{Phase: started|finished, Kind: task}` → dashboard nhận `session.turn` như chat; TaskBoard bỏ poll 5s khi có push.
- Sửa hiển thị sai: hết `max_attempts` → **`failed` thật** với `task_events` note `exhausted`; TaskBoard không còn "will be reclaimed" cho task chết.
- `bomclaw task show T` in: cây con, run gần nhất với liveness, checkpoint, session ref (che), continuations còn lại.

### 5.8 Bề mặt lệnh

Mới / đổi (tất cả ghi thẳng DB như `task` hiện tại, poke qua HTTP loopback):

```
bomclaw task sub      --parent T --title X [--body …] [--to agent]     # tạo con
bomclaw task progress --id T --note "…"                                 # checkpoint
bomclaw task block    --id T --on children|human [--note "…"]
bomclaw task resume   --id T [--more N]                                  # nới continuation, người gõ
bomclaw task show     --id T                                             # có cây + run + checkpoint

bomclaw schedule add  --name X --cron "0 8 * * 1-5" [--tz Asia/Ho_Chi_Minh] \
                      --agent-task "Tóm tắt inbox" | --command "df -h"  [--to agent] [--skip-missed]
bomclaw schedule add  --name X --every 10m --command "…"
bomclaw schedule add  --name X --at 2026-09-07T09:00 --agent-task "…"
bomclaw schedule list | show | enable | disable | remove | run-now

bomclaw heartbeat scratch --set|--append|--clear|--show
bomclaw heartbeat run-now
```

RPC thêm: `schedules.list/create/toggle/delete/run`, `tasks.progress`, `heartbeat.scratch`. Dashboard: tab Schedules (P1), TaskBoard vẽ cây (P1).

System prompt (`config.yaml` mục "Working with the other agents"): thêm đoạn về `task sub/progress/block`, `schedule add` cho việc lặp, và **sửa câu cấm**: từ *"Do not create tasks in a loop…"* thành *"Do not create tasks to continue your own work — write progress and return; the system calls you back. Create sub-tasks only for work a peer can do in parallel. Never create a task from inside a task at depth 5."*

---

## 6. Schema đề xuất (coord schema v3)

```sql
-- ── tasks: thêm cột, không đổi cột cũ ──────────────────────────────────
ALTER TABLE tasks ADD COLUMN parent_id          TEXT DEFAULT '';
ALTER TABLE tasks ADD COLUMN kind               TEXT DEFAULT 'manual';   -- manual|scheduled|heartbeat|sub
ALTER TABLE tasks ADD COLUMN schedule_id        TEXT DEFAULT '';
ALTER TABLE tasks ADD COLUMN checkpoint         TEXT DEFAULT '';        -- ghi chú tiến độ mới nhất
ALTER TABLE tasks ADD COLUMN session_ref        TEXT DEFAULT '';        -- JSON {provider, session_id, account}
ALTER TABLE tasks ADD COLUMN continuations      INTEGER DEFAULT 0;
ALTER TABLE tasks ADD COLUMN max_continuations  INTEGER DEFAULT 20;
ALTER TABLE tasks ADD COLUMN blocked_on         TEXT DEFAULT '';        -- ''|children|human
ALTER TABLE tasks ADD COLUMN fail_reason        TEXT DEFAULT '';        -- exhausted|continuations-exhausted|...
CREATE INDEX IF NOT EXISTS idx_tasks_parent ON tasks(parent_id, state);

-- state thêm giá trị: 'blocked'. Bỏ dùng 'input-required' (giữ hằng cho tương thích; blocked_on='human' thay nó)

-- ── task_runs: một hàng mỗi lần claim ──────────────────────────────────
CREATE TABLE IF NOT EXISTS task_runs (
  id          TEXT PRIMARY KEY,           -- 'tr_' || uuid
  task_id     TEXT NOT NULL,
  agent_id    TEXT NOT NULL,
  attempt     INTEGER NOT NULL,           -- fencing token của lần claim này
  liveness    TEXT NOT NULL DEFAULT 'running',
              -- running|completed|advanced|plan_only|empty|blocked|failed|timed_out|canceled
  trace_id    TEXT DEFAULT '',
  started_at  TEXT NOT NULL,
  ended_at    TEXT DEFAULT '',
  note        TEXT DEFAULT ''             -- lỗi hoặc tóm tắt ngắn
);
CREATE INDEX IF NOT EXISTS idx_task_runs_task ON task_runs(task_id, started_at);

-- ── schedules ───────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS schedules (
  id                   TEXT PRIMARY KEY,  -- 'sch_' || uuid
  name                 TEXT NOT NULL UNIQUE,
  created_by           TEXT NOT NULL,
  owner_agent          TEXT DEFAULT '',   -- '' = gateway nào cũng nhận
  kind                 TEXT NOT NULL,     -- at|every|cron
  spec                 TEXT NOT NULL,
  tz                   TEXT NOT NULL,
  payload_kind         TEXT NOT NULL,     -- agent|command
  payload              TEXT NOT NULL,     -- JSON
  enabled              INTEGER NOT NULL DEFAULT 1,
  system               INTEGER NOT NULL DEFAULT 0,  -- 1 = heartbeat, không xoá từ CLI
  skip_missed          INTEGER NOT NULL DEFAULT 0,
  next_run_at          TEXT NOT NULL,
  last_run_at          TEXT DEFAULT '',
  last_status          TEXT DEFAULT '',
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_schedules_due ON schedules(enabled, next_run_at);

CREATE TABLE IF NOT EXISTS schedule_runs (
  id          TEXT PRIMARY KEY,
  schedule_id TEXT NOT NULL,
  task_id     TEXT DEFAULT '',            -- khi payload_kind = agent
  started_at  TEXT NOT NULL,
  ended_at    TEXT DEFAULT '',
  status      TEXT NOT NULL,              -- ok|failed|skipped
  exit_code   INTEGER DEFAULT 0,
  output      TEXT DEFAULT ''             -- cắt 8KB
);
CREATE INDEX IF NOT EXISTS idx_schedule_runs ON schedule_runs(schedule_id, started_at);

-- ── agents: scratch cho heartbeat ───────────────────────────────────────
ALTER TABLE agents ADD COLUMN scratch TEXT DEFAULT '';
```

Giữ nguyên: `lease_until`/`attempts`/`max_attempts` nghĩa cũ. **Không** tái dụng `lease_until` làm "đến giờ" — `ClaimTask` sẽ không phân biệt được "chưa tới giờ" với "chủ cũ chết". Việc theo lịch **không nằm trong `tasks` cho tới khi đến giờ**; đó là việc của `schedules.next_run_at`.

`task_runs` là "sổ cái *chuyện gì đã xảy ra*" (OpenClaw §3.1); `runs`/trace vẫn là cây span kỹ thuật. Hai bảng, hai câu hỏi.

---

## 7. Hai luồng ví dụ

**A. Một việc lớn, hai agent, hai giờ.** Người gõ Telegram cho agent 1: *"Tổng hợp tin tuần từ 5 nguồn thành một báo cáo."*

1. Agent 1 (chat) tạo task gốc `T` (`bomclaw task new`), chat trả lời "đã xếp việc".
2. Agent 1 claim `T` (auto_claim bật). Run 1: đọc yêu cầu, tạo 5 con `T1…T5` (`task sub`), `task block --on children`. Liveness `blocked`. `T` → `blocked`. Poke agent 2.
3. Agent 2 claim `T2`, `T4`; agent 1 claim `T1`, `T3`, `T5` (từng cái, tuần tự — song song là P2). Mỗi con ≤15 phút; con nào dài thì `progress` + `advanced` + affinity + `--resume`.
4. Con cuối `done` → cùng transaction: `T` `blocked → submitted`, event `children-done`, poke agent 1 (affinity: người giữ `T` lần trước).
5. Agent 1 claim `T`, run 2 **`--resume`** session của run 1, prompt kèm 5 kết quả con. Viết báo cáo. `task done`. Kết quả về Telegram.

Restart gateway ở bất kỳ bước nào: lease hết → claim lại → `--resume` nếu cùng agent, `checkpoint` nếu agent chết. Không mất gì ngoài thời gian.

**B. Việc theo lịch.** `bomclaw schedule add --name inbox-brief --cron "0 8 * * 1-5" --tz Asia/Ho_Chi_Minh --agent-task "Tóm tắt inbox và lịch hôm nay, gửi tôi"`. 08:00 thứ Hai: gateway nào quét trước CAS thắng, tạo `tasks` hàng `kind=scheduled`; agent rảnh claim, chạy, kết quả về Telegram; `schedule_runs` ghi `ok`. Máy tắt qua 08:00 → bật lại 10:30 → chạy bù **một lần**, `next_run_at` = 08:00 mai. `--command "brew outdated"` mỗi 6h thì không tạo task, không model, chỉ `schedule_runs`.

---

## 8. Tương tác với những gì đã có

| Với | Cách |
|---|---|
| **S3 Session workspace** (chưa làm) | `task_runs.trace_id` + `tasks.session_ref` là hai đầu móc sẵn; khi S3 tới, thư mục `~/.goterm/sessions/task_<id>/` gắn vào đây, không cần đổi schema |
| **S4 Nối task** | `taskrunner` đăng ký session thật (§5.7) chính là nửa của S4; `tasks.session_ref` là `sessions.task_id` nhìn từ phía kia |
| **Credential pool** (`docs/credential-pools.md`) | continuation resume phải cùng account → `session_ref.account`; `Pick(pinned)` đã có. Task mới thì pool chọn LRU như chat |
| **Browser Bridge** | không đổi; task chạy `bomclaw browser` như chat. Lưu ý: hai agent chạy task song song đều lái browser → mỗi agent một tab đã có (PR #83) |
| **Memory flush** | run task **không** kích flush (flush là của chat session); heartbeat cũng không |
| **`autocontinue.go`** | heuristic TodoWrite pending tái dùng để phân loại `plan_only`; `maxAutoContinue=3` trong-lượt giữ nguyên, khác với continuation giữa-các-run |
| **Trace** | mỗi run một root trace như hiện tại (`StartTrace("task")`), thêm tag `task_run_id`; cha–con nhìn thấy qua `context_id` chung |

---

## 9. Lộ trình

| Giai đoạn | Nội dung | Điều kiện hoàn thành |
|---|---|---|
| ~~**P0 — Nền cho "dài" và "nhìn thấy"**~~ ✅ | `task_runs` + liveness; `checkpoint`/`session_ref`/`continuations` + `--resume`; `progress`/`done` lệnh; dead-letter thật; task run vào `Runs` + `TurnEvent`; sửa chuông sang HTTP loopback; nới `assigned_to` khi agent chết | Một task 40 phút xong qua 3 run **giữ ngữ cảnh**; tray giữ Mac thức suốt; hết attempts → `failed` thật trên TaskBoard; poke chéo agent < 2s dưới auth bật. **Xong (PR #88–#91).** Đo thật: task claim sau 9s qua chuông HTTP; run đóng đúng; Runs API hiện `kind:task`. Hai bug chỉ lộ khi chạy thật: `SQLITE_BUSY` khi FinishRun đọc-rồi-ghi trong khi trace recorder ghi span (→ `_txlock=immediate` + retry, #90) và sweep của gateway kia gặt run còn sống ngay sau `task done` (→ reap theo tuổi, FinishRun ghi đè `lost`, #91) |
| **P1 — Lịch & heartbeat** | **P1a ✅ lịch**: `schedules`/`schedule_runs` (schema v4, kèm `agents.scratch` cho P1b); vòng quét CAS 30s; `agent`→task `kind=scheduled` (run `pending` tới khi task kết thúc, gateway nào thấy trước thì settle — cũng CAS), `command`→sh trong gateway, output cắt 8KB giữ đầu+cuối; catch-up một lần / `--skip-missed`; bậc thang 30s→1m→5m→15m→1h, báo Telegram lần 2 (cooldown 1h), tự tắt lần 10; kết quả task theo lịch **gửi Telegram** cho chủ (`--quiet` để không); `bomclaw schedule add|list|show|enable|disable|remove|run-now`; RPC `schedules.*`; config `schedules.enabled` mặc định false. **P1b** heartbeat + scratch + skip rules; **P1c** tab Schedules — chưa | `0 8 * * 1-5` chạy đúng giờ đúng TZ, đúng một lần khi hai gateway (test `TestTwoGatewaysFireOnce`, `TestClaimScheduleIsExclusiveAcrossHandles`); máy tắt qua giờ → bù một lần (`TestMissedScheduleCatchesUpOnce`); heartbeat scratch rỗng → 0 API call |
| **P2 — Cha–con** | `parent_id`, `task sub/block`, children-done → cha thức, prompt gom kết quả con, `max_open_children`, TaskBoard vẽ cây, wake `comment` + auto-read inbox | Luồng A (§7) chạy thật với hai agent; `MaxDepth` từ chối ở depth 6 trên đường thật |
| **P3 — Song song trong một agent** | taskrunner claim nhiều task đồng thời (giới hạn `tasks.concurrency`), vẫn một lane cho chat | Task dài không chặn task ngắn xếp sau |

P0 trước P1 vì lịch sinh ra task — task chưa "dài được, thấy được" thì lịch chỉ nhân bản vấn đề theo giờ. P2 sau P1 vì cha–con cần `task_runs` (P0) và cần chuông chạy (P0) để cha thức đúng lúc.

---

## 9b. Ghi chú triển khai P1a (khác/thêm so với §5.5)

- **Kết quả task theo lịch được gửi Telegram** cho `security.allowed_user_ids` khi task `completed` (payload `quiet: true` để chỉ ghi). §5.5 không nói; lý do: một "tóm tắt inbox 08:00" không ai đọc là một lần gọi model vô ích. Đường gửi: `bot.Bot.Notify` — không có hội thoại để trả lời vào, nên người nhận là danh sách tin cậy trong config, không phải "ai nhắn gần nhất"; không có allow-list thì chỉ ghi log.
- **"Lỗi" của payload `agent`**: hàng `schedule_runs` sinh ra ở trạng thái `pending` mang `task_id`; mỗi tick, gateway nào cũng quét pending, task `completed` → `ok`, `failed|canceled|rejected` → `failed`. Việc đóng là CAS trên `status` (`pending→…`) nên hai gateway không đếm lỗi hai lần.
- **Thời điểm kế tiếp** tính từ *lúc claim* (`every` = now+d, `cron` = Next(now) trong TZ, `at` = `Never`). `ScheduleSucceeded` không đụng `next_run_at` (có thể đã claim lần mới); `ScheduleFailed` ghi đè `next_run_at = now + Backoff(n)` — bậc thang **đè** nhịp riêng của lịch, job hằng ngày lỗi sẽ thử lại trong giờ, không đợi mai.
- **Missed** = trễ hơn `2×tick` (60s) — trễ một tick là bình thường, không phải downtime.
- **Spec không parse được** (TZ bị gỡ, lỗi dữ liệu) → tự `enabled=0` + báo, thay vì log lỗi mỗi 30s mãi.
- Parser cron: `robfig/cron/v3` chuẩn 5 trường + descriptor (`@daily`, `@every 1h`); DOM và DOW cùng đặt → **OR** (ghi trong `schedule --help`).
- `schedule run-now` chỉ đẩy `next_run_at = now`; gateway bắt ở tick kế (≤30s). RPC `schedules.run` có thêm poke vòng quét cục bộ nên từ dashboard là ngay.
- Chưa có: heartbeat (P1b), tab Schedules (P1c), purge `schedule_runs` theo tuổi (hàng nhỏ; thêm khi cần).

---

## 10. Kế hoạch triển khai P0

Mục tiêu P0: **một task 40 phút xong, giữ ngữ cảnh, nhìn thấy, không kẹt, không ngủ.** Chưa có lịch, chưa có cha–con.

### 10.1 Thứ tự & file

| # | Việc | File | Ghi chú |
|---|---|---|---|
| 1 | Coord schema v3: `task_runs`, cột mới trên `tasks`, `blocked` state, `fail_reason` | `internal/coord/coord.go` (DDL + `migrate` v2→v3), `internal/coord/tasks.go` | Thêm cột bằng `ALTER … ADD COLUMN` như `storage/schema.go` đã làm; `IF NOT EXISTS` để hai process cùng mở an toàn |
| 2 | API run: `StartRun(taskID, agent, attempt) (runID)`, `EndRun(runID, liveness, note)`; `FinishTask` nhận `liveness` và làm máy trạng thái §5.3 trong **một transaction** (kể cả dead-letter và affinity) | `internal/coord/tasks.go`, `internal/coord/runs_task.go` (mới) | `FinishTask` cũ giữ chữ ký cho CLI `task done/fail`; thêm `FinishRun` là đường mới. Test: mọi nhánh của sơ đồ §5.3 |
| 3 | `SetCheckpoint(taskID, agent, attempts, note)` (fencing), `SetSessionRef`, `ClearSessionRef` | `internal/coord/tasks.go` | Checkpoint chỉ được ghi bởi người đang giữ lease (fencing bằng `attempts`) |
| 4 | Nới `assigned_to` khi agent chết: `RelaxDeadAssignments(staleAfter)` chạy trong vòng quét runner; xoá `session_ref` kèm | `internal/coord/tasks.go`, `internal/taskrunner/runner.go` | Test với `last_seen_at` giả quá hạn |
| 5 | Runner: mỗi claim → `StartRun`; nạp `checkpoint` + `session_ref` vào prompt/`sess`; `--resume` khi `session_ref` khớp agent+provider; phân loại liveness từ runtime (ctx deadline → `timed_out`, cancel → `canceled`, lệnh agent → `completed/advanced/blocked`, mặc định → heuristic `plan_only`) | `internal/taskrunner/runner.go` (`execute`, `taskPrompt`) | Session: `session.New(-1)`, `SetSessionID(ref.session_id)`, `SetProvider`, `SetAccount` để `credentials.Pick(pinned)` chọn đúng account |
| 6 | Runner **đăng ký** session vào `session.Manager` + `MarkRunning/NoteTool/MarkIdle`; phát `TurnEvent{Kind:"task"}` | `internal/taskrunner/runner.go`, `internal/bot/handler.go` (TurnEvent thêm `Kind`, `TaskID`), `cmd/bomclaw/main.go` (`Runs` gom cả task) | `gateway.RunInfo` thêm `Kind`, `TaskID`. Tray/dashboard đọc thêm field, không đổi hành vi cũ |
| 7 | Lệnh: `bomclaw task progress`, `task done` gọi `FinishRun(completed)`, `task fail` → `failed`, `task block --on human`, `task resume --more N`, `task show` in run + checkpoint | `cmd/bomclaw/coordcmd.go` | Vẫn ghi thẳng DB (nguyên tắc 4). `task progress` cần `--attempts` hoặc đọc từ hàng như `done` đang làm |
| 8 | Chuông HTTP: `POST /api/tasks/poke` sau `RequireAuthExceptLocal`; `PokeAgent` dùng HTTP; `agents.ws_addr` vẫn giữ, thêm suy ra `http://` từ nó | `internal/gateway/notify.go`, `cmd/bomclaw/main.go` (mount route), `internal/gateway/browserapi.go` (mẫu) | Test: gateway auth bật, poke từ loopback → 200; từ header `X-Forwarded-For` → 401 |
| 9 | Dead-letter hiển thị: TaskBoard bỏ nhánh "will be reclaimed" khi `state=failed`; hiện `fail_reason`, `continuations`, checkpoint trong drawer | `dashboard/src/admin/TaskBoard.tsx`, `types.ts` | |
| 10 | Prompt: `taskPrompt` mới theo §5.3; system prompt `config.yaml` sửa câu cấm theo §5.8 | `internal/taskrunner/runner.go`, `config.yaml`, và **config sống** `~/.bomclaw*/config.yaml` (đã tách khỏi repo — phải sửa tay, không copy đè) | |
| 11 | Docs: `docs/api-reference.md` (RPC/HTTP mới), `docs/credential-pools.md` (mục "continuation và affinity"), `CLAUDE.md` (không) | | |

### 10.2 Test bắt buộc

- `coord`: máy trạng thái §5.3 đủ nhánh; fencing của `SetCheckpoint`; dead-letter chuyển `failed` + `fail_reason`; `RelaxDeadAssignments` chỉ nới của agent quá `StaleAfter`; hai claimer đồng thời vẫn đúng một (test cũ giữ).
- `taskrunner`: run `timed_out` có checkpoint → task `submitted` lại với affinity, `continuations=1`; không checkpoint → `attempts+1`; `--resume` được truyền khi `session_ref` khớp, **không** truyền khi khác provider/account; hết `max_continuations` → `failed` + event + gọi hàm báo (stub).
- `gateway`: `/api/tasks/poke` loopback 200, forwarded 401; `StatusResult.Runs` chứa run task với `Kind:"task"`.
- Sống: chạy task giả 40 phút (mock CLI ngủ 16 phút mỗi run, ghi progress) trên gateway thật với `--port` riêng; quan sát `bomclaw status`, tray `☕`, TaskBoard; kill gateway giữa run 2 → khởi động lại → run 3 resume đúng session (kiểm tra `claude_session_id` không đổi).

### 10.3 Không làm trong P0

Lịch, heartbeat, cha–con, song song, tab Schedules, wake `comment`. Mỗi cái là một PR riêng ở P1/P2.

### 10.4 Rủi ro riêng P0

- **`--resume` với session của run bị `timed_out`**: CLI có thể để session ở trạng thái dở (tool call chưa trả). Đã thấy CLI chấp nhận resume sau kill; cần test thật với claude và codex. Fallback: nếu resume lỗi ở run kế → xoá `session_ref`, chạy với checkpoint.
- **Phân loại `plan_only` bằng heuristic**: sẽ có sai. Sai theo hướng "gọi thêm một lượt" rẻ hơn sai theo hướng "đánh dấu xong". Giới hạn `empty` = 2, `plan_only` tính vào 20.
- **Đăng ký session task vào Manager** có thể làm danh sách hội thoại dashboard hiện `task_*` — câu hỏi mở Q3 của `sessions-and-workspaces.md`. P0: lọc `chat_id < 0` khỏi `sessions.list`, chỉ hiện ở `Runs`/TaskBoard.

---

## 11. Rủi ro & câu hỏi mở

| Rủi ro | Mức | Xử lý |
|---|---|---|
| Chạy không giám sát nhiều giờ với `bypassPermissions` | **Cao** | Không hạ chuẩn `auto_claim: false`; scheduler/heartbeat mặc định tắt (nguyên tắc 12); mọi lần bật là dòng config có tên người; báo Telegram khi continuation cạn; `task cancel` từ Telegram/dashboard cắt được run đang chạy (ctx cancel) |
| Vòng lặp agent tự tạo việc | **Cao** | `MaxDepth` có hiệu lực thật (prompt biết depth), `max_open_children`, câu cấm mới trong prompt, continuation thay cho "tạo con để tiếp tục" |
| Đốt token bởi heartbeat/lịch hỏng | Trung bình | Scratch rỗng → bỏ qua; bậc thang backoff; tự tắt sau 10 lỗi; `command` cho việc không cần model |
| Hai gateway cùng vật chất hoá một lịch | Thấp | CAS trên `next_run_at`; test hai process |
| Resume session dở sau `timed_out` | Trung bình | §10.4; fallback checkpoint |
| Mac vẫn ngủ khi tray không chạy | Trung bình | P0 làm task *nhìn thấy* để tray giữ; **câu hỏi mở**: gateway tự giữ IOKit assertion khi có run? (tray là Aqua session, gateway là daemon — tray đúng chỗ hơn, nhưng phụ thuộc người đã cài tray) |
| Affinity biến "ai cũng claim được" thành "chỉ một người" | Trung bình | Chỉ áp cho continuation (có `session_ref`); nới khi agent chết; task mới vẫn tự do |
| Tin nhắn `agent_messages` đã có nhưng không ai đọc | Thấp | P2 wake `comment` + auto-read; cho tới đó vẫn như cũ |

**Câu hỏi mở**

1. **Rotate chat session 04:00 có nên thành một `schedule` hệ thống** thay cho `ShouldRotate` lười? Khi đó nó chạy thật lúc 04:00 và tránh được cắt giữa hội thoại (kiểm tra `Runs` trước khi cắt). Nghiêng về: có, ở P1, cùng lúc S3.
2. **Ai giữ Mac thức**: tray hay gateway? Nếu người không cài tray, task đêm sẽ ngủ. Gateway giữ assertion thì cần cgo trong binary chính. Nghiêng về: giữ ở tray P0; đo xem có ai chạy không tray không.
3. **Continuation của task `blocked --on human`**: người trả lời qua đâu? Telegram reply có `task_id`? Hay `bomclaw task answer --id T "…"`? Nghiêng về: lệnh, vì cả người và agent khác gõ được; Telegram là P2.
4. **`kind=command` chạy với quyền gì?** Cùng user gateway, cùng `bypass`. Có nên allowlist lệnh? Nghiêng về: không allowlist (cùng mức tin cậy với agent), nhưng log đầy đủ vào `schedule_runs` và không cho `command` gọi `bomclaw task new` (tránh scheduler → task → schedule vòng).
5. **Heartbeat đọc inbox không?** OpenClaw không. Paperclip có (bước 3). Nghiêng về: có, nhưng chỉ *liệt kê* tin chưa đọc vào scratch view, không tự trả lời.
6. **`task_runs` giữ bao lâu?** Cùng `trace_retention_days` (7) hay lâu hơn vì nó là audit? Nghiêng về: 30 ngày, purge cùng goroutine với trace.

---

## Tham khảo

- OpenClaw — đọc qua docs và deepwiki:
  - [docs.openclaw.ai/gateway/heartbeat](https://docs.openclaw.ai/gateway/heartbeat) — heartbeat là wake prompt; scratch; skip rules; `NO_REPLY`
  - [docs.openclaw.ai/automation/cron-jobs](https://docs.openclaw.ai/automation/cron-jobs) — `at|every|cron`, payload kinds, backoff 30s→60m, auto-disable 10, `skipMissedJobs`, TZ, DOM/DOW OR-trap
  - [docs.openclaw.ai/automation/tasks](https://docs.openclaw.ai/automation/tasks) — sổ cái task; *"automations decide when, tasks track what happened"*
  - [docs.openclaw.ai/tools/subagents](https://docs.openclaw.ai/tools/subagents) — `sessions_spawn`, `maxSpawnDepth 5`, resume sau restart, trạng thái từ runtime
  - [deepwiki openclaw/openclaw](https://deepwiki.com/openclaw/openclaw) §3.7, §3.8 (không phải §3.4.4)
  - Issues [#64103](https://github.com/openclaw/openclaw/issues/64103) (bẫy đặt tên trạng thái), [#15444](https://github.com/openclaw/openclaw/issues/15444) (keep-awake)
- Paperclip — [paperclipai/paperclip](https://github.com/paperclipai/paperclip):
  - `docs/guides/agent-developer/heartbeat-protocol.md` — 9 bước; "do the work this heartbeat"; `X-Paperclip-Run-Id`
  - [docs.paperclip.ing/guides/org/agents](https://docs.paperclip.ing/guides/org/agents/) — issue status ⟂ run liveness; `plan_only`/`empty_response` continuation; audit comment khi cạn
  - Issues [#7234](https://github.com/paperclipai/paperclip/issues/7234), [#3722](https://github.com/paperclipai/paperclip/issues/3722) — cạm bẫy retry/re-entrancy
- Nội bộ: [shared-agent-memory.md](./shared-agent-memory.md) (nền điều phối), [sessions-and-workspaces.md](./sessions-and-workspaces.md) (S3–S5), [../credential-pools.md](../credential-pools.md) (session sống trong config dir của account)
