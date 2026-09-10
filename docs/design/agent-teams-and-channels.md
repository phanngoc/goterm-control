# Design: Đội agent — provisioning, kênh trao đổi, và bàn giao dữ liệu cha–con

> Trạng thái: **đề xuất**, chưa code. Nguồn: yêu cầu người dùng 2026-09-10 (ba ý) + khảo sát lại `paperclipai/paperclip`.
> Liên quan: `scheduling-and-long-tasks.md` §5.2 (cha–con — **đã ship**, PR #105), `sessions-and-workspaces.md` §artifact, `shared-agent-memory.md`.
> Khác phạm vi: `agents-as-a-service.md` và `zeroclaw-docker-aaas.md` là multi-tenant bán cho khách. Doc này là **một người dùng, nhiều agent**.

---

## 1. Ba yêu cầu, đọc lại cho chính xác

**R1 — Admin provision được agent.** Tạo agent 3, 4, 5… từ trang admin. Lúc tạo chọn **harness** phía sau (Claude Code, Codex, …). Hôm nay agent 2 được dựng bằng tay: plist riêng, config riêng, port riêng.

**R2 — Vùng Messages thành một kênh kiểu Slack** (mượn mô hình, *không* tích hợp Slack). Một kênh chứa **nhiều** agent. Một agent mở được **thread** để trao đổi; **người dùng cùng tham gia** trong kênh/thread đó. Hôm nay Messages là DM 1–1 phẳng (`from_agent → to_agent`).

**R3 — Fan-out từ một request.** Agent nhận request của người dùng → tạo task (task = **goal**) → thường một agent làm; không đủ thì tạo **task con** giao agent khác cùng làm. Phần **trao đổi dữ liệu giữa task cha và task con** lấy Paperclip làm tham chiếu.

**Phi mục tiêu:** org chart, budget theo cent, approval gate, hire request (Paperclip có, ta không lấy — đã chốt ở `scheduling-and-long-tasks.md` §3.3).

---

## 2. Hiện trạng (verify 2026-09-10, main @ da0111c)

| Mảnh | Đã có | Thiếu |
|---|---|---|
| Đăng ký agent | `coord.agents(id, display_name, provider, model, ws_addr, workspace, scratch)` — `internal/coord/coord.go:94` | Không có gì tạo *ra* một agent. Agent tự `RegisterAgent` lúc khởi động. |
| Service | `internal/daemon/launchd.go:13` — `const launchdLabel = "com.bomclaw.gateway"` | **Label hardcode.** Agent 2 (`com.bomclaw2.gateway`) dựng tay ngoài code. |
| Harness | `provider: claude \| codex`, `config.Validate()` chặn provider lệch model — `internal/config/config.go:405-425` | Không chọn được lúc tạo, vì không có "lúc tạo". |
| Tài khoản | `accounts.pool[]` có `config_dir` riêng mỗi account — `internal/config/config.go:114-133` | Chưa nối vào provisioning. |
| Tin nhắn | `agent_messages(from_agent, to_agent, task_id, body, read_at)` — `internal/coord/coord.go:245` | **Đúng 1–1.** Không kênh, không thread, không thành viên là người. |
| Task cha–con | Đã ship: `CreateSubTask`, `WakeParents`, `MaxOpenChildren=8`, `blocked_on=children` — `internal/coord/children.go` | — |
| Bàn giao cha↔con | `childrenSummary()` — `internal/coord/children.go:164` | **Paste text, cắt cụt 1500 rune.** Đây là chỗ R3 nhắm tới. |

### 2.1 Bằng chứng rằng R3 là nhu cầu thật, không phải suy đoán

`~/goterm-shared/mailbox/` hôm nay có 5 file, trong đó `t_a4a876da-codex-error-cleanup.patch`. Trong ảnh admin người dùng gửi, agent nhắn nguyên văn: *"Patch ready at ~/goterm-shared/mailbox/t_a4a876da-…"*.

Hai điều đáng chú ý:
- `shared-agent-memory.md:34` từng ghi mailbox là *"Rỗng — chưa bao giờ dùng. Chỉ tồn tại trong system prompt"*. Giờ nó có file.
- `sessions-and-workspaces.md:265` đã **đoán trước**: mailbox tồn tại *"vì không có chỗ nào tốt hơn"* và sẽ thành thừa khi task có thư mục artifact riêng.

Tức là: agent đang **tự chế** một kênh bàn giao dữ liệu vì hệ thống không cho. R3 là hợp thức hoá thứ đã xảy ra, không phải thêm tính năng suy đoán.

---

## 3. Học từ Paperclip — riêng phần bàn giao dữ liệu

Đọc: `docs/guides/board-operator/delegation.md`, `docs/guides/agent-developer/task-workflow.md`, `packages/skills-catalog/.../task-planning/SKILL.md`, `packages/shared/src/types/artifact.ts`.

**Lấy:**

- **Ba mặt phẳng dữ liệu tách bạch, không cái nào là "paste vào prompt":**
  1. **Document** — markdown có *key* gắn vào issue (`plan` là key chuẩn). Sửa tại chỗ, có bản sửa đổi. Không phải comment.
  2. **Artifact** — sản phẩm công việc. `source: document | attachment | work_product`, và quan trọng nhất: `CompanyArtifactGroupBy = "none" | "task" | "parent_task"` (`artifact.ts:5`). **Cha nhìn được artifact của cả cây con** mà không cần con nhét text vào đâu cả.
  3. **Comment** — luồng hội thoại của issue. Cập nhật trạng thái *kèm* comment, chứ comment không mang sản phẩm.
- **Con phải tự đứng được.** Nguyên văn quy tắc chia việc: *"A child must be checkout-able by the owner from its title and description alone. Reviewers should not have to re-read the parent plan to understand a child."* Đây là ràng buộc lên `CreateSubTask`, không phải lời khuyên.
- **Một con, một chuyên môn; một con, một phán quyết nghiệm thu.** *"If a reviewer would say 'this is half done', split it."*
- **Kế hoạch trước, con sau.** *"Do not create implementation subtasks until the plan is accepted."*
- **`continuationPolicy: wake_assignee`** — người tạo nói rõ việc con xong thì có đánh thức mình không. Ta đang mặc định luôn đánh thức.

**Không lấy:** approval queue của board, hire request, budget 80% ngưỡng, org chart CEO→CTO→IC.

**Chỗ ta khác Paperclip và giữ nguyên:** cha **không quản** con. Cha tạo con rồi *ngủ* (`task block --on children`), được gọi lại khi con xong; peer nào claim cha lần sau cũng được (`children.go` §5.2). Paperclip có manager thật sự theo dõi report. Ta không.

---

## 4. Nguyên tắc

1. **Con trỏ, không phải bản sao.** Cha nhận *chỉ mục* artifact của con, đọc nội dung khi cần. Prompt không phải là phương tiện vận chuyển dữ liệu.
2. **Đánh thức phải trả giá.** Mention đánh thức ngay; tin nhắn kênh thường chỉ tăng unread. Nếu mọi tin nhắn đều wake, N agent trong một kênh là một đám cháy token.
3. **Một đường tạo agent.** Admin RPC và CLI gọi *cùng* một hàm provision. Trang admin không tự viết plist.
4. **coord.db là nơi duy nhất mọi agent nhìn thấy nhau.** Kênh phải ở đó, không ở session DB per-agent.
5. **Không thêm bảng khi một cột đủ.** Thread là "trả lời một tin nhắn", không cần bảng riêng (Slack cũng vậy).

---

## 5. Thiết kế

### 5.1 R1 — Provisioning agent

**Quy ước hiện hành, tổng quát hoá:**

| | Agent 1 | Agent 2 | Agent N (mới) |
|---|---|---|---|
| Label | `com.bomclaw.gateway` | `com.bomclaw2.gateway` | `com.bomclaw<N>.gateway` |
| Config | `~/.bomclaw/config.yaml` | `~/.bomclaw2/config.yaml` | `~/.bomclaw<N>/config.yaml` |
| Data | `~/.goterm/data` | `~/.goterm2/data` | `~/.goterm<N>/data` |
| Workspace | `~/goterm-workspace` | `~/goterm-workspace-2` | `~/goterm-workspace-<N>` |
| Port | 18789 | 18790 | port trống đầu tiên từ 18791 |
| Binary | `~/.bomclaw/bomclaw` | ← dùng chung | ← dùng chung |

Binary dùng chung là **cố ý**: chữ ký code và định danh TCC không đổi khi thêm agent, nên không có prompt Documents/Desktop mới. Đổi lại, deploy khởi động lại *tất cả* agent — đã đúng như hôm nay.

**Lệnh mới:**

```
bomclaw agent new  --id bomclaw3 --provider codex --model gpt-6-astra \
                   [--account <tên trong pool>] [--workspace DIR] [--port N] \
                   [--auto-claim] [--display-name "Reviewer"]
bomclaw agent pause <id>      # bootout, giữ config; task đang giữ được RelaxDeadAssignments thả
bomclaw agent resume <id>
bomclaw agent rm <id> --force # bootout + xoá plist; config/workspace giữ lại, phải xoá tay
```

`admin.agent.provision` / `.pause` / `.resume` / `.remove` là bọc mỏng quanh đúng các hàm này (nguyên tắc 3).

**Việc `agent new` làm, theo thứ tự:**
1. Validate id (`^[a-z][a-z0-9]{2,15}$`, chưa tồn tại trong `coord.agents`, chưa có plist).
2. Chọn port: quét `18791..18820`, lấy cái đầu không listen và không trùng `ws_addr` nào trong `coord.agents`.
3. Sinh `~/.bomclaw<N>/config.yaml` từ template: `agent.id`, `agent.name`, `agent.ws_addr`, `provider`, `models.default`, `coord.path` (**trỏ chung** `~/.goterm-shared/data/coord.db`), `tasks.auto_claim`, `gateway.auth` kế thừa agent 1.
4. `config.Validate()` — chặn ngay provider/model lệch, trước khi ghi plist.
5. Sinh plist qua `buildPlist()` với label tham số hoá; `bootstrap gui/501`.
6. Chờ agent tự `RegisterAgent` (poll `coord.agents` 30s). Không thấy → báo lỗi kèm 20 dòng log cuối, và **rollback plist**.

**Đổi code tối thiểu:** `internal/daemon/launchd.go` bỏ `const launchdLabel`, nhận label + plist path qua `newLaunchdService(agentID)`. Agent 1 giữ nguyên label cũ (`agentID == "bomclaw"` → `com.bomclaw.gateway`) để không phá service đang chạy.

**Cổng chặn về tài khoản — phải nói thẳng.** Provider `claude` mà không chọn `--account` thì agent mới **dùng chung OAuth của agent 1**. Nó chạy được (agent 1 và 2 từng như vậy), nhưng quota là một. Một account riêng cần `claude login` **tương tác một lần** trong `config_dir` của nó — provisioning không tự làm được, vì credential nằm trong Keychain khoá theo config dir. Nên:
- `agent new --provider claude` không có `--account` → cảnh báo rõ "dùng chung quota với `<id>`", yêu cầu `--yes`.
- `--account foo` mà `accounts.pool[foo]` chưa đăng nhập → từ chối, in ra đúng lệnh người dùng cần gõ.
- Provider `codex` không vướng: agent 2 đã chạy độc lập bằng `~/.codex`.

**Cap:** `agents.max` mặc định 5. Không phải giới hạn kỹ thuật — là chặn tai nạn, vì mỗi agent là một tiến trình CLI thật ăn quota thật.

### 5.2 R2 — Vùng Messages thành một cái "kênh"

**Không tích hợp Slack.** Không app, không webhook, không workspace Slack. Mượn *mô hình* của nó, vì đó là mô hình đã được chứng minh cho "nhiều bên nói chuyện trong một chỗ chung" — còn vùng Messages hôm nay thì không có "chỗ" nào cả.

**Tư tưởng lấy, và mỗi cái sửa đúng điều gì:**

| Tư tưởng Slack | Hôm nay hỏng ở đâu | Thành cái gì |
|---|---|---|
| **Kênh là một *nơi*, không phải một cặp người** | `agent_messages(from_agent → to_agent)` ép mọi câu phải có đúng một người nhận; không tồn tại chỗ nào sống độc lập với hai cái tên | `channels` — địa chỉ là chủ đề/việc, không phải người |
| **Mặc định ai cũng thấy; DM mới là ngoại lệ** | Muốn agent thứ ba biết thì phải gửi lại lần nữa cho nó | Agent chưa được nhắc vẫn đọc được → tự nhảy vào giúp được |
| **Thread tách nhánh sâu khỏi dòng chính** | Stream phẳng: một cuộc đào sâu chiếm hết màn hình (đúng như ảnh admin) | `thread_root`; và thread là **đơn vị gắn được vào một task** |
| **Mention là hợp đồng chú ý** | `to_agent` vừa là địa chỉ vừa là chuông — không tách được "cho biết" khỏi "làm đi" | `@` → đánh thức; không `@` → chỉ unread |
| **Lịch sử thuộc về kênh, không thuộc phiên** | Agent vừa provision (R1) không có đường đọc ngược | Agent 5 vào kênh đọc được cả quá khứ của việc |
| **Thành viên là danh sách tường minh** | Không có khái niệm "ai đang ở trong việc này" | `channel_members`, gồm cả người dùng |

**Một tư tưởng Slack *không* chuyển được — và đây là chỗ dễ sai nhất.** Slack thiết kế cho người **tự kéo**: mở app, đọc, badge chỉ là gợi ý. Agent không kéo, agent **được đánh thức**. Nên "unread" đứng một mình là vô nghĩa: tin không ai wake là tin không ai đọc, vĩnh viễn. Vì vậy mỗi mức hiển thị phải đi kèm một mức đánh thức (bảng bên dưới) — không bê nguyên notification model của Slack.

**Không lấy:** reaction, huddle, presence/status, notification preferences, workspace/org layer, app directory. Một người dùng, một nơi làm việc — không cái nào có việc để làm.

Bốn bảng, không hơn:

```sql
channels(id, name, kind, purpose, created_by, created_at, archived_at)
    -- kind: 'channel' | 'dm'
channel_members(channel_id, member_kind, member_id, joined_at, last_read_at)
    -- member_kind: 'agent' | 'user'
channel_messages(id, channel_id, thread_root, author_kind, author_id, body, task_id, created_at)
    -- thread_root = '' ⇒ tin cấp kênh; = id tin gốc ⇒ trả lời trong thread
channel_mentions(message_id, member_kind, member_id, read_at)
```

**Thread không có bảng riêng.** Một thread *là* các tin có cùng `thread_root`. Tiêu đề thread = 80 ký tự đầu của tin gốc. Ràng buộc task của thread = `task_id` **trên tin gốc**. Đây đúng mô hình Slack, và tiết kiệm một bảng + một vòng đời trạng thái không ai cần.

**Quy tắc đánh thức** (áp trực tiếp `now` vs `next-heartbeat` của OpenClaw, `scheduling-and-long-tasks.md` §3.1):

| Sự kiện | Với agent được nhắc | Với agent khác trong kênh |
|---|---|---|
| `@agent` trong tin | **poke ngay** | không |
| Tin trong thread agent đã từng post | next-heartbeat | next-heartbeat |
| Tin cấp kênh, không mention | không | không (chỉ tăng unread) |

Không có quy tắc "mọi tin đều wake". Với 5 agent trong một kênh, quy tắc đó tự nó là một vòng lặp.

**Người dùng là thành viên hạng nhất.** `member_kind='user'`. Tin của người dùng trong thread có `@agent` → poke đúng agent đó. Đây là cách R2 nối vào R3: người dùng đặt yêu cầu **trong thread**, agent trả lời **trong thread**, và task sinh ra từ thread đó giữ `task_id` trên tin gốc — nên nhìn thread là thấy cả tiến độ.

**Di trú `agent_messages`:** mỗi cặp `{a,b}` → một channel `kind='dm'`, `name = "a↔b"` (thứ tự chuẩn hoá). `from_agent` → author, `to_agent` → một dòng `channel_mentions`, `read_at` giữ nguyên, `created_at` giữ nguyên. Bảng cũ giữ lại một version rồi mới bỏ.

**UI:** tab Messages hiện tại (`dashboard/src/admin/AdminView.tsx:98`, `MessageStream`) thành sidebar kênh + pane tin + pane thread. Composer giữ dropdown "gửi với tư cách" đang có trong ảnh, thêm lựa chọn `me (user)`.

### 5.3 R3 — Artifact: bàn giao dữ liệu cha ↔ con

```sql
artifacts(id, context_id, task_id, kind, title, content_type, path, preview, bytes,
          created_by, created_at)
    -- kind: 'document' | 'patch' | 'file' | 'link' | 'result'
artifact_links(artifact_id, task_id, role)   -- role: 'input' | 'output'
```

Nội dung **trên đĩa**, không trong SQLite: `~/goterm-shared/artifacts/<context_id>/<task_id>/<tên>`. `preview` là 400 rune đầu, đủ để hiện trong index và trong prompt. `~/goterm-shared/mailbox/` bị thay thế đúng như `sessions-and-workspaces.md:265` đã dự tính.

`artifact_links` tồn tại để cha **đưa artifact xuống làm input của con** mà không copy: cùng một artifact, hai dòng link, `role` khác nhau.

**Lệnh:**
```
bomclaw artifact put  --task T --kind patch --title "codex error cleanup" --file p.diff
bomclaw artifact list --task T [--tree]    # --tree = cả cây con, nhóm theo task (Paperclip group_by=parent_task)
bomclaw artifact get  <id>                 # in nội dung đầy đủ ra stdout
```

**Đổi `childrenSummary()`** — đây là thay đổi cốt lõi của R3. Hôm nay:

```
2. [completed] Fix scanner (t_ab12, by bomclaw2)
   <1500 rune đầu của result, cắt cụt bằng dấu …>
```

Sau:

```
2. [completed] Fix scanner (t_ab12, by bomclaw2)
   → Sửa join stderr trên turn.failed; 5 ca hồi quy fake-CLI pass.     ← result, cắt ở 400
   artifacts:
     a_7f3c  patch     codex-error-cleanup.patch   (14.2 KB)
     a_9d01  document  test-notes.md               (1.1 KB)
   Đọc đầy đủ: bomclaw artifact get a_7f3c | bomclaw task show t_ab12
```

Cha nhận **chỉ mục**, không nhận nội dung. Nội dung không bao giờ bị cắt cụt nữa — nó nằm nguyên trên đĩa, cha đọc cái nó cần. Tám con × 14 KB patch không còn là vấn đề prompt.

**Ràng buộc mới lên `CreateSubTask`** (quy tắc Paperclip "con tự đứng được"):
- `Body` của con bắt buộc, tối thiểu ~200 ký tự, và **không được** chỉ tham chiếu cha ("làm tiếp phần 2 của cha").
- Thêm `NewTask.Acceptance string` — tiêu chí nghiệm thu, bắt buộc với `KindSub`. Không có tiêu chí thì không ai biết con xong hay chưa, và `polish`/`cleanup` không bao giờ đóng.
- Thêm `NewTask.Inputs []string` — artifact id cha trao xuống, ghi vào `artifact_links(role='input')`.

**Thêm `tasks.wake_parent`** (mặc định `true`) — bản `continuationPolicy: wake_assignee` của Paperclip. Con "làm cho biết" không cần dựng cha dậy.

### 5.4 Ba mảnh nối vào nhau

```
Người dùng post trong #build:  "@bomclaw dựng landing page có form đăng ký"
   │
   ├─ mention → poke bomclaw ngay (§5.2)
   │
   ├─ bomclaw mở thread trên chính tin đó, tạo task gốc G, ghi task_id=G lên tin gốc
   │
   ├─ bomclaw viết plan → artifact kind='document' key 'plan' gắn G   (Paperclip: plan trước, con sau)
   │  và post link vào thread
   │
   ├─ tạo 2 con:  c1 "Dựng form" → @bomclaw3 (codex)
   │              c2 "Viết copy"  → @bomclaw4 (claude)
   │  mỗi con: body tự đứng được + acceptance + inputs=[plan]
   │
   ├─ bomclaw: task block --on children  → ngủ, thả lease
   │
   ├─ c1, c2 xong, mỗi con artifact put sản phẩm của mình
   │
   └─ WakeParents dựng G dậy với chỉ mục artifact của c1+c2 (§5.3)
      → bomclaw gom, post kết quả vào **đúng thread cũ**, người dùng đọc tại chỗ
```

---

## 6. Lộ trình — và tại sao không theo thứ tự người dùng nêu

Đề nghị đảo thành **R3 → R1 → R2**:

| | Việc | Vì sao ở đây |
|---|---|---|
| **P4a** | R3 artifacts + sửa `childrenSummary` + ràng buộc `CreateSubTask` | Nhỏ nhất, sửa một cái đau **đang xảy ra** (mailbox tay, cắt cụt 1500 rune). Không đụng schema của mảng nào khác. Cha–con vừa ship xong nên đây là lúc đúng. |
| **P4b** | R1 provisioning + tham số hoá launchd label | Điều kiện cần của R2: hai agent thì chưa cần Slack. Năm agent thì cần. |
| **P4c** | R2 kênh/thread + di trú `agent_messages` + UI | Lớn nhất, và giá trị của nó **tỉ lệ với số agent** — nên làm sau khi có nhiều agent thật. |

Làm R2 trước sẽ là dựng một Slack cho hai người dùng.

---

## 6b. Ghi chú triển khai (khác/thêm so với §5)

Đã ship trong một PR (nhánh `feat/p4-teams-channels-artifacts`), coord schema **v6**. Những chỗ lệch với thiết kế ở trên, và lý do:

**R1 thu hẹp còn "3 agent cố định".** Người dùng chốt số agent là 3, nên **không** làm `bomclaw agent new`, không làm bộ cấp phát port, không `admin.agent.provision`, không `agents.max`. Thay vào đó chỉ gỡ đúng vật cản: `daemon.Resolve(agentID)` thay cho label hardcode, và `--agent` trên mọi lệnh `bomclaw gateway …`. Dựng agent 3 giờ là `gateway install --agent bomclaw3 --port 18791 --config …`; quy trình đầy đủ ở `docs/adding-an-agent.md`. Khi nào cần agent thứ N tự động thì lớp provisioning mới đáng viết.

**Agent 3 chạy Claude, dùng chung tài khoản agent 1** (người dùng chọn, biết là chung quota). Không đụng `accounts.pool`; tài liệu ghi rõ cách tách quota khi cần và vì sao không tự động hoá được (`claude login` tương tác, credential khoá theo config dir trong Keychain).

**Bỏ `tasks.wake_parent`.** Thiết kế §5.3 đề xuất cột này (bản `continuationPolicy` của Paperclip). Khi viết mới thấy nó tạo một cái bẫy thật: nếu mọi con của một cha đều `wake_parent=false` thì cha kẹt `blocked` vĩnh viễn. Với 3 agent chưa có nhu cầu nào cần nó — YAGNI, để lại khi có ca dùng thật.

**`Acceptance` là tuỳ chọn, không bắt buộc.** §5.3 nói bắt buộc với `KindSub`. Thực tế hard-fail giữa lượt của agent vì thiếu tiêu chí nghiệm thu đắt hơn giá trị nó mang lại. Cái **bắt buộc** là `Body` ≥ `MinSubTaskBody` (40 rune) — đó mới là thứ thực thi quy tắc "con tự đứng được" của Paperclip. `Acceptance` được lưu, hiện trong `task show` và đi kèm kết quả con khi cha thức dậy.

**Thêm `NewChannelMessage.Notify`** (không có trong §5.2). Chỉ parse `@` trong body là không đủ: `SendMessage` (DM) địa chỉ hoá bằng cấu trúc chứ không bằng văn xuôi, và agent nhận có thể **chưa đăng ký** trong bảng `agents` — parse sẽ bỏ qua và tin nhắn rơi vào hư không. `Notify` là địa chỉ tường minh; `@` trong body vẫn hoạt động song song.

**`#general` tạo sẵn, agent tự vào khi khởi động.** `RegisterAgent` gọi `JoinChannel` — một agent phải được mời vào căn phòng duy nhất mới nói chuyện được là một agent không ai nhớ mời.

**`agent_messages` được giữ lại.** Dữ liệu đã di trú sang `channel_messages`/`channel_mentions` (giữ nguyên id cũ nên mọi tham chiếu còn phân giải được), bảng cũ để nguyên một version phòng khi phải soi lại. `SendMessage`/`Inbox`/`RecentMessages`/`MarkRead` viết lại trên nền channel nên `bomclaw msg`, `bomclaw inbox` và RPC `messages.*` không đổi hành vi với agent đang chạy. Một thay đổi chữ ký: `MarkRead(agentID, ids)` — trước đây không có agent, nên một agent đọc sẽ xoá luôn mention của agent khác được nhắc cùng dòng.

**Artifact có thêm cột `url`** cho `kind=link`, và ghi bytes ra `~/goterm-shared/artifacts/<context>/<task>/`. Tên file được khử độc: `../../.ssh/authorized_keys` sập về một tên lá (có test). Trùng tên thì thêm hậu tố thay vì đè.

**RPC mới:** `channels.list|messages|post|create|read`, `artifacts.list|get`. Dashboard đọc bằng danh tính người dùng (`owner`) chứ không phải danh tính agent của gateway — đó là lý do trước đây lời của người dùng không hề có trong lịch sử agent.

**Chưa làm:** forward kênh sang Telegram; TTL dọn artifact; `max_tasks_per_context`. Vẫn là câu hỏi mở bên dưới.

---

## 7. Rủi ro & câu hỏi mở

1. **Hai khái niệm "conversation".** `storage` (session DB, per-agent) đã có `conversations` + `channel_bindings` nối Telegram↔web (schema v6). Doc này thêm `channels` ở **coord.db**. Chúng *phải* tách — kênh cần mọi agent nhìn thấy, session DB thì không — nhưng hai cái tên giống nhau sẽ gây nhầm. **Cần chốt tên trước khi code.**
2. **Danh tính người dùng xuyên DB.** `users` nằm ở session DB per-agent; `channel_members.member_id` cho `kind='user'` cần một id **dùng chung**. Đơn giản nhất: một hằng `user:owner` (hệ single-account, PR #63 đã bỏ role). Đủ chưa?
3. **Telegram có thấy kênh không?** Nếu một tin trong `#build` phải bay sang Telegram thì cần binding kênh→chat. Chưa thiết kế. Đề xuất P4c không làm.
4. **Quota.** 5 agent Claude cùng một OAuth = một quota. `accounts.pool` + `cooldown_minutes` giảm đau chứ không tạo thêm hạn mức. Có sẵn sàng trả tiền nhiều tài khoản không?
5. **Cây con nở.** `MaxOpenChildren=8` × `MaxDepth=5` = 32768 task trên lý thuyết. Con giao cho agent khác (R3) làm việc này *dễ hơn* trước. Cần thêm cap theo cây: `max_tasks_per_context`, đề xuất 50.
6. **Artifact rác.** Không có TTL thì `~/goterm-shared/artifacts/` phình mãi. Đề xuất theo `coord.trace_retention_days`: xoá artifact của context đã terminal quá N ngày, `kind='document'` được giữ.
7. **Xoá agent giữa chừng.** `agent rm` khi nó đang giữ task: `RelaxDeadAssignments` sẽ thả sau khi hết lease (đã có), nhưng `SessionRef` ghim vào agent đó thì **mất** — task tiếp tục được nhưng không `--resume` được. Chấp nhận, và phải nói rõ trong xác nhận xoá.

---

## Tham khảo

- `paperclipai/paperclip` @ 9c1f8e788 — `docs/guides/board-operator/delegation.md`, `docs/guides/agent-developer/task-workflow.md`, `packages/shared/src/types/artifact.ts`, `packages/skills-catalog/catalog/bundled/paperclip-operations/task-planning/SKILL.md`
- `scheduling-and-long-tasks.md` §3.1 (OpenClaw wake), §3.2 (Paperclip hai mặt phẳng trạng thái), §5.2 (cha–con)
- `sessions-and-workspaces.md` §265 (artifact thay mailbox)
