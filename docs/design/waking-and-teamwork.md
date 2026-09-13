# Research: Đánh thức agent, và để chúng tự chia việc với nhau

> Trạng thái: **RESEARCH** — khảo sát mã nguồn và hệ đang chạy, chưa code. Mọi dòng "hiện trạng" đều kèm `file:dòng` hoặc lệnh đã chạy ngày 2026-09-13.
> Câu hỏi gốc: agent cần **tự chọn lẫn nhau** để hoàn thành việc người dùng trao đổi — có thể mỗi con một khía cạnh, hoặc một con xem xét rồi chia việc. Học cách một nhóm người làm việc.
> Liên quan: `agent-teams-and-channels.md` (kênh, cha–con), `scheduling-and-long-tasks.md` (task, lease, wake).

---

## 1. Sự cố mở đầu, vì nó chứa gần hết vấn đề

Hôm nay, trong một thread, người dùng chọn `to: bomclaw3` và gõ "reply đi". Ba agent cùng thức. Rồi:

- `bomclaw3`: *"Tạo task tổng hợp kinh tế VN 3 tháng qua để theo dõi trong thread."*
- `bomclaw2`: *"Đã mở task t_6a7475ad… cho việc này."*
- `bomclaw2`: *"Mình sẽ mở task… rồi trả kết quả vào thread này."*

Hai con cùng nhận một việc; một con tuyên bố sẽ làm thứ con kia vừa làm xong. Không con nào sai — **không con nào biết con kia tồn tại trong khoảnh khắc đó**.

Nửa nguyên nhân đã vá (địa chỉ tường minh giờ thắng luật theo-thread). Nửa còn lại là chủ đề của doc này: **khi nhiều agent cùng thức, không có gì điều phối chúng**.

---

## 2. Hiện trạng: cái gì đánh thức một agent

| Nguồn | Đường đi | File |
|---|---|---|
| `@tên` trong kênh, hoặc chọn từ dropdown | → `channel_mentions` → chuông → `MentionWatcher` → **một lượt chat** | `gateway/mentions.go` |
| Người trả lời trong thread **không nêu tên ai** | → mọi agent đã nói trong thread | `coord/channels.go` |
| Task vào hàng đợi | `NotifyTaskCreated` → vòng claim | `coord/tasks.go` |
| Lịch tới giờ | → task `kind=scheduled` | `internal/scheduler` |
| Heartbeat | → task `kind=heartbeat` | `coord/schedules.go` |
| `msg --task T` | đọc ở đầu run kế tiếp của task đó | `taskrunner/runner.go:575` |

Sáu nguồn, và **cả sáu đều là "gọi đích danh"**. Không nguồn nào trả lời được câu *"việc này nên giao cho ai?"* — người gọi phải biết trước.

### 2.1 Agent biết gì về đồng đội

Cơ sở dữ liệu biết đủ:

```
ID        STATUS  PROVIDER  MODEL
bomclaw   online  claude    claude-opus-5
bomclaw2  online  codex     gpt-6-astra
bomclaw3  online  opencode  opencode/muse-spark-1.3-contributor-free
```

Nhưng **prompt** thì không. Phần "Your peer agent" là một đoạn văn **viết tay trong từng file config**, và nó **đã lệch thực tế**:

- `grep -c bomclaw3` trong config agent 1 và agent 2 → **0**. Hai con này không biết agent 3 tồn tại, trừ khi tự chạy `bomclaw agents`.
- Prompt viết "Your peer is `bomclaw2`" — **số ít**, từ thời còn hai agent.
- Đường dẫn log peer ghi `~/.bomclaw2/gateway.log`, nhưng PR #107 đã chuyển sang `~/.goterm2/logs/`.

Đây không phải lỗi cẩu thả. Đây là thứ **bắt buộc** xảy ra khi hiểu biết về đồng đội là văn xuôi chép tay: thêm một agent là phải sửa tay ba file, và không có gì bắt lỗi khi quên.

`coord.agents` có `id, display_name, provider, model, ws_addr, workspace, online` (`coord/agents.go:13`) — **không có** trường nào nói agent này *giỏi gì*, *đắt bao nhiêu*, *đang bận gì*. `agents.scratch` có tồn tại nhưng là sổ tay của heartbeat (`coord/scratch.go:11`).

### 2.2 Cái gì đã chống trùng, và cái gì chưa

**Đã có:** `ClaimTask` là một câu UPDATE…RETURNING có CAS (`coord/tasks.go:250`) — hai agent không bao giờ *chạy* cùng một task. Lease + fencing token lo phần còn lại.

**Chưa có:** không gì chống **trùng lúc tạo**. Ba agent cùng thức trên một câu hỏi sẽ tạo ra ba task cho cùng một việc, và mỗi task sau đó được claim độc quyền một cách hoàn hảo. Khoá đặt đúng chỗ nhưng sai tầng: nó bảo vệ *việc thực thi*, trong khi thứ bị trùng là *quyết định nhận việc*.

**Và:** một agent vừa bị đánh thức **không biết ai khác cũng bị đánh thức**. Prompt trong `mentions.go` không nhắc tới điều đó (`grep -c "also named"` → 0). Nó trả lời như thể nó là người duy nhất trong phòng.

---

## 3. Nhóm người làm gì ở đúng chỗ này

Không phải phép ẩn dụ — bốn hành vi cụ thể, và cả bốn đều thiếu ở đây:

**1. Người ta nói "tôi nhận" trước khi làm.** Không phải để xin phép, mà để người khác thôi. Trong một phòng ba người, câu đó rẻ và **đến trước công việc**. Ở đây, "tôi nhận" của agent đến *sau* khi nó đã làm xong việc — nếu có.

**2. Không ai hỏi cả phòng mọi câu.** Người hỏi đã biết ai làm mảng nào, hoặc hỏi một người đủ hiểu để chuyển tiếp. Broadcast là lựa chọn cuối, không phải mặc định.

**3. Chuyên môn là thứ **biết sẵn**, không phải thứ suy ra mỗi lần.** "Hỏi Tâm chuyện database" là kiến thức của nhóm, cập nhật khi có người mới. Ở đây nó là đoạn văn chép tay đã lệch.

**4. Người chia việc là người hiểu toàn cục.** Chia việc bằng cách mỗi người tự đoán phần của mình cho ra hai người làm trùng và một mảng không ai làm — đúng chuyện vừa xảy ra.

Một điều nhóm người **không** làm và cũng không nên bắt chước: bầu cử, thương lượng, giao thức đồng thuận. Với ba tiến trình trên một máy dùng chung một SQLite, cái đó là kỹ thuật thừa.

---

## 4. Ba hình dạng, và cái nào đáng làm trước

### 4.1 Hình A — "Ai nhận thì nói" (claim công khai)

Nhiều agent vẫn cùng thức, nhưng **việc đầu tiên mỗi con làm là cố giành quyền trả lời**, bằng một CAS trên hàng thread. Ai thắng thì trả lời; ai thua thì im — hoặc chỉ bổ sung nếu có gì thật sự khác.

- **Rẻ**: một bảng nhỏ `thread_claims(thread_key, agent_id, claimed_at)` với CAS, cùng khuôn `ClaimTask` đã chạy tốt cả tháng.
- **Giải đúng sự cố §1**: hai con không thể cùng tuyên bố nhận một việc.
- **Không giải** chuyện *chọn đúng người*: người thắng là người thức trước, không phải người hợp nhất.

### 4.2 Hình B — "Một con xem rồi chia" (dispatcher)

Agent **được gọi đích danh** đọc yêu cầu, tra bảng đồng đội, rồi tự chia thành các task `--to` từng con. Cha–con (`task sub`), `MaxOpenChildren=8`, `WakeParents` — **đã có sẵn hết** (P2).

- **Đúng với câu hỏi của người dùng** hơn cả: "chỉ cần 1 agent xem xét rồi chia việc".
- **Tận dụng thứ đã ship**: cha park bằng `task block --on children`, con xong thì cha thức dậy với kết quả.
- **Cần**: agent phải *biết* đồng đội là ai và mạnh gì → §5.
- **Rủi ro**: dispatcher trở thành nút thắt, và nếu luôn là agent 1 thì đó là điểm hỏng duy nhất. Giảm bằng cách: **người được gọi là dispatcher**, không chỉ định cố định.

### 4.3 Hình C — "Mỗi con một khía cạnh" (fan-out có chủ đích)

Dispatcher không chia theo *bước* mà theo *góc nhìn*: một con đọc số liệu, một con đọc rủi ro, một con viết tổng hợp. Rồi gộp.

- **Chỉ đáng khi các agent thật sự khác nhau** — và bây giờ thì đúng thế: claude / codex / opencode khác về cửa sổ ngữ cảnh, về vòng lặp tool, và **về giá**: `muse-spark-1.3-contributor-free` tốn `"cost":0`, đo được trong một lượt chạy thật.
- Đó là lý do kinh tế để **định tuyến** thay vì **phát tán**: việc đọc nhiều, phán đoán ít nên về agent 3.
- **Rủi ro thật**: gộp ba câu trả lời thành một là việc khó hơn nó trông, và nếu làm ẩu thì kết quả là ba đoạn dán cạnh nhau — tệ hơn một câu trả lời tử tế.

**Đề xuất thứ tự: A → B → C.** A nhỏ và chặn đúng cái đau đang xảy ra. B mở ra năng lực thật và dùng lại nguyên tầng cha–con. C chỉ có nghĩa sau khi B chạy, vì nó là một chiến lược *của dispatcher*, không phải một cơ chế riêng.

---

## 5. Điều kiện cần chung: bảng đồng đội là **dữ liệu**

Cả B lẫn C sụp đổ nếu agent không biết đồng đội. Và biết bằng văn xuôi chép tay thì đã hỏng rồi (§2.1).

Đề xuất: mỗi agent **tự khai** khi khởi động, vào `coord.agents`:

- `provider`, `model` — đã có.
- `cost_hint` — `free | subscription | metered`. Đây là thứ đổi cách chia việc nhiều nhất và hiện không ai biết.
- `strengths` — một dòng người viết trong config (`agent.strengths`), ví dụ *"đọc nhiều file, tổng hợp; rẻ"*. Ngắn, vì nó vào prompt của mọi agent khác.
- `busy` — suy từ `deps.Runs()`, không lưu.

Rồi prompt **sinh ra** từ bảng đó thay vì chép tay: *"Đồng đội đang online: bomclaw2 (codex/gpt-6-astra, subscription — sửa code, chạy test), bomclaw3 (opencode/muse-spark, free — đọc và tổng hợp)."*

Lợi ích đo được ngay: thêm agent thứ tư không phải sửa file nào, và không bao giờ lệch nữa — thứ vừa xảy ra với bomclaw3 sẽ không lặp lại.

---

## 6. Và một thứ nhỏ nhưng nên làm cùng A

Agent bị đánh thức **phải biết ai khác cũng bị đánh thức**. Một dòng trong prompt: *"Cùng được gọi: bomclaw2, bomclaw3."*

Không có nó thì mọi luật nhường nhau đều là đoán. Có nó thì ngay cả khi chưa làm gì thêm, một agent tử tế cũng đã có cơ sở để nói "để bomclaw3 trả lời, nó hợp hơn" — tức phần lớn giá trị của A, với chi phí một dòng `fmt.Fprintf`.

---

## 7. Lộ trình

| | Việc | Vì sao ở đây |
|---|---|---|
| **W1** | Prompt nói ai cùng được gọi (§6) | Một dòng. Không có nó, mọi thứ sau đều là đoán |
| **W2** | `thread_claims` + CAS: ai nhận thì nói (§4.1) | Chặn đúng sự cố §1. Dùng lại khuôn `ClaimTask` |
| **W3** | Bảng đồng đội là dữ liệu, prompt sinh ra từ nó (§5) | Điều kiện cần của W4; và tự nó sửa cái prompt đang lệch |
| **W4** | Dispatcher: người được gọi chia việc bằng `task sub --to` (§4.2) | Dùng lại toàn bộ P2. Không có W3 thì nó chia mù |
| **W5** | Chia theo khía cạnh + gộp (§4.3) | Là chiến lược của W4, không phải cơ chế mới |

---

## 8. Rủi ro & câu hỏi mở

1. **Nhường nhau bằng prompt là thoả thuận, không phải bảo đảm.** W1 và phần "im lặng" của W2 phụ thuộc vào việc model chịu nghe. CAS ở W2 là thứ duy nhất *cưỡng chế* được. Nên đo: sau W1+W2, còn bao nhiêu lần hai agent cùng tuyên bố nhận một việc?
2. **Dispatcher tiêu một lượt chỉ để chia việc.** Với câu hỏi nhỏ thì đó là lãng phí gấp đôi. Ngưỡng nào thì chia, ngưỡng nào thì tự trả lời? Nghiêng về: để model tự quyết, nhưng prompt phải nói rõ rằng chia việc **cũng tốn**.
3. **`cost_hint` dễ biến thành "cứ đẩy hết sang con free".** Agent 3 chạy model free, nhưng nó không mạnh bằng hai con kia; định tuyến theo giá mà bỏ qua độ khó sẽ cho ra câu trả lời rẻ và sai. `strengths` phải nói cả điểm yếu.
4. **Gộp kết quả (C) chưa có chỗ đứng.** Cha đọc kết quả con qua artifact (P4) — đủ để *đọc*, chưa nói gì về *hợp nhất ba góc nhìn mâu thuẫn*. Ai phân xử khi hai con nói ngược nhau?
5. **Ba agent, một quota.** agent 1 và agent 3 dùng chung OAuth claude (CLAUDE.md); fan-out ba con không nhân ba năng lực nếu hai con chia nhau một hạn mức. `accounts.pool` và agent 3 chạy opencode đã gỡ phần lớn, nhưng cần nhớ khi tính W5.
