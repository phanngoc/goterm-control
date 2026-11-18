# game-spec — prompt thành GameSpec

Không nhảy thẳng từ prompt vào code. Sửa spec rẻ hơn sửa code, và spec là thứ `stages/playtest.md`
dùng để sinh test case.

## Bước 1 — Nếu là clone một game có thật, đi tìm cơ chế thật

`castle` và `rumba` đều là clone. Cả hai chỉ làm đúng vì đã xác định cơ chế gốc trước khi code:
rumba = họ Binairo/Tohu-wa-Vohu (cân bằng hàng/cột, không 3 liên tiếp, ràng buộc `=`/`×`, nghiệm duy nhất).
Đoán cơ chế → game sai luật, sửa rất đắt. Tra bằng WebSearch, ghi luật ra spec trước.

## Bước 2 — Sinh GameSpec

```json
{
  "id": "kebab-case",
  "title": "...",
  "genre": "action-arcade | puzzle | turn-based | party",
  "loop": "một câu: người chơi làm gì, lặp lại thế nào",
  "mode": "authoritative | relay | offline",
  "maxPlayers": 1,
  "entities": [{ "name": "tank", "props": ["hp", "dir", "speed"] }],
  "rules": ["luật 1 (nếu clone: trích từ nguồn)", "..."],
  "controls": { "mobile": "dpad + nút bắn", "desktop": "WASD + space" },
  "winCondition": "điều kiện thắng, đo được",
  "loseCondition": "điều kiện thua, đo được",
  "artDirection": "pixel 8-bit, palette 4 màu, portrait",
  "template": "canvas2d-offline | canvas2d-authoritative-mp | turn-based-mp | party-relay",
  "balance": [{ "param": "CANNON_DMG", "constraint": ">= BRICK_HP", "why": "không thì không phá được gạch" }]
}
```

## Bước 3 — Chọn mode đúng, đây là quyết định đắt nhất

- Một người, không cần đối thủ thật → `offline`. **Mặc định.** Đừng thêm phòng vì "sau này có thể cần".
- Nhiều người, cần chống cheat / có xếp hạng → `authoritative`.
- Nhiều người, vui vẻ, không xếp hạng → `relay` (rẻ hơn nhiều).

## Bước 4 — Bắt buộc: mục `balance`

Mỗi ràng buộc số học mà nếu sai thì game **không thể chơi được** phải nằm trong spec.
Bài học từ `castle`: sát thương pháo phải ≥ HP mỗi viên gạch, vì HP castle = số gạch còn lại —
thiếu ràng buộc này thì bắn cả trận mà HP không giảm, và bug này rất khó thấy khi đọc code.

## Đầu ra

Ghi `spec.json` vào thư mục game. In lại cho người dùng phần `loop`, `winCondition`, `controls`,
`balance` và hỏi có sửa gì không — trước khi gọi `stages/build.md`.
