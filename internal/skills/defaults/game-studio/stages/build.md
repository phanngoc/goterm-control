# game-build — spec thành code

## Quy ước bắt buộc (rút từ 3 game đã làm)

1. **Vanilla JS, không framework, không build step.** tank/castle/rumba đều vậy: mở `index.html` là chạy.
   Dependency duy nhất được phép ở game MP là `ws` (và sẽ bỏ luôn khi lên SDK).
2. **Mobile-first, portrait.** Người chơi thật vào bằng điện thoại. Vùng chạm ≥ 44px, D-pad + nút hành động
   cỡ lớn ở nửa dưới, canvas ở nửa trên. Desktop là bonus, không phải mục tiêu chính.
3. **Tách engine khỏi UI.** `engine.js` = logic thuần, test được, không chạm DOM; `game.js` = render + input.
   `rumba` làm đúng (generator + solver kiểm tra nghiệm duy nhất nằm trong engine) → sửa UI không sợ vỡ luật.
4. **Tham số cân bằng đặt thành hằng số có tên, ở đầu file**, kèm comment đơn vị:
   `var GRAV = 1500;  // px/s^2`. Không rải magic number trong logic — `stages/iterate.md` cần sửa số này.
5. **DPR cap = 2.** `Math.min(window.devicePixelRatio || 1, 2)` — hơn nữa chỉ tốn fill rate trên mobile.
6. **Không cấp phát trong vòng lặp render.** Tái dùng object/array; đây là nguyên nhân giật số 1 trên mobile.

## Nếu là game nhiều người

- **Server là chân lý** (authoritative). Client chỉ gửi input, không gửi vị trí.
- Tick 30Hz là đủ cho game 2D; đừng chạy 60Hz trên server.
- Mã phòng 4 ký tự, **bỏ ký tự dễ nhầm** (`0/O`, `1/I`), kèm link mời copy được. Phòng trống tự thu hồi
  sau ~60s. (Khi có Arcade SDK thì platform lo hết phần này — xoá code tự viết.)
- Người vào giữa trận phải vào được; đừng chặn join.

## Nếu đã có Arcade SDK

Đọc `arcade.toml` + typing SDK trước khi viết. Không gọi capability chưa khai báo trong manifest.
State phải là JSON thuần trong `room.state` — không tự serialize, không tự broadcast.

## Đầu ra

Game chạy được ở local + `arcade.toml` (hoặc `server.js` tối thiểu nếu chưa lên platform).
Chuyển ngay sang `stages/playtest.md` — **không tự tuyên bố "xong"** trước khi qua cổng verify.
