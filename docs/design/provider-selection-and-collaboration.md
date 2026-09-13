# Research: Chọn backend cho agent, và giữ nguyên đường Telegram khi mở thêm kênh

> Trạng thái: **RESEARCH** — khảo sát mã nguồn thật, chưa code. Mọi dòng "hiện trạng" dưới đây đều kèm file:dòng đã đọc ngày 2026-09-12, main @ `be68950`.
> Câu hỏi gốc: (1) Telegram nhắn tin điều khiển máy **như cũ**, (2) agent vào kênh tự thảo luận + giao việc, (3) **trace log đầy đủ**, (4) agent chọn được `claude` / `claude-tam` / `codex`, tương lai `openclaw` + `hermes`.
> Liên quan: `agent-teams-and-channels.md` (kênh), `scheduling-and-long-tasks.md` (task, lịch, trace).

---

## 1. Kết luận ngắn

Ba trong bốn câu hỏi **không cần kiến trúc mới** — đường nối đã có sẵn, chỉ bị một chỗ thắt:

| Câu hỏi | Trạng thái thật |
|---|---|
| Telegram điều khiển máy như cũ | Không có gì phải làm để *giữ*; cần một **test đặc tả** để không ai làm hỏng về sau |
| Agent thảo luận trong kênh | Phòng đã có (PR #106); hop cuối mention→lượt chat đang ở PR #119 |
| `claude-tam` | **Không phải provider mới** — đã chốt: tài khoản Claude của Tâm. `accounts.pool` hôm nay đã làm được. Xem §5 |
| `openclaw`, `hermes` | Cần `chat.Client` mới. Chi phí nằm ở **chỗ thắt** §3, không nằm ở giao diện |
| Trace đầy đủ | Có ba lỗ thật, §6 |

---

## 2. Hiện trạng (đã verify)

**Đường nối provider đã tồn tại và sạch.** `internal/chat/chat.go:28`:

```go
type Client interface {
    SendMessage(ctx, sess, modelID, userText, memoryContext string, cb StreamCallbacks) error
    Name() string   // khoá provider ghi vào session: "claude" | "codex"
}
```

`claude.Client` và `codex.Client` cùng thoả. Turn engine (`bot.Handler`) chỉ biết interface này.

**Chỗ thắt là chỗ chọn, không phải chỗ định nghĩa.** `internal/bot/bot.go:84`:

```go
func NewChatClientWithPool(cfg, executor, pool) chat.Client {
    if cfg.Provider == config.ProviderCodex { ... return codex }
    return claude   // mọi thứ không phải codex đều thành claude
}
```

Bốn hệ quả đo được:

1. **`cfg.Provider` là một khoá cho cả agent** (`config.go:16`). Không có chỗ nào chọn theo session, theo task, hay theo lượt.
2. **`Validate()` từ chối mọi giá trị khác** `claude`/`codex` (`config.go:445`) — một provider thứ ba không khởi động được, chứ không phải chạy sai.
3. **Pool chỉ dựng cho đúng một provider**: `NewPool(cfg.Provider, accts, cooldown)` và account khác provider bị **bỏ im lặng** (`credentials/pool.go:136`).
4. **Fallback im lặng**: `else → claude`. Gõ nhầm `provider: cladue` thì `Validate` bắt được; nhưng nếu ai nới `Validate` mà quên `NewChatClientWithPool` thì provider mới sẽ **chạy bằng claude** mà không báo gì.

**Model đã biết mình thuộc protocol nào.** `models/catalog.go:7` có sẵn `claude-cli`, `codex-cli`, và hai giá trị để dành `anthropic`, `openai`. `Validate()` đã đối chiếu chéo provider ↔ `model.API` (`config.go:441`). Tức **model đã đủ thông tin để quyết định provider** — hôm nay thông tin đó chỉ dùng để báo lỗi, không dùng để chọn.

**Session đã mang danh tính backend.** `SetProvider`/`SetAccount` (`session.go:123,142`), và resume bị chặn khi khác CLI: `if ref.Provider == r.llm.Name()` (`taskrunner/runner.go:348`). Nghĩa là đổi provider **không** làm hỏng session cũ — nó chỉ không resume được, và code đã lường.

---

## 3. Đề xuất: registry theo `ModelAPI`, thay cho if/else theo `cfg.Provider`

Một hàm `Resolve(api models.ModelAPI) (chat.Client, bool)` với bảng đăng ký, thay cho if/else. `cfg.Provider` vẫn là mặc định của agent, nhưng thôi là *thứ duy nhất* được phép quyết.

Vì sao theo `ModelAPI` chứ không theo tên provider: bảng model **đã** khai báo `api`, `Validate` **đã** đối chiếu nó, và chọn model là thao tác người dùng đã làm hằng ngày (`/model`, `--model`). Chọn theo API thì "đổi backend" và "đổi model" là một hành động, không phải hai chỗ cấu hình phải khớp tay.

Việc phải làm, theo đúng thứ tự:

1. `chat.Register(api, factory)` + `chat.Resolve(api)`; `NewChatClientWithPool` gọi `Resolve`, **không còn nhánh else im lặng** — không tìm thấy thì lỗi, nói rõ api nào.
2. `Validate()` hỏi registry thay vì `switch` cứng hai giá trị.
3. **Pool theo từng provider**: `map[string]*Pool`, dựng từ `accounts.pool` gom theo `provider`. Hôm nay account của backend kia bị bỏ im lặng — với ba backend thì đó là một cái bẫy.
4. `claude` và `codex` tự đăng ký trong `init()`; thêm `openclaw`/`hermes` sau này là một file mới + một dòng đăng ký, không sửa `bot`.

---

## 4. Telegram: đảm bảo bằng test, không bằng lời hứa

Không có gì trong §3 đụng vào đường Telegram — `bot.Handler.RunTurn` là đường chung của Telegram, dashboard, kênh và task từ PR #75. Nhưng "không đụng" là một lời hứa, và lời hứa cần một cái chốt:

- `TestRunTurnIsTheTelegramPath` (`bot/runturn_test.go:92`) đã chốt rằng dashboard và Telegram đi chung một đường.
- **Cần thêm**: một test đặc tả rằng với config hiện tại (`provider: claude`, model claude-cli) registry trả về đúng `claude.Client` — để một lần refactor registry không lặng lẽ đổi backend của người đang chạy.
- Và một test rằng `Resolve` với api lạ **báo lỗi**, không rơi về claude.

---

## 5. `claude-tam`: một account, không phải một provider

**Đã chốt 2026-09-13: `claude-tam` là tài khoản Claude của Tâm.** Cùng CLI, login khác.

Nghĩa là **hôm nay đã làm được**: `accounts.pool` với `config_dir` riêng (`CLAUDE_CONFIG_DIR`) cho mỗi login (`config.go:116-135`). Việc còn thiếu chỉ là **chọn được account** thay vì để pool tự xoay — và hai mảnh của việc đó đã nằm sẵn trong code: `sess.SetAccount` (`session.go:142`) và `Pool.Pick(pinned)` (`pool.go:185`) đã nhận tham số ghim.

Nên "chọn claude-tam" là **một form + một tham số**, không phải cả §3. Đó là V2.

Một hệ quả phải nói rõ với người dùng: xoay account là **theo session, không theo lượt** — cả hai CLI giữ session store *bên trong* thư mục credential, nên đổi account giữa cuộc là mất cuộc nói chuyện (`config.go:109-115`). Vậy "đổi sang tài khoản Tâm" nghĩa là **mở phiên mới**, không phải chuyển giữa chừng.

`openclaw` và `hermes` thì là chuyện khác: CLI khác, cần `chat.Client` mới như §3.

---

## 6. Trace: ba lỗ thật

Hôm nay có ba gốc trace: `turn` (`bot/handler.go:654`), `task` (`taskrunner/runner.go:356`), `gateway.send` (`gateway/methods.go:560,815`). Mỗi gốc mang `SessionID`, `ChatID`, `Model`, `Provider`, `Tags`; `AgentID` do recorder gắn (`trace/trace.go:157`).

| Lỗ | Hệ quả | Đề xuất |
|---|---|---|
| **Lịch kiểu `command` không có span nào.** `internal/scheduler` không gọi `StartTrace` lần nào | Một job shell chạy 03:00 hỏng thì chỉ còn `schedule_runs.output` cắt 8KB; không có waterfall, không lọc được ở tab Traces | Gốc `schedule.command`, `RunType` mới `RunTypeCommand` |
| **Lượt kênh không trỏ ngược về tin nhắn.** Trace có `session_id = ch_<thread>` nhưng không có `channel_id`/`message_id` | Từ một dòng trong kênh không mở thẳng được trace của nó | `Meta.Tags` (đã là JSON array, không cần schema mới) |
| **Lệnh `bomclaw` agent tự gõ** chỉ hiện dưới dạng tool span `Bash` | Không thấy được "agent này đã tạo task kia" trong trace | Chấp nhận — `task_events` đã ghi; ghi hai nơi thì hai nơi sẽ lệch |

---

## 7. Lộ trình

| | Việc | Vì sao ở đây |
|---|---|---|
| **V1** | Registry `ModelAPI → chat.Client`; pool theo provider; bỏ nhánh else im lặng; test đặc tả §4 | Điều kiện cần của mọi backend thứ ba. Không đổi hành vi nào đang chạy — đó là điểm của test đặc tả |
| **V2** | Chọn account (`claude-tam`) từ dashboard/CLI, ghim vào session | Nhỏ, và nếu §5(a) đúng thì đây **là** toàn bộ yêu cầu "chọn claude-tam" |
| **V3** | Trace: gốc `schedule.command` + tag kênh | Độc lập với V1/V2, làm lúc nào cũng được |
| **V4** | `openclaw` / `hermes` là `chat.Client` | Sau V1, mỗi cái là một file. Trước V1 thì mỗi cái là một lần sửa `bot` |

---

## 8. Rủi ro & câu hỏi mở

1. ~~`claude-tam` là account hay CLI?~~ **Đã chốt: account của Tâm.** V2 là một form + một tham số ghim.
2. **`hermes` là gì?** (tên đúng là Hermes agent, không phải "helmet".) Chưa có dữ liệu nào trong repo hay trên máy này. Trước khi ước lượng V4 cần biết: nó chạy bằng **CLI subprocess** (như claude/codex, hợp với `chat.Client` ngay) hay bằng **HTTP API** (thì cần thêm một `ModelAPI` và một client không-subprocess — `models.APIAnthropic`/`APIOpenAI` đã để dành chỗ cho hình dạng đó)? Và nó có khái niệm **session resume** không — nếu không thì `sess.SetSessionID` không có gì để ghi, và mỗi lượt là một lượt mới.
3. **Quota dùng chung.** Ba agent Claude cùng một OAuth vẫn là một quota — `accounts.pool` chia đau chứ không tạo thêm hạn mức. Chọn được account không giải quyết việc này.
4. **Session không resume được sau khi đổi backend.** Đã lường trong code (`ref.Provider == llm.Name()`), nhưng với người dùng thì nó hiện ra như "agent quên mất cuộc nói chuyện". Có nên báo một dòng khi điều đó xảy ra?
