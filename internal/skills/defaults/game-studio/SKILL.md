---
name: game-studio
description: 'Make a browser game in 5 stages. Use when asked to make, clone or tune a game. NOT for: other apps.'
---

# game-studio — prompt thành game chơi được

Năm bước, mỗi bước là một file trong `stages/` cạnh file này. **Đọc đúng file của bước
trước khi làm bước đó** — mỗi file mang bài học đã trả giá từ các game thật (tank-battle,
castle, rumba, blockdrop), không phải lý thuyết.

| Bước | File | Vào → Ra | Cổng chặn |
|---|---|---|---|
| 1. Spec | `stages/spec.md` | prompt → `spec.json` | người duyệt spec |
| 2. Build | `stages/build.md` | spec → code chạy local | — |
| 3. Playtest | `stages/playtest.md` | code + spec → bảng pass/fail G1–G4 | **fail thì dừng** |
| 4. Iterate | `stages/iterate.md` | phản hồi / log fail → patch nhỏ | chạy lại playtest |
| 5. Ship | `stages/ship.md` | code đã pass → URL công khai | verify local + tunnel |

```
prompt → spec → build → playtest ─pass→ ship → phản hồi người chơi ─┐
                          ↑  └fail→ iterate ┘                        │
                          └──────────── iterate ←────────────────────┘
```

Hai vòng lặp: **fix** (playtest ⇄ iterate, tối đa 3 vòng rồi trả về cho người) và
**cải tiến** (người chơi → iterate → playtest → ship).

## Vào bước nào

- "Làm game …", "clone game …" → bắt đầu từ **Spec**. Không nhảy thẳng vào code.
- "Địch nhanh quá", "thêm màn", "giật trên iPhone", hoặc playtest vừa fail → **Iterate**.
- "Đưa lên mạng", "cho bạn chơi thử" → chỉ **Ship** sau khi Playtest pass.

## Trên máy này

- Game một người, không cần server: để trong một dự án có `bomclaw.json` để nút Preview chạy
  được (skill `preview`), và publish tĩnh bằng skill `publish` — đơn giản hơn tự host.
- Game nhiều người cần server WebSocket: theo `stages/ship.md` (pm2 + Cloudflare tunnel, hoặc
  platform arcade ở `~/goterm-workspace/games`, xem `SKILL_CHAIN.md` và `PLATFORM_DESIGN.md` ở đó).
- Playtest dùng browser headless (skill `browser`, Playwright) — đừng tuyên bố "xong" khi chưa qua
  cổng của nó.

Học được gì mới khi làm game (một lỗi cân bằng, một cạm bẫy deploy) → ghi vào file `stages/` của
bước đó, cho lần sau.
