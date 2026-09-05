# Research: Hosting production cho ZeroClaw AaaS (Docker single-node)

**Trạng thái**: RESEARCH — phục vụ quyết định deploy sau này, chưa cam kết.
**Ngày**: 2026-07-18 (giá tham khảo 07/2026 — thị trường đang biến động vì "RAM crisis",
Hetzner đã tăng 30–40% trong 2026, netcup cũng tăng; **kiểm tra lại giá khi mua**).
**Liên quan**: `zeroclaw-docker-aaas.md` (kiến trúc v3 — doc này chọn chỗ đặt nó).

---

## 1. Yêu cầu rút ra từ thiết kế v3

1. **Một VM Linux có Docker daemon do ta toàn quyền** — panel điều khiển `docker.sock`
   trực tiếp ⇒ loại các PaaS container (Cloud Run, Railway, Render…) vì không cấp Docker
   API. Ứng viên hợp lệ: VPS / dedicated server / (ngoại lệ có chủ đích: Fly.io Machines,
   xem §4).
2. **RAM là tài nguyên quyết định mật độ** (v3 §9): tenant idle ~100–150Mi, Chrome burst
   0.5–1Gi. Quy đổi thô: **16GB ≈ 50–80 tenant idle / 6–8 phiên browse; 64GB ≈ 250+ tenant
   idle / ~25 phiên browse**. ⇒ tối ưu **giá/GB RAM**.
3. **Ổn định > rẻ nhất**: bot chạy 24/7 (Telegram long-poll), downtime là user thấy ngay.
4. Egress: LLM API + Telegram — vài trăm GB/tháng là nhiều ⇒ hầu hết provider đều thừa,
   không phải yếu tố quyết định.
5. Latency: Telegram và LLM API không nhạy latency; chỉ dashboard web là user VN cảm nhận
   ⇒ region EU chấp nhận được, SG là điểm cộng không bắt buộc.
6. Ưu tiên có **snapshot/backup + API hạ tầng** (đường lên multi-node sau này panel tự
   provision).
7. Deploy production = **đổi máy Mac thành VM Linux, kiến trúc không đổi** (còn bỏ được
   lớp VM macOS — Docker chạy thẳng trên Linux host).

## 2. Bảng so sánh (giá 07/2026, làm tròn)

| Provider | Gói tiêu biểu | Giá/tháng | €/GB RAM | Ổn định | Region | Ghi chú |
|----------|---------------|-----------|----------|---------|--------|---------|
| **netcup** (DE) | RS 2000 G12 — 8 core dedicated / 16GB / 512GB NVMe | **€16.89** | ~1.06 | Tốt (cộng đồng self-host tin dùng từ 2011) | EU (US ít gói) | Rẻ nhất trong nhóm "ổn định"; billing tháng, API/snapshot yếu hơn Hetzner |
| **Hetzner Cloud** | CX53 — 32GB / shared vCPU | €29.49 | ~0.92 | Rất tốt (VPSBenchmarks A, ISO 27001) | DE/FI (CX/CAX chỉ EU); có SG nhưng giá cao hơn | API + snapshot + backup + hourly billing tốt nhất nhóm; 20TB egress |
| **Hetzner Cloud ARM** | CAX41 — 16 core Ampere / 32GB | €40.99 | ~1.28 | Rất tốt | EU | ARM: cần kiểm chứng Chromium/pinchtab arm64 (§5.3) |
| **Hetzner Auction (dedicated)** | vd AX41 cũ — Ryzen 3600 / 64GB / NVMe | **từ ~€30–45** | **~0.5–0.7** | Rất tốt (hardware refurbished, tự quản RAID) | DE/FI | **Best giá/GB khi scale**; không hourly, có setup fee (chính sách 2026) |
| **Contabo** | ~8–12GB | ~€5–9 | ~0.6–0.8 | **Kém** — uptime 91.1%/quý (Pingoru, Q2/2026), CPU steal 20–40%, VPSBenchmarks F | EU/US/SG | Chỉ cân nhắc cho dev/staging vứt đi được; **loại khỏi production** |
| **DigitalOcean** | 16GB memory-optimized | ~$84 | ~4.8 | Tốt | có **SGP1** | Đắt 3–5× EU; chỉ khi bắt buộc SG + cần hệ sinh thái DO |
| **Vultr** | 16GB regular | ~$80–96 | ~4.5–5.5 | Tốt | có SG | Tương tự DO |
| **Fly.io Machines** | per-machine, tính giây | shared-1x/1GB ≈ $5.7/máy always-on | — | Khá (từng có nhiều incident, free tier đã bỏ) | có SIN | Không phải VPS — xem §4, là phương án kiến trúc riêng |
| **Oracle Cloud Free** | A1 ARM 4 OCPU / 24GB | $0 | 0 | Rủi ro reclaim/khoá account nổi tiếng | có | Chỉ dùng staging/thí nghiệm, không đặt production trả phí của khách |
| Cloud VN (Viettel/FPT/VNG) | 16GB | thường ≥ $60–100 | cao | trung bình, tooling yếu | VN | Chỉ khi cần data residency VN hoặc latency dashboard là ưu tiên số 1 |

## 3. Khuyến nghị theo giai đoạn

```
PoC/P0-P1     : máy Mac hiện tại (đúng thiết kế v3) — $0
                (+ tuỳ chọn: Oracle free ARM làm môi trường thử Linux)
                        │
Production v1 : netcup RS 2000 G12 (€16.89, 16GB, core dedicated)
(20–60 tenant)  — rẻ nhất trong nhóm ổn định, đủ chỗ cho giai đoạn dò sản phẩm
                HOẶC Hetzner CX53 (€29.49, 32GB) nếu muốn API/snapshot/hourly
                và đường tự động hoá hạ tầng đẹp hơn ngay từ đầu
                        │
Scale         : Hetzner Auction dedicated 64GB (~€35–45) — giá/GB tốt nhất,
(100+ tenant)   1 máy gánh ~250 tenant idle; mua máy thứ 2 = HA/DR thủ công
                        │
Vượt 1 máy    : quay lại design k8s v2 (multi-node) hoặc phương án Fly (§4)
```

Lý do chọn **netcup làm production v1**: đúng tiêu chí "giá rẻ + ổn định" nhất bảng —
core dedicated (không CPU steal như Contabo), reputation tốt, €1.06/GB. Trade-off so với
Hetzner: không hourly billing, API/snapshot hệ sinh thái mỏng hơn — chấp nhận được vì
single-node, backup đã tự lo bằng restic (v3 §9). Nếu dự tính chắc chắn sẽ multi-node
trong ~1 năm thì chọn Hetzner ngay từ đầu để panel dùng một API hạ tầng xuyên suốt.

## 4. Phương án kiến trúc thay thế: Fly.io Machines (ghi nhận, chưa khuyến nghị)

Fly Machines = microVM có REST API tạo/dừng/xoá — về vai trò **tương đương docker.sock
as-a-service**. Nếu thay adapter bollard bằng Fly Machines API:
- ✅ Mỗi tenant một **microVM riêng** (Firecracker) — cách ly mạnh hơn hẳn container
  chung kernel; xoá luôn rủi ro tồn dư §10.3-4 của v3.
- ✅ Tính tiền theo giây khi máy chạy → hibernate = stop machine = gần như $0/tenant idle
  → hợp mô hình nhiều tenant ngủ.
- ❌ Always-on (Telegram long-poll) 1GB ≈ $5.7/máy/tháng → 50 tenant always-on ≈ $285/th,
  đắt hơn hẳn 1 VPS 64GB. Chỉ rẻ khi chuyển được sang webhook + wake-on-demand (câu hỏi
  mở #5 của v3).
- ❌ PinchTab shared + private network nội bộ phải thiết kế lại trên 6PN/WireGuard của Fly.
- ❌ Lịch sử ổn định của Fly không bằng Hetzner/netcup.

Kết luận: giữ làm **option P3** khi tỷ lệ hibernate cao và cần isolation mạnh hơn; thiết
kế panel nên tách `trait ContainerRuntime` (bollard | fly) để cửa này không bị đóng.

## 5. Việc cần làm trước khi chốt (checklist mua máy)

1. **Đo lại footprint thật ở P0** trên Mac (RAM idle/burst per tenant) → quy ra cỡ máy.
2. Kiểm tra lại giá tại thời điểm mua (biến động RAM crisis; Hetzner đã tăng 4 lần trong 2026).
3. **Nếu cân nhắc ARM (CAX/Oracle A1)**: kiểm chứng multi-arch của cả ba image —
   zeroclaw (Rust, khả năng cao có arm64), pinchtab (Go, khả năng cao), và **Chromium
   linux/arm64** (Google Chrome không có bản Linux ARM chính thức — pinchtab phải trỏ
   `browser.binary` sang Chromium). Nếu lằng nhằng → chọn x86 cho đỡ rủi ro.
4. Hardening VM khi lên production (ngoài scope v3 vì trước giờ là máy cá nhân):
   SSH key-only + fail2ban, ufw chỉ mở 443 (D8), unattended-upgrades, Docker daemon
   không expose TCP, panel :443 sau Caddy/traefik có TLS.
5. Backup offsite: restic → object storage rẻ (Hetzner Storage Box ~€4/1TB hoặc
   Backblaze B2) — bổ sung đích backup cho v3 §9.
6. DR drill: khôi phục toàn bộ từ backup lên máy trống trong < 1 giờ (compose + volumes
   restore) — single-node không HA, DR là lưới an toàn duy nhất.

## 6. Câu hỏi mở

1. Ngân sách tháng mục tiêu cho hạ tầng là bao nhiêu? (quyết netcup 16GB vs Hetzner 32GB
   vs auction 64GB ngay từ đầu)
2. Dashboard latency với user VN có là tiêu chí không? (nếu có: cân Hetzner SG/Vultr SG,
   chấp nhận đắt hơn; nếu không: EU)
3. Có yêu cầu data residency (dữ liệu hội thoại của khách VN phải ở VN)? — ảnh hưởng
   pháp lý nhiều hơn kỹ thuật.
4. Khi nào cần máy staging riêng thay vì Mac cá nhân? (đề xuất: khi có tenant trả phí
   đầu tiên)

---

*Nguồn: Hetzner pricing/press (đợt điều chỉnh 15/06/2026), bitdoze/comparedge/betterstack
tổng hợp giá Hetzner 2026, netcup.com gói RS G12, VPSBenchmarks (Hetzner A / Contabo F,
CPU steal), DanubeData/Pingoru (uptime Contabo 91.11% Q2/2026), fly.io/docs/about/pricing,
DigitalOcean pricing SGP. Chi tiết link trong PR/commit message.*
