# Design: BomClaw — Agents as a Service (Multi-tenant)

> Trạng thái: DRAFT — thiết kế, chưa triển khai.
> Mô hình tham chiếu: [MyClaw.ai](https://myclaw.ai/) — managed hosting cho OpenClaw/agent cá nhân.
> ⚠️ Bản này chọn mô hình **"AI included" (bán lượt, không BYOK)** dùng **pool OAuth token xoay vòng**. Đây là quyết định kinh doanh có rủi ro ToS/ban đã biết — xem §12 trước khi triển khai.

## 1. Tầm nhìn & mô hình kinh doanh

Biến bomclaw từ **bot cá nhân trên 1 máy Mac** thành **dịch vụ cho thuê agent trọn gói**: mỗi khách hàng có một AI assistant riêng — chat qua Telegram/dashboard, bộ nhớ dài hạn riêng, chạy 24/7. **Khách không cần nhập API key, không cần tài khoản Claude** — cứ mua gói và hỏi.

**Điểm khác biệt cốt lõi so với MyClaw**: MyClaw cố tình **KHÔNG** bao gồm token AI (khách tự trả). Ta chọn hướng **ngược lại — bán lượt hỏi đáp, AI đã bao gồm**. Đây là lợi thế bán hàng (khách lười, không muốn tự lo credential) nhưng cũng chính là rủi ro mà MyClaw đã né (§12).

| Yếu tố | MyClaw | BomClaw AaaS (bản này) |
|---|---|---|
| Đơn vị bán | 1 instance OpenClaw / khách | **Gói lượt hỏi đáp / tháng** + 1 agent instance / tenant |
| Isolation | VM riêng per khách | Container riêng per tenant |
| AI credentials | **Không bao gồm** — khách tự trả (BYOK) | **AI included** — operator sở hữu **pool nhiều token Claude OAuth**, xoay vòng |
| Khách nhập gì | API key của họ | **Không gì cả** — chỉ đăng ký + (tuỳ chọn) dán bot token Telegram |
| Giá | PRO $33.3/th · MAX $66.6/th | Theo **số lượt/tháng**, xem §10 |
| Giá trị bán | 24/7, zero-maintenance, backup | Như trên + **không phải lo token/tài khoản AI** + memory system + Telegram |

**Cơ chế then chốt**: tổ chức (operator) nạp **nhiều token Claude OAuth** vào hệ thống. Mỗi lượt hỏi đáp tiêu 1 token slot; hệ thống **xoay vòng token — dùng ~20 lượt trên 1 token rồi chuyển sang token khác** để phân tán tải, tránh dồn hết vào một subscription. Khách chỉ thấy "còn X lượt".

## 2. Hiện trạng & khoảng cách (gap analysis)

Bomclaw hôm nay là **single-tenant triệt để**:

| Thành phần | Hiện tại | Vấn đề khi multi-tenant |
|---|---|---|
| Agent runtime | 1 process gateway spawn `claude` CLI, `--permission-mode bypassPermissions`, **full quyền trên máy host** | Một tenant = chiếm cả máy. KHÔNG THỂ share process |
| Workspace | 1 thư mục `~/goterm-workspace` chung | Tenant A đọc được file + memory của tenant B |
| Claude auth | 1 OAuth subscription của chủ máy, gắn cứng | Không có khái niệm **pool** hay **xoay vòng**; không đo được lượt/token đã dùng |
| Session | Keyed theo `chat_id` Telegram, 1 DB chung | Chưa có khái niệm tenant |
| Users | Bảng `users` (v4) chỉ để login dashboard | Chưa gắn với tenant/data scoping |
| Telegram | 1 bot token, whitelist user id | Mọi user nói chuyện với cùng 1 agent |
| Memory | 1 bộ MEMORY.md + memory/ chung | Bộ nhớ trộn lẫn giữa các user |
| Metering | Không có | Không đếm được lượt để bán / để tính hết gói |

**Kết luận nền tảng**: (1) vì agent có shell access, ranh giới bảo mật đáng tin duy nhất là **ranh giới OS-level (container/VM)**; (2) vì bán lượt, phải có **credential broker** để cấp phát + xoay token và **turn metering** để đếm lượt. Hai thành phần này là phần việc mới lớn nhất so với hôm nay.

## 3. Nguyên tắc thiết kế

1. **Isolation-first**: mỗi tenant một sandbox OS riêng (container) + volume disk riêng. Không đường code nào cho agent tenant này đọc dữ liệu tenant khác — kể cả khi bị prompt-injection.
2. **Managed credentials (không BYOK)**: credentials AI thuộc **operator**, gom thành **pool** ở control plane, chỉ inject token *đang được cấp* vào container tenant tại đúng thời điểm gọi — không bao giờ để lộ cả pool cho agent.
3. **Provider-agnostic broker (đường lui bắt buộc)**: broker cấp phát credential qua interface `checkout/checkin` **không phụ thuộc loại token**. `kind = claude_oauth | anthropic_api_key`. → Nếu OAuth bị siết/ban, **swap sang API key thương mại hợp lệ mà không viết lại agent** (xem §12).
4. **Meter everything**: mỗi lượt hỏi đáp được đếm 2 chiều — trừ số dư lượt của tenant (billing) và cộng usage cho token phục vụ (rotation + rate-limit awareness).
5. **Control plane / Data plane tách biệt**: gateway (auth, billing, broker) không thực thi lệnh agent; agent không thấy DB điều phối, không thấy pool.
6. **Tái sử dụng tối đa**: `internal/claude`, `internal/session`, `internal/memory`, `internal/bot` hiện tại trở thành **agent runtime** trong container. Gateway tiến hóa thành control plane + broker.

## 4. Kiến trúc mục tiêu

```
                    ┌─────────────────────────── CONTROL PLANE ───────────────────────────┐
Internet ─HTTPS──>  │  bomclaw-gateway                                                     │
(Cloudflare Tunnel) │  ├── Auth (users/tenants, session cookie — đã có từ v4)             │
                    │  ├── Provisioner (tạo/stop/xóa agent container, quota)              │
                    │  ├── Router (tenant_id → agent endpoint)                            │
                    │  ├── ★ Token Broker (pool OAuth, cấp phát + xoay 20 lượt/token)     │
                    │  ├── ★ Metering (đếm lượt, trừ số dư tenant, ledger)                │
                    │  ├── Billing (Stripe webhook, nạp lượt, tier)                       │
                    │  └── Fleet DB (SQLite → Postgres khi scale)                         │
                    └────────┬───────────────────────────────────┬───────────────────────┘
                             │ lease token (mTLS nội bộ)          │ proxy chat (WS/HTTP)
        ┌────────────────────┼────────────────────────┐          │
        ▼                    ▼                        ▼           ▼
┌─ tenant: alice ──────┐ ┌─ tenant: bob ────────┐ ┌─ tenant: carol ──────┐   DATA PLANE
│ agentd (bomclaw core)│ │ agentd               │ │ agentd               │
│ ├─ claude CLI        │ │ ├─ claude CLI        │ │ ├─ claude CLI        │
│ │   (token cấp/lượt) │ │ │   (token cấp/lượt) │ │ │   (token cấp/lượt) │
│ ├─ telegram poller*  │ │ ├─ telegram poller*  │ │ ├─ telegram poller*  │
│ ├─ memory system     │ │ ├─ memory system     │ │ ├─ memory system     │
│ └─ /workspace (vol)  │ │ └─ /workspace (vol)  │ │ └─ /workspace (vol)  │
└──────────────────────┘ └──────────────────────┘ └──────────────────────┘
        * bot token Telegram vẫn BYO per tenant (xem §9); token AI thì KHÔNG — do broker cấp
```

**Khác biệt lớn so với bản BYOK**: token AI **không** nằm cố định trong container tenant nữa. Mỗi lượt, agentd **checkout** token đang-active từ broker → dùng → **checkin**. Broker là thành phần trên hot-path mọi lượt (đánh đổi: xem downgrade isolation ở §11–12).

### 4.1 Control plane — `bomclaw-gateway`
- **Không spawn claude CLI.** Điều phối + sở hữu pool token + đếm lượt.
- Sở hữu: users, tenants, billing, **provider_tokens (pool)**, **turn_ledger**, audit. **Không** sở hữu nội dung chat.
- Route dashboard user → agent container tương ứng; cấp phát token cho mỗi lượt.

### 4.2 Data plane — `agentd` (bomclaw core đóng gói lại)
- Giữ toàn bộ code hiện có: claude wrapper, session manager, memory, Telegram, transcript.
- `--workspace /workspace`, `--data-dir /data` (volume + SQLite riêng của tenant).
- **Mới**: trước mỗi lượt gọi broker lấy token → ghi vào `~/.claude/.credentials.json` (hoặc env) → spawn `claude -p --resume` → sau khi trả lời, checkin usage.
- Một tenant ≈ bomclaw single-tenant hôm nay, khác đúng chỗ credential do broker cấp.

## 5. Schema control plane (v5)

```sql
CREATE TABLE tenants (
  id           TEXT PRIMARY KEY,        -- slug, vd "alice"
  display_name TEXT NOT NULL,
  tier         TEXT NOT NULL DEFAULT 'starter',  -- starter|pro|max
  status       TEXT NOT NULL DEFAULT 'active',   -- active|suspended|deleted
  turn_balance INTEGER NOT NULL DEFAULT 0,       -- ★ số lượt còn lại
  created_at   TEXT NOT NULL
);

-- users (v4) thêm cột:
ALTER TABLE users ADD COLUMN tenant_id TEXT REFERENCES tenants(id);
ALTER TABLE users ADD COLUMN is_operator INTEGER DEFAULT 0;  -- vận hành platform

CREATE TABLE agent_instances (
  tenant_id     TEXT PRIMARY KEY REFERENCES tenants(id),
  container_id  TEXT DEFAULT '',
  state         TEXT NOT NULL DEFAULT 'stopped', -- provisioning|running|stopped|error
  endpoint      TEXT DEFAULT '',                 -- http://agent-alice:8080 (mạng nội bộ)
  cpu_limit     REAL, mem_limit_mb INTEGER, disk_limit_gb INTEGER,
  updated_at    TEXT NOT NULL
);

-- ★ POOL token AI của operator (KHÔNG per-tenant) — mã hóa envelope
CREATE TABLE provider_tokens (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  label          TEXT,                            -- "acc-01" để operator nhận diện
  kind           TEXT NOT NULL DEFAULT 'claude_oauth', -- claude_oauth | anthropic_api_key
  ciphertext     BLOB NOT NULL,                   -- AES-GCM, master key ngoài DB (0600/KMS)
  status         TEXT NOT NULL DEFAULT 'active',  -- active|cooling|disabled|banned
  turns_used     INTEGER NOT NULL DEFAULT 0,      -- tổng lượt đời token (audit)
  window_turns   INTEGER NOT NULL DEFAULT 0,      -- lượt trong cửa sổ rate-limit hiện tại
  window_start   TEXT,                            -- mốc cửa sổ (Claude giới hạn theo ~5h)
  dwell_turns    INTEGER NOT NULL DEFAULT 0,      -- ★ lượt liên tiếp trên token này (reset khi xoay)
  cooldown_until TEXT,                            -- set khi gặp 429/limit
  last_used_at   TEXT,
  created_at     TEXT NOT NULL
);

-- ★ sổ cái lượt: vừa để billing (trừ tenant) vừa để audit (token nào phục vụ)
CREATE TABLE turn_ledger (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  ts         TEXT NOT NULL,
  tenant_id  TEXT REFERENCES tenants(id),
  session_id TEXT,
  token_id   INTEGER REFERENCES provider_tokens(id),  -- token đã phục vụ lượt này
  delta      INTEGER NOT NULL,        -- -1 mỗi lượt hỏi đáp, +N khi nạp/refund
  reason     TEXT NOT NULL            -- qa_turn | topup | refund | adjust
);

CREATE TABLE audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts TEXT, actor TEXT, tenant_id TEXT, action TEXT, detail TEXT
);
```

Nội dung chat/session/memory **không nằm ở control plane** — nằm trong SQLite + files của từng container (volume tenant). Control plane chỉ biết metadata + đếm lượt.

## 6. Workspace & disk isolation

- Mỗi tenant 1 volume: `/data/tenants/<tenant_id>/` gồm `workspace/` (agent làm việc + memory) và `data/` (SQLite, transcripts).
- Mount vào container tại `/workspace`, `/data`. **Không mount path nào khác của host.**
- Quota theo tier — filesystem quota (XFS project quota) hoặc volume size limit.
- Rootfs container **read-only**; chỉ `/workspace`, `/data`, `/tmp` ghi được.
- Backup: snapshot volume hằng ngày (restic/borg), retention theo tier.
- Xóa tenant: stop container → giữ volume 30 ngày (grace) → xóa vĩnh viễn.

## 7. Sandbox runtime — lựa chọn & khuyến nghị

| Phương án | Isolation | Chi phí/khách | Ghi chú |
|---|---|---|---|
| **A. Container (Docker + gVisor/runsc)** ✅ đề xuất mặc định | Tốt | Thấp | Agent chạy `claude` CLI trong Linux container |
| B. microVM (Firecracker) | Rất tốt | Trung bình | Tier Max / enterprise |
| C. VM đầy đủ / VPS (mô hình MyClaw) | Tốt nhất | Cao | Ít code, vận hành thủ công hơn |
| D. macOS sandbox-exec | Yếu, deprecated | — | ❌ Loại |

**Khuyến nghị**: dev/beta trên Mac hiện tại (OrbStack) với container thường; production thuê 1 server Linux (Hetzner/OVH ~$40–80/th) chạy Docker + gVisor. Network container: **egress-only**, chặn 169.254.0.0/16 (metadata), chặn LAN, chặn mạng control plane (chỉ cho phép chiều gateway↔agentd + agentd→broker).

## 8. ★ Token pool & rotation (thành phần cốt lõi mới)

Đây là phần thay thế mục "BYOK" của bản trước, và là phần việc kỹ thuật rủi ro/khó nhất.

### 8.1 Nạp token
- Operator vào dashboard admin → dán nhiều token Claude OAuth (lấy từ `claude setup-token` trên từng account Pro/Max), đặt `label`. Lưu mã hóa AES-GCM vào `provider_tokens`, master key ngoài DB.
- Cũng chấp nhận `anthropic_api_key` cùng bảng (`kind` khác) → sẵn sàng cho đường lui §12.

### 8.2 Rotation policy — "20 lượt rồi đổi token"
Broker giữ con trỏ tới token đang-active. Với mỗi lần `checkout`:
1. Bỏ qua token `status != active` hoặc `cooldown_until > now`.
2. Cấp token hiện tại; `dwell_turns++`, `window_turns++`.
3. Khi `dwell_turns >= 20` → advance con trỏ sang token active kế tiếp (round-robin), reset `dwell_turns = 0`.
4. Nếu lượt trả về **429/limit** → mark token `cooling`, set `cooldown_until`, advance con trỏ ngay và **retry lượt trên token khác** (không tính là hết dwell).
5. Cửa sổ rate-limit: nếu `now - window_start > 5h` → reset `window_turns`, `window_start = now`.

> Nâng cấp về sau (ghi chú): thay round-robin cứng bằng **least-loaded theo `window_turns`** để rải đều theo cửa sổ 5h của Claude, và **giới hạn concurrency per-token** (không cấp 1 token cho 2 lượt song song) — giảm áp lực limit và độ "lộ".

### 8.3 Broker API (nội bộ, mTLS)
- `POST /internal/token/checkout {tenant_id, session_id}` → `{token_id, kind, secret}` — lease token đang-active.
- `POST /internal/token/checkin {token_id, ok, error?, in_tok?, out_tok?}` → cập nhật counter/cooldown.
- agentd chỉ nhận **1 token đang dùng**, không bao giờ thấy cả pool.

### 8.4 Flow một lượt hỏi đáp
```
user msg → agentd
  → gateway: turn_balance > 0 ?  (không thì báo "hết lượt, nạp thêm")
  → broker.checkout() → ghi ~/.claude/.credentials.json (hoặc env) trong container
  → spawn claude -p --resume <session>
  → trả lời xong:
      broker.checkin(token_id, ok)         # token.turns_used++, window_turns++
      metering: turn_ledger(-1, qa_turn)   # trừ số dư tenant
      tenants.turn_balance--
```
**Định nghĩa 1 lượt** = 1 message user → 1 câu trả lời cuối của assistant (bất kể bao nhiêu tool-call bên trong). Đếm khi hoàn tất.

### 8.5 Cảnh báo kỹ thuật (rủi ro triển khai, cần PoC sớm)
- **Session continuity khi đổi token giữa chừng**: state `--resume` là local, context gửi lại mỗi lượt → *lý thuyết* xoay token vẫn resume được. **Chưa xác nhận** Anthropic không gắn session/thread với account. → PoC bắt buộc trước khi cam kết.
- **Prompt cache miss**: cache theo từng account. Đổi token = mất cache → **chậm hơn + đốt token/limit nhiều hơn** (dù OAuth không tính tiền theo token, vẫn ăn vào cửa sổ giới hạn).
- **Ghi credential đua nhau**: nếu nhiều tiến trình trong 1 container hoặc nhiều container dùng chung file credential → race. Giải: mỗi lượt set qua **env riêng cho tiến trình** thay vì sửa file chung, hoặc credential file per-turn.

## 9. Channels per tenant

- **Telegram — BYO bot token (giữ nguyên khuyến nghị)**: mỗi tenant tạo bot riêng qua @BotFather, dán token → agentd trong container tự long-poll. Vì dịch vụ nay là managed hoàn toàn, có thể **thêm tuỳ chọn "bot dùng chung của platform"** cho khách ngại tạo bot (route theo `from.ID` → tenant), đánh đổi bằng identity bot chung + rate limit Telegram chung. Mặc định vẫn BYO bot.
- **Dashboard**: login v4 + tenant scoping; gateway proxy WS `wss://bot.bomclaw.org/t/<tenant>/ws` → agentd. Hiển thị **số lượt còn lại**.

## 10. Tiers & billing (bán theo LƯỢT)

| | Starter | Pro | Max |
|---|---|---|---|
| Giá tham khảo | $9/th | $19/th | $39/th |
| **Lượt / tháng** | 300 | 1.000 | 3.000 (hoặc fair-use) |
| vCPU / RAM | 1 / 2GB | 2 / 4GB | 4 / 8GB |
| Disk workspace | 20GB | 40GB | 80GB |
| Backup retention | 7 ngày | 30 ngày | 90 ngày |
| Kênh | Dashboard + Telegram | + WhatsApp/Discord (sau) | + API access |

- Stripe Checkout + webhook → set `tenants.tier`, nạp `turn_balance` (topup ledger).
- Hết lượt: agent trả thông báo "hết lượt", nút nạp nhanh trên dashboard. Hết hạn thanh toán: `suspended` → stop container (giữ volume).
- **Giá phải phủ chi phí subscription pool**: margin = doanh thu − (chi phí N subscription Claude + hạ tầng). Vì OAuth flat-rate nên **không đo được cost/lượt chính xác** → định giá theo lượt là cách chặn abuse; cần theo dõi tỉ lệ lượt-thực/subscription để không lỗ (xem rủi ro §12).

## 11. Threat model (tóm tắt)

| Mối đe dọa | Đối sách |
|---|---|
| Agent escape container | gVisor/microVM, rootfs read-only, no-new-privileges, seccomp, non-privileged, user namespace |
| Cross-tenant đọc dữ liệu | Volume riêng + docker network riêng per tenant; agentd chỉ nhận kết nối từ gateway |
| **Prompt injection đánh cắp token đang-active** ⚠️ | Chỉ inject token *đang dùng* (không cả pool); token per-turn, thu hồi/rotate nhanh; nhưng **vẫn có thể mất token active** → xem §12 (downgrade so với BYOK) |
| SSRF vào control plane / metadata | Egress firewall: chặn RFC1918, 169.254.*, mạng docker control plane; broker chỉ nhận từ agentd qua mTLS |
| Chiếm control plane (lộ cả pool) | Auth v4 + operator role, audit_log, rate limit, Cloudflare; master key mã hóa ngoài DB |
| Khách abuse (mining/spam) | CPU/RAM limit theo tier, egress rate limit, **giới hạn lượt**, ToS + suspend |
| **Tài khoản Claude bị ban hàng loạt** ⚠️⚠️ | Xem §12 — rủi ro nền tảng của mô hình, không có đối sách kỹ thuật đáng tin |

## 12. ⚠️ Rủi ro & điều kiện (ĐỌC KỸ trước khi triển khai)

Mô hình "pool OAuth token + xoay vòng để bán lượt" mang lại UX tốt nhất cho khách nhưng đặt cược cả dịch vụ vào các rủi ro sau. Ghi lại rõ để team quyết định có chấp nhận không.

**R1 — Vi phạm ToS Anthropic (rủi ro pháp lý, cao).** Token OAuth sinh từ subscription **Pro/Max cá nhân**. Dùng chúng để phục vụ khách trả tiền = gần như chắc chắn vi phạm Consumer Terms / Usage Policy (cấm chia sẻ tài khoản, resell, sub-license). **Cần review pháp lý**; khả năng cao không hợp lệ. (Ngược lại: API key thương mại được phép build sản phẩm phục vụ end-user.)

**R2 — Ban hàng loạt (rủi ro tồn vong).** Xoay token theo nhịp cố định để né rate-limit là **dấu hiệu vi phạm rõ**, khó biện minh "dùng cá nhân". Anthropic có thể phát hiện qua nhiều account cùng IP/ASN/hạ tầng, pattern rotation đều, usage bất thường, device fingerprint của Claude CLI. Hậu quả: **ban đồng loạt cả pool** → dịch vụ chết trong 1 đêm + mất tiền subscription đã trả trước. **Không có đối sách kỹ thuật đáng tin** cho rủi ro này — đa dạng hóa IP/hạ tầng chỉ khiến hành vi *giống né tránh hơn*, không an toàn hơn.

**R3 — Không đo được cost thật.** OAuth flat-rate → không có `usage` cost/lượt như API. Khó định giá chính xác, dễ lỗ nếu vài khách dùng nặng. Chỉ đếm được *lượt*, không đếm được *token/tiền thật*.

**R4 — Cache miss khi rotate** → chậm + tốn cửa sổ limit (§8.5).

**R5 — Downgrade isolation.** Token pool chảy vào container tenant mỗi lượt. Agent bị prompt-injection có thể exfil **token đang-active** → account đó bị chiếm. BYOK không có vấn đề này (secret là của chính khách).

### Điều kiện nếu vẫn triển khai
1. **Giới hạn phạm vi**: chỉ beta nội bộ / nhóm tin cậy, **không mở public** cho tới khi có phương án hợp lệ.
2. **Đường lui bắt buộc (§3.3)**: broker provider-agnostic ngay từ đầu → **swap sang `anthropic_api_key` thương mại** (metering theo token thật, hợp lệ) chỉ bằng đổi `kind`, **không viết lại agent**. Đây là de-risk quan trọng nhất.
3. **Giới hạn thiệt hại**: không trả trước quá nhiều subscription cùng lúc; chuẩn bị **kill-switch** tắt pool tức thì; theo dõi email/cảnh báo từ Anthropic.
4. **Review pháp lý** điều khoản resale/AI-included trước khi tính phí khách thật.

> Khuyến nghị kỹ thuật (không phải khuyến nghị kinh doanh): nếu mục tiêu là "khách không nhập key + bán lượt", **API key thương mại + metering token** đạt đúng mục tiêu đó một cách hợp lệ và **đo cost chính xác hơn**. Bản này giữ kiến trúc sao cho chuyển sang hướng đó là một-dòng-config, không phải một-lần-rewrite.

## 13. Lộ trình triển khai

- **Phase 0 — hoàn thành ✅**: single-tenant + dashboard auth + tunnel public.
- **Phase 1 — Tenant hóa control plane** (~1 tuần): schema v5 (tenants + turn_balance, agent_instances, provider_tokens, turn_ledger, audit), users gắn tenant, dashboard scoping, CLI `bomclaw tenant add/list/suspend`. Agent chạy như cũ cho tenant đầu tiên.
- **Phase 1.5 — ★ PoC token rotation** (rủi ro cao nhất, làm SỚM): xác nhận (a) xoay OAuth token giữa các lượt vẫn `--resume` được; (b) cache miss impact; (c) hành vi khi 1 token hit limit. Nếu (a) fail → mô hình phải đổi.
- **Phase 2 — Broker + Metering + container hóa** (~2–3 tuần): Dockerfile `agentd`, volume layout §6, Provisioner, gateway proxy `/t/<tenant>/`, **Token Broker (§8) + turn metering (§8.4)**, admin UI nạp pool token. Beta OrbStack, 2–3 khách tin cậy.
- **Phase 3 — Thương mại hóa** (~2 tuần): Stripe bán gói lượt, tier enforcement, backup, landing + onboarding self-service (đăng ký → chọn gói → chat, **không nhập key**).
- **Phase 4 — Production scale**: data plane sang server Linux (gVisor), monitoring per agent, multi-host scheduler khi >50 tenant, cân nhắc Postgres. **Đồng thời chuẩn bị sẵn nhánh API-key hợp lệ (§12.2) để bật khi cần.**

## 14. Câu hỏi mở

1. **PoC rotation (§1.5)** — câu hỏi sống-còn: OAuth có gắn session với account không? Cần trả lời trước mọi thứ khác.
2. Ngưỡng "20 lượt" là cố định hay adaptive theo cửa sổ 5h của từng token? (Đề xuất: bắt đầu 20 cứng, chuyển least-loaded sau.)
3. Concurrency per-token: cho phép 1 token phục vụ nhiều tenant song song không? (Đề xuất: giới hạn để giảm áp lực limit + độ lộ.)
4. Có mở "bot Telegram dùng chung platform" (§9) không, hay bắt buộc BYO bot?
5. Support load: mỗi khách một agent = mỗi khách một kiểu hỏng. Cần log tập trung (metadata, không đọc nội dung chat — privacy promise).
6. Mở: cho phép custom skills/MCP per tenant không (tăng attack surface)?

---
*Tài liệu là bản thiết kế để review trước Phase 1. Bản này chọn mô hình "AI included / bán lượt" với pool OAuth xoay vòng — xem §12 về rủi ro. Nguồn tham khảo: [MyClaw.ai](https://myclaw.ai/) (mô hình BYOK đối chiếu), [myclaw.ai/pricing](https://myclaw.ai/pricing).*
