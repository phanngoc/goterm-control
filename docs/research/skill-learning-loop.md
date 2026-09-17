# Vòng lặp học kĩ năng — hermes-agent dạy được gì cho hệ này

> Đọc từ `~/Documents/projects/hermes-agent` @ `main` và `hermes-memory-skills-deep-dive.md`.
> Ngày đọc: 2026-09-17. Đối chiếu với `internal/skills`, `internal/memory`, `internal/taskrunner`.

Tài liệu này **không tóm tắt hermes**. Nó trả lời một câu: *cái gì trong đó áp được vào đây, theo thứ
tự nào, và cái gì thì không.*

---

## 0. Chỗ ta đang đứng

| | hermes | ở đây |
|---|---|---|
| Skill là gì | thư mục + `SKILL.md`, frontmatter `name`/`description` | **giống hệt** |
| Vào prompt thế nào | index `name + description`, thân đọc theo nhu cầu | **giống hệt** |
| Ai sở hữu | mỗi agent một bản | **giống hệt** (PR #182) |
| Đổi được lúc chạy | có | **có** — index đọc lại mỗi lượt |
| **Ai sửa skill** | **agent tự sửa, sau mỗi ~10 lượt** | **chỉ người, qua hub** |
| **Đếm mức dùng** | `.usage.json` — counter, state, pinned, created_by | **không có** |
| **Bảo trì dài hạn** | curator: active → stale → archived | **không có** |

Nền đã xong. Thiếu đúng **vòng lặp**: không gì ở đây khiến một skill **tự tốt lên**.

---

## 1. Ba tầng thời gian

hermes tách việc học thành ba tầng, mỗi tầng một ngân sách và một mức rủi ro:

| Tầng | Khi nào | Làm gì | Chi phí |
|---|---|---|---|
| **trong lượt** | agent thấy việc liên quan | `skill_view` → đọc thân file | ~0 |
| **sau lượt** | mỗi ~10 lượt, hoặc `/refine` | fork xem lại hội thoại, **sửa skill** | một lần gọi model |
| **hàng tuần** | máy rảnh ≥ N giờ | curator: stale/archive, gộp skill trùng | phần lớn **không LLM** |

Điểm đáng học không phải ba con số, mà **vì sao tách**: một việc chạy mỗi lượt không được phép đắt;
một việc ghi đè file không được phép chạy khi người đang gõ; một việc xoá thứ gì đó phải chạy hiếm và
có ảnh chụp trước.

Ở đây ta mới có **tầng một**.

---

## 2. Phần đắt giá nhất lại là phần rẻ nhất để bê về

Ba khối prompt của hermes là **triết lý học viết thành luật**. Chúng là *chữ*, không phải code — nên
chi phí gần bằng không, và giá trị thì cao nhất trong cả tài liệu.

### 2.1 Hình dạng của một bài học

> Skill = hướng dẫn làm **một lớp việc** theo cách user này muốn, sao cho session sau làm đúng **ngay
> lần đầu**.

- **Quy trình trước**: các bước đúng thứ tự, kèm lệnh cụ thể. Bài học **gắn vào bước nó ảnh hưởng**,
  không gom thành mục "Lessons" cuối file.
- **Một cạm bẫy = quy tắc tổng quát + một mệnh đề VÌ SAO**, viết ở thể mệnh lệnh. Không phải tường
  thuật chuyện đã xảy ra.
- **Không số PR, không ngày, không ticket, không trích chat làm nội dung.** Quy tắc phải đứng vững mà
  không cần câu chuyện đằng sau.
- **Cùng một bài học hai lần = MỘT quy tắc**, được làm mạnh lên — không nối thêm bản sao.
- **Sai thì sửa tại chỗ**, không nối `"UPDATE: actually…"` bên dưới.

Cái cuối là lý do họ có linter bắt `incident-log-shape`: một `SKILL.md` **100k ký tự** dày đặc số PR,
và một skill có **443 file reference kiểu một-file-một-session**. Đó là hình dạng một vòng lặp không
có luật sẽ tự trôi tới.

### 2.2 Thứ tuyệt đối không được thành skill

Mở đầu bằng lý do: *chúng biến thành ràng buộc tự áp đặt vĩnh viễn, cắn lại khi môi trường đổi.*

- **Lỗi phụ thuộc môi trường** — thiếu binary, sai path, chưa cấu hình credential. User sửa được.
- **Khẳng định phủ định về tool** — *"browser không hoạt động"*, *"X bị hỏng"*.
  → *"These harden into refusals the agent cites against itself for months after the actual problem
  was fixed."*
  **Đây là failure mode nguy hiểm nhất của self-improvement: agent tự dạy mình từ chối làm việc.**
- **Thất bại chưa giải quyết** — session thử vài thứ, không cái nào được, rồi bảo user tự kiểm. Viết
  đống đó thành "workflow đáng tin cậy" là *trình bày một chuỗi thất bại chưa kiểm chứng như hướng dẫn
  đã validate mà session sau sẽ tin và lặp lại*. Hoặc nói "không có gì để lưu", hoặc **chỉ** ghi
  phương án mình **thực sự tự tin**.
- Tool hỏng vì **setup state** → ghi **CÁCH SỬA** vào mục troubleshooting, **không bao giờ** ghi "tool
  này không hoạt động" như một ràng buộc độc lập.

### 2.3 Thứ tự hành động — chọn cái **sớm nhất** phù hợp

1. Vá skill **đang được load trong session** — nó đang ở trong cuộc.
2. Vá một **umbrella đã có** — thêm subsection, thêm pitfall, mở rộng trigger.
3. Thêm **file hỗ trợ** dưới umbrella có sẵn (`references/<chủ-đề>.md`, đặt tên theo **chủ đề**, không
   theo session/sự cố), và SKILL.md nhận **một dòng trỏ** tới nó.
4. Tạo skill mới — **chỉ khi** không cái nào phủ được lớp việc đó. Tên phải ở **mức lớp**:
   *"nếu cái tên chỉ có nghĩa với task hôm nay thì nó sai."*

Đây là thứ chặn sprawl. Không có nó, mỗi bài học đẻ ra một skill mới.

### 2.4 Tín hiệu hạng nhất

User sửa **style / tone / format / độ dài** của bạn — *"stop doing X"*, *"quá dài dòng"*, *"cứ trả lời
thẳng đi"* — là **tín hiệu SKILL hạng nhất, không chỉ tín hiệu memory**. Vì memory nói *"user là ai"*,
skill nói *"làm lớp việc này cho user này thế nào"*.

Và: *"một pass không làm gì là **cơ hội học bị bỏ lỡ**, không phải kết quả trung tính"* — nhưng
"không có gì để lưu" vẫn là lựa chọn thật.

---

## 3. Bài học vận hành đắt nhất trong cả tài liệu

Fork review của họ ban đầu **chặn `read_file`/`search_files`** cho an toàn. Kết quả đo được trên một
deployment thật:

> **~142 denial + ~204 read-before-write refusal trong 2 ngày.**

Vì sao: guard bắt buộc phải `skill_view` **tươi** trước khi vá. Model thì tự nhiên với tay lấy
`read_file` để xem skill — bị từ chối — nên **không bao giờ load file theo cách guard yêu cầu**, và
**gần như không patch nào đáp xuống**. Vòng lặp tự cải thiện **chết đói bởi chính guard của nó**.

> **Guard và bề mặt tool phải khớp nhau. Một guard đòi hành vi X mà whitelist cấm phương tiện làm X
> thì hệ thống không an toàn hơn — nó chỉ ngừng hoạt động.**

Cùng họ với lỗi ta đã gặp ở đây: `--disable-slash-commands` tắt skill của CLI vì nó cạnh tranh với
`bomclaw browser`; chặn đúng, nhưng nếu ta cũng chặn luôn đường agent đọc `SKILL.md` thì index sẽ là
một danh sách tên vô dụng.

---

## 4. Những cơ chế đáng bê, và cái giá thật của chúng

### 4.1 Read-before-write (rẻ, làm sớm)

Trước khi vá một `SKILL.md` đã tồn tại, phải **đọc tươi nó trong chính lượt đó**. Nội dung được trích
trong transcript **không tính** — nếu không, fork sẽ vá thứ nó chỉ *suy ra* từ hội thoại, và đó chính
là cách sinh ra bản ghi đè mất nội dung.

### 4.2 Provenance quyết định ai được đụng vào gì (rẻ)

`.usage.json` giữ `created_by`, và **chỉ trường đó** quyết định curator được động vào skill nào —
**không bao giờ suy từ vị trí file**.

| `created_by` | curator quản? |
|---|---|
| `agent` (fork tạo ra) | ✅ |
| `learn` (user chỉ đạo tạo) | ❌ |
| vắng (viết tay, có từ trước) | ❌ |

Hệ quả họ cảnh báo: thư viện **trông** như được curate trong khi phần lớn nằm ngoài tầm với. Nên có
`curator list-unmanaged` và `curator adopt` — **chuyển giao bằng tuyên bố tường minh**.

Ở đây trường tương đương sẽ là: skill nào do **bộ mặc định** gieo, skill nào do **người** viết qua hub,
skill nào do **vòng lặp** đẻ ra. Ta đã có một nửa — cờ `bundled`/`đã sửa` trong hub.

### 4.3 Máy trạng thái curator (rẻ — **không cần LLM**)

`active → stale (14d) → archived (30d)`, và **`reactivated` là chuyển dịch hai chiều**: skill stale
được dùng lại thì **tự quay về active**. Không có chiều ngược, mọi skill đều rơi xuống đáy theo thời gian.

Hai chi tiết nhỏ mà sai là hỏng hẳn:

- **Anchor = `last_activity ?? created_at ?? now`.** Neo vào epoch thì **mọi skill mới tự archive ngay**.
- **Lần quan sát đầu tiên không làm gì** — seed `last_run_at = now` rồi thôi. Cài mới được trọn một
  interval để người kịp xem và pin thứ quan trọng **trước khi** curator chạm vào.
- **Cron-referenced = pinned.** `use_count` chỉ tăng khi job bắn, nên job chạy thưa sẽ làm skill của
  nó già đi rồi **bị archive ngay dưới chân job**.

### 4.4 Persist trước khi gọi LLM (rẻ)

Ghi `last_run_at` **trước** khi gọi model: crash giữa chừng vẫn tính là đã chạy, **không lặp vô hạn
một pass hỏng**. Snapshot là **best-effort** — *"một lỗi đĩa nhất thời không được âm thầm vô hiệu hoá
curator vĩnh viễn"*. Dry-run **không** bump đồng hồ, nhưng **vẫn** viết report.

### 4.5 Không tin lời tự thuật của model, cũng không vứt nó (vừa)

Khi phân loại "skill này biến mất vì được gộp hay vì bị xoá", họ dùng **ba nguồn có thứ tự tin cậy**:
khai báo **tại điểm hành động** > tự thuật **sau khi xong** > **audit từ tool call**. Mỗi kết luận mang
theo `source` để người đọc biết nó đến từ đâu.

Bắt được đúng hai lỗi: **umbrella bịa ra** và **bỏ sót khỏi bản tóm tắt**.

### 4.6 Bỏ hẳn `terminal` khỏi fork — đóng lỗ bằng **cấu trúc** (rẻ)

Một `mv`/`rm` qua shell dưới cây skills ghi đúng bytes nhưng **không sinh ledger entry**, nên bản
archive sau đó chụp một package đã bị rút ruột và rollback khôi phục ra skill rỗng. Cách sửa **không
phải** heuristic dò lệnh nguy hiểm — mà là **bỏ hẳn toolset**: *"no command heuristic can."*

### 4.7 Cache-parity fork — **không áp được**

Fork giữ system prompt byte-identical với cha để trúng prefix cache: đo được **~26% rẻ hơn**. Kĩ thuật
đẹp, nhưng nó đòi kiểm soát prompt gửi lên API. Ở đây ba backend đều là **CLI của người khác** — ta
không cầm cache key. Ghi lại để biết, đừng cố bắt chước.

---

## 5. Áp vào đây — bốn bước, theo đúng thứ tự phụ thuộc

### Bước 1 — Luật, không code *(rẻ nhất, giá trị cao nhất)*

Thêm vào khối skill index: **hình dạng bài học** (§2.1), **danh sách không-được-ghi** (§2.2), **thứ tự
hành động** (§2.3). Cộng một câu nói rõ agent **được phép sửa `SKILL.md` của chính nó**, vì hôm nay
không gì nói với nó điều đó.

Làm được ngay, không cần hạ tầng nào. Và nó **phải đi trước**: một vòng lặp chạy trước khi có luật sẽ
sinh ra đúng đống 443-file mà họ đã phải viết linter để dọn.

### Bước 2 — Đếm mức dùng

Không có counter thì không có curator. Chỗ khó: ta **không nhìn thấy** agent đọc `SKILL.md` — nó dùng
tool `Read` của CLI, ta chỉ thấy một tool span tên `Read`.

Ba cách, chọn một:
- **Đọc từ trace**: ta đã có tool span; khớp đường dẫn `skills/<name>/SKILL.md` trong `inputs`. Không
  cần agent hợp tác, nhưng phụ thuộc hình dạng span của từng backend.
- **`bomclaw skills used <name>`**: chính xác, nhưng cần agent nhớ gọi — cùng loại vấn đề với
  `msg --task` mà ta vừa phải sửa bằng prompt.
- **mtime**: rẻ nhất, sai nhiều nhất.

Khuyến nghị: **đọc từ trace**. Tag đã có từ #126 làm chỗ này dễ hơn.

### Bước 3 — Pass review sau lượt

Chỗ đắt. Ta **không có fork** và không có cache parity để làm nó rẻ. Hình dạng khả dĩ ở đây: một
**task** mở ra sau khi một task khác kết thúc, chạy với prompt review và **chỉ** được dùng công cụ
đọc + ghi skill.

Trước khi làm, phải trả lời: **kích hoạt bằng gì?** Của họ là đếm lượt. Ở đây một "lượt" trải trên ba
làn — chat, mention, task — và task là chỗ có bài học đáng học nhất. Đề xuất: **sau mỗi task
completed**, không phải sau mỗi N lượt chat.

Và **read-before-write guard (§4.1) phải vào cùng bước này**, không phải sau.

### Bước 4 — Curator

Chỉ có nghĩa sau bước 2. Phần lớn **không cần LLM** (§4.3), nên nó rẻ hơn bước 3 dù nghe to hơn.
Phase gộp-skill-trùng cần LLM thì để **opt-in**, đúng như họ.

---

## 6. Rủi ro phải nhận trước

**Ba agent, ba bản sao, phân kỳ là mục đích** (quyết định ở #180). Nghĩa là một vòng lặp chạy **ba lần
song song** trên ba bản khác nhau của cùng một skill. Không có gì hợp nhất chúng ngoài nút **chép sang**
trong hub — tức là **một con người phải nhìn hai bản rồi quyết**.

Đó là đánh đổi đã chọn có ý thức, nhưng nó có nghĩa: **vòng lặp tự cải thiện ở đây không tự cộng dồn
giữa các agent.** Nếu về sau muốn nó cộng dồn, thứ cần thêm không phải là thư viện chung — mà là một
bước **đề xuất chép sang** khi hai bản của cùng một skill lệch nhau và một bản rõ ràng tốt hơn.

**Và cái rủi ro lớn nhất vẫn là §2.2**: một agent tự dạy mình rằng một công cụ hỏng, rồi trích dẫn
chính nó để từ chối làm việc, hàng tháng sau khi sự cố đã được sửa.
