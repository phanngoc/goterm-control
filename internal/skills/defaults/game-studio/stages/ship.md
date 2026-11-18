# game-ship — deploy

## Hạ tầng hiện tại (từ 2026-11-06)

pm2 giữ process sống + Cloudflare tunnel `bomclaw` đưa ra internet: `<game>.bomclaw.org` → ingress →
`127.0.0.1:<PORT>`. Platform arcade (mọi game host, :8090) đã chạy trong pm2: app `arcade`,
`~/.goterm-shared/pm2/arcade.config.cjs`. Game mới thường chỉ cần đăng ký vào platform; chỉ game tự host mới cần
một app pm2 riêng.

**Checklist (game tự host):**
1. Chọn PORT chưa dùng: `bomclaw infra ls` liệt kê mọi cổng đang mở và ai giữ nó.
2. Thêm một app vào `~/.goterm-shared/pm2/arcade.config.cjs` (hoặc file của dự án game đó): `name`,
   `namespace`, `cwd` = thư mục game, `script` = node tuyệt đối, `args` = [server.js tuyệt đối],
   `interpreter: 'none'`, `env` (PORT, HOST=127.0.0.1 nếu sau tunnel, PATH), `out_file`/`error_file` trong
   thư mục game.
3. `pm2 start <file> --only <name> && pm2 save`. Sửa code thì `pm2 restart <name>`.
4. Thêm ingress vào config cloudflared + DNS record.
5. Verify **cả hai tầng**: `curl -o /dev/null -w '%{http_code}' http://127.0.0.1:<PORT>/` **và** `https://<game>.bomclaw.org/`.
6. Dashboard → Admin → Infra phải thấy app online, đúng cổng. Đừng chạy server bằng nohup/launchd: không ai
   restart nó, và Infra báo nó là "không ai quản lý".

## Cạm bẫy đã gặp

- **Server .ts chạy bằng node (type stripping) phải để node làm `script`**, `interpreter: 'none'`: wrapper fork của pm2
  nạp script bằng require(), server ESM không bao giờ lên mà pm2 vẫn báo `online`. *(Xảy ra 2026-11-06 khi chuyển
  arcade sang pm2.)*
- **Đường dẫn tuyệt đối trong file ecosystem (trước đây là plist).** Di chuyển thư mục game là hỏng service, và **không có lỗi nào hiện ra
  cho tới khi ai đó mở trang**. Đổi chỗ thư mục thì phải sửa file ecosystem + `pm2 restart`. *(Xảy ra thật ngày 2026-09-06
  khi gom 3 game vào `games/`.)*
- **cloudflared không hot-reload**: SIGHUP làm nó thoát, launchd khởi động lại. Đừng tưởng là crash.
- Dùng `node` bằng đường dẫn nvm tuyệt đối; pm2 khởi động lúc boot không có PATH của shell.

## Khi đã có platform

Thay toàn bộ mục trên bằng `arcade deploy`: bundle bất biến theo version, room module đẩy vào isolate pool,
rollback 1 lệnh (`ops/rollback.sh`, dừng app pm2 `arcade`). pm2/tunnel chỉ còn dùng cho self-host.
