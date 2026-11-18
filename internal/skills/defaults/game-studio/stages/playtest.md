# game-playtest — cổng verify

Không có cổng này thì "prompt ra game" chỉ là demo. Chạy tuần tự, fail-fast.
Dùng `browse`/`gstack` hoặc Playwright MCP.

## G1 — Tĩnh
- `spec.json` và `arcade.toml` hợp lệ; capability dùng trong code ⊆ capability khai báo.
- Bundle trong hạn mức; không gọi domain ngoài allowlist; không `eval`.
- Mọi ràng buộc trong `spec.balance` được kiểm bằng grep/assert trên hằng số thật.
  *(Đây là nơi bắt được lỗi kiểu `CANNON_DMG < BRICK_HP`.)*

## G2 — Boot
- Trang load, **0 console error**.
- Canvas vẽ ra khung hình khác màu nền trong 3 giây → bắt "màn hình đen".
- Chạy 60s: heap không tăng tuyến tính vượt ngưỡng.

## G3 — Chơi được
- Bơm chuỗi input theo `spec.controls` → state **phải đổi**. Bắt game "vẽ đẹp mà không tương tác".
- Bot script sinh từ `spec.winCondition` → phải **đến được màn thắng**.
- Viewport 390×844: không tràn ngang; vùng chạm ≥ 44px.
- Nếu là puzzle có nghiệm: chạy solver, xác nhận **nghiệm duy nhất** trên ≥ 20 seed.

## G4 — Multiplayer (chỉ khi mode ≠ offline)
- 2 client headless vào cùng room code → state hội tụ (checksum khớp sau 3s).
- Ngắt 1 client → rejoin trong window → state đúng.
- Tick p99 trong ngân sách; state không vượt giới hạn.

## Báo cáo
Bảng pass/fail từng cổng + screenshot của lần fail đầu tiên + log lỗi.
**Fail thì không publish.** Chuyển sang `stages/iterate.md` với đúng log đó, tối đa 3 vòng, rồi trả về cho người.
