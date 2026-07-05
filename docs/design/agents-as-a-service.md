# Design: BomClaw — Agents as a Service (Multi-tenant)

> Trạng thái: DRAFT — thiết kế, chưa triển khai.
> Mô hình tham chiếu: [MyClaw.ai](https://myclaw.ai/) — managed hosting cho OpenClaw/agent cá nhân.

## 1. Tầm nhìn & mô hình kinh doanh

Biến bomclaw từ **bot cá nhân trên 1 máy Mac** thành **dịch vụ cho thuê agent**: mỗi khách hàng có một AI assistant riêng — chat qua Telegram/dashboard, có bộ nhớ dài hạn riêng, chạy 24/7, không phải tự vận hành.

Học từ MyClaw (đã được thị trường kiểm chứng):

| Yếu tố | MyClaw | BomClaw AaaS (đề xuất) |
|---|---|---|
| Đơn vị bán | 1 instance OpenClaw / khách | 1 agent instance / tenant |
| Isolation | VM riêng per khách (2-4 vCPU, 4-8GB RAM, 40-80GB SSD) | Container riêng per tenant (tier cao: VM) |
| AI tokens | **Không bao gồm** — khách tự trả | **BYOK** — khách nhập API key riêng |
| Giá | PRO $33.3/th · MAX $66.6/th (annual) | Starter/Pro/Max, xem §10 |
| Giá trị bán | 24/7, zero-maintenance, auto-update, backup, encryption | Như MyClaw + memory system + Telegram tích hợp sẵn |

**Điểm mấu chốt của mô hình**: không bán "token AI" (biên lợi nhuận âm, rủi ro ToS) — bán **hạ tầng + vận hành + trải nghiệm**. Khách mang API key/subscription của họ.

## 2. Hiện trạng & khoảng cách (gap analysis)

Bomclaw hôm nay là **single-tenant triệt để**:

| Thành phần | Hiện tại | Vấn đề khi multi-tenant |
|---|---|---|
| Agent runtime | 1 process gateway spawn `claude` CLI, `--permission-mode bypassPermissions`, **full quyền trên máy host** | Một tenant = chiếm cả máy. KHÔNG THỂ share process |
| Workspace | 1 thư mục `~/goterm-workspace` chung | Tenant A đọc được file + memory của tenant B |
| Claude auth | 1 OAuth subscription của chủ máy | Share subscription cho khách = vi phạm ToS Anthropic + đụng rate limit lẫn nhau |
| Session | Keyed theo `chat_id` Telegram, 1 DB chung | Chưa có khái niệm tenant |
| Users | Bảng `users` (v4) chỉ để login dashboard | Chưa gắn với tenant/data scoping |
| Telegram | 1 bot token, whitelist user id | Mọi user nói chuyện với cùng 1 agent |
| Memory | 1 bộ MEMORY.md + memory/ chung | Bộ nhớ trộn lẫn giữa các user |

**Kết luận nền tảng**: vì agent có shell access, ranh giới bảo mật duy nhất đáng tin là **ranh giới OS-level (container/VM)**, không phải logic trong ứng dụng. Thiết kế xoay quanh nguyên tắc này.

## 3. Nguyên tắc thiết kế

1. **Isolation-first**: mỗi tenant một sandbox OS riêng (container/VM) + volume disk riêng. Không có đường code nào cho phép agent của tenant này đọc dữ liệu tenant khác — kể cả khi agent bị prompt-injection.
2. **BYOK (Bring Your Own Key)**: credentials AI là của tenant, chỉ tồn tại trong sandbox của tenant đó.
3. **Control plane / Data plane tách biệt**: gateway (điều phối, auth, billing) không bao giờ thực thi lệnh của agent; agent không bao giờ thấy DB điều phối.
4. **Tái sử dụng tối đa**: `internal/claude`, `internal/session`, `internal/memory`, `internal/bot` hiện tại trở thành **agent runtime** chạy TRONG container. Gateway hiện tại tiến hóa thành control plane.

## 4. Kiến trúc mục tiêu

```
                    ┌─────────────────────────── CONTROL PLANE ───────────────────────────┐
Internet ─HTTPS──>  │  bomclaw-gateway                                                     │
(Cloudflare Tunnel) │  ├── Auth (users/tenants, session cookie — đã có từ v4)             │
                    │  ├── Provisioner (tạo/stop/xóa agent container, quota)              │
                    │  ├── Router (tenant_id → agent endpoint)                            │
                    │  ├── Billing (Stripe webhook, tier enforcement)                     │
                    │  └── Fleet DB (SQLite → Postgres khi scale)                         │
                    └────────────┬────────────────────────────────────────────────────────┘
                                 │ mạng nội bộ (docker network, mTLS nội bộ)
        ┌────────────────────────┼────────────────────────┐
        ▼                        ▼                        ▼
┌─ tenant: alice ──────┐ ┌─ tenant: bob ────────┐ ┌─ tenant: carol ──────┐   DATA PLANE
│ agentd (bomclaw core)│ │ agentd               │ │ agentd               │
│ ├─ claude CLI (BYOK) │ │ ├─ claude CLI (BYOK) │ │ ├─ claude CLI (BYOK) │
│ ├─ telegram poller*  │ │ ├─ telegram poller*  │ │ ├─ telegram poller*  │
│ ├─ memory system     │ │ ├─ memory system     │ │ ├─ memory system     │
│ └─ /workspace (vol)  │ │ └─ /workspace (vol)  │ │ └─ /workspace (vol)  │
│ limits: 2cpu/4GB/40GB│ │ limits: 2cpu/4GB/40GB│ │ limits: 4cpu/8GB/80GB│
└──────────────────────┘ └──────────────────────┘ └──────────────────────┘
        * mỗi tenant dùng BOT TOKEN TELEGRAM RIÊNG (BYO bot) — xem §9
```

### 4.1 Control plane — `bomclaw-gateway` (tiến hóa từ gateway hiện tại)

- **Không spawn claude CLI nữa.** Chỉ điều phối.
- Expose: dashboard (multi-tenant), API quản trị, provisioning.
- Route request dashboard của user → agent container của tenant tương ứng (reverse proxy WS/HTTP tới `agentd`).
- Sở hữu: users, tenants, billing, audit log. **Không** sở hữu nội dung chat (nằm trong container của tenant).

### 4.2 Data plane — `agentd` (bomclaw core đóng gói lại)

Binary bomclaw hiện tại, chạy chế độ "agent" trong container:
- Toàn bộ code hiện có được giữ: claude CLI wrapper, session manager, memory system (MEMORY.md + daily notes + flush), Telegram bot, transcript.
- `--workspace /workspace` (volume riêng), `--data-dir /data` (SQLite riêng của tenant).
- Expose HTTP/WS trên mạng nội bộ để control plane proxy tới (dashboard chat).
- **Điểm hay**: một tenant của AaaS ≈ chính xác bomclaw single-tenant hôm nay. Không viết lại agent logic.

## 5. Tenant model — schema control plane (v5)

```sql
CREATE TABLE tenants (
  id           TEXT PRIMARY KEY,        -- slug, vd "alice"
  display_name TEXT NOT NULL,
  tier         TEXT NOT NULL DEFAULT 'starter',  -- starter|pro|max
  status       TEXT NOT NULL DEFAULT 'active',   -- active|suspended|deleted
  created_at   TEXT NOT NULL
);

-- users (v4) thêm cột:
ALTER TABLE users ADD COLUMN tenant_id TEXT REFERENCES tenants(id);
-- role hiện có (admin|viewer) trở thành role TRONG tenant; thêm role hệ thống:
ALTER TABLE users ADD COLUMN is_operator INTEGER DEFAULT 0;  -- vận hành platform

CREATE TABLE agent_instances (
  tenant_id     TEXT PRIMARY KEY REFERENCES tenants(id),
  container_id  TEXT DEFAULT '',
  state         TEXT NOT NULL DEFAULT 'stopped', -- provisioning|running|stopped|error
  endpoint      TEXT DEFAULT '',                 -- http://agent-alice:8080 (mạng nội bộ)
  cpu_limit     REAL, mem_limit_mb INTEGER, disk_limit_gb INTEGER,
  updated_at    TEXT NOT NULL
);

CREATE TABLE credentials (              -- BYOK, mã hóa envelope
  tenant_id  TEXT REFERENCES tenants(id),
  kind       TEXT NOT NULL,             -- anthropic_api_key | telegram_bot_token
  ciphertext BLOB NOT NULL,             -- AES-GCM, master key ngoài DB (file 0600/KMS)
  created_at TEXT NOT NULL,
  PRIMARY KEY (tenant_id, kind)
);

CREATE TABLE audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts TEXT, actor TEXT, tenant_id TEXT, action TEXT, detail TEXT
);
```

Nội dung chat/session/memory **không nằm ở control plane** — nằm trong SQLite + files của từng container (volume tenant). Control plane chỉ biết metadata.

## 6. Workspace & disk isolation

- Mỗi tenant 1 volume: `/data/tenants/<tenant_id>/` gồm `workspace/` (agent làm việc + memory) và `data/` (SQLite, transcripts của tenant).
- Mount vào container tại `/workspace`, `/data`. **Không mount bất kỳ path nào khác của host.**
- Quota theo tier (40/80/320 GB) — enforce bằng filesystem quota (XFS project quota trên Linux) hoặc volume size limit.
- Rootfs container **read-only**; chỉ `/workspace`, `/data`, `/tmp` ghi được.
- Backup: snapshot volume hằng ngày (restic/borg), retention theo tier — đây là feature bán được (MyClaw: "daily backups").
- Xóa tenant: stop container → giữ volume 30 ngày (grace) → xóa vĩnh viễn.

## 7. Sandbox runtime — lựa chọn & khuyến nghị

| Phương án | Isolation | Chi phí/khách | Ghi chú |
|---|---|---|---|
| **A. Container (Docker + gVisor/runsc)** ✅ đề xuất mặc định | Tốt (gVisor chặn syscall escape) | Thấp — 1 host Linux chạy được hàng chục tenant | Agent chạy `claude` CLI trong Linux container bình thường |
| B. microVM (Firecracker/Cloud Hypervisor) | Rất tốt | Trung bình | Cho tier Max hoặc khách enterprise |
| C. VM đầy đủ / VPS riêng (mô hình MyClaw) | Tốt nhất | Cao | Resale VPS + ansible; ít code nhất nhưng vận hành thủ công hơn |
| D. macOS sandbox-exec + OS user riêng | Yếu, API deprecated | — | ❌ Loại — Mac không phải nền tảng multi-tenant |

**Khuyến nghị**: dev/beta trên Mac hiện tại bằng OrbStack (đã cài) với Docker container thường (khách beta tin cậy); production thuê 1 server Linux (Hetzner/OVH ~$40-80/th cho 16-32GB RAM) chạy Docker + gVisor. Network của container: **egress-only**, chặn 169.254.0.0/16 (metadata), chặn LAN, chặn mạng nội bộ control plane (chỉ cho phép chiều gateway → agentd).

## 8. BYOK — credentials của khách

- Onboarding: khách dán `ANTHROPIC_API_KEY` (hoặc chạy `claude setup-token` OAuth của chính họ qua web terminal vào container của họ — hỗ trợ khách dùng subscription Claude Pro/Max cá nhân).
- Lưu: mã hóa AES-GCM trong bảng `credentials`, master key nằm ngoài DB (file mode 0600 trên host, sau này KMS). Giải mã CHỈ tại thời điểm inject env vào container của đúng tenant.
- **Tuyệt đối không** dùng chung OAuth token của operator cho khách: vi phạm ToS, chung rate-limit, và một khách prompt-injection có thể đốt quota của tất cả.
- Nếu muốn bán "AI included" sau này: mua API key doanh nghiệp, meter theo token (bảng usage), margin dương — để phase sau.

## 9. Channels per tenant

**Telegram — BYO bot token (khuyến nghị)**: mỗi tenant tạo bot riêng qua @BotFather (2 phút), dán token vào dashboard → agentd trong container của họ tự long-poll. Ưu điểm: tách hoàn toàn, không cần routing tập trung, đúng mô hình OpenClaw/MyClaw. (Phương án 1-bot-chung route theo `from.ID` bị loại: mọi tenant chung identity bot, rate limit chung, rủi ro route nhầm.)

**Dashboard**: login đã có (v4) → thêm tenant scoping: user thấy đúng sessions/agent của tenant mình; gateway proxy WS `wss://bot.bomclaw.org/t/<tenant>/ws` → `agentd` container tương ứng. Cookie/session giữ nguyên cơ chế hiện tại.

## 10. Tiers & billing

| | Starter | Pro | Max |
|---|---|---|---|
| Giá tham khảo | $15/th | $35/th | $69/th |
| vCPU / RAM | 1 / 2GB | 2 / 4GB | 4 / 8GB |
| Disk workspace | 20GB | 40GB | 80GB |
| Sandbox | container | container (gVisor) | microVM |
| Backup retention | 7 ngày | 30 ngày | 90 ngày |
| Kênh | Dashboard + Telegram | + WhatsApp/Discord (sau) | + API access |

- Stripe Checkout + webhook → `tenants.tier`, `tenants.status`.
- Hết hạn thanh toán: `suspended` → stop container (giữ volume), banner trên dashboard; xóa sau 30 ngày.
- Metering phase đầu: chỉ đo disk + uptime (đủ cho pricing cố định). Token là việc của khách (BYOK).

## 11. Threat model (tóm tắt)

| Mối đe dọa | Đối sách |
|---|---|
| Agent escape container (khách chạy code độc trong sandbox của chính họ) | gVisor/microVM, rootfs read-only, no-new-privileges, seccomp, không privileged, user namespace |
| Cross-tenant: đọc dữ liệu tenant khác | Volume riêng + docker network riêng per tenant; agentd chỉ nhận kết nối từ gateway |
| Prompt injection đánh cắp secrets | Secrets chỉ tồn tại trong env container của chính tenant; control plane không bao giờ đưa secrets vào prompt |
| SSRF từ agent vào control plane / metadata | Egress firewall: chặn RFC1918, 169.254.*, mạng docker của control plane |
| Chiếm control plane | Auth v4 + operator role riêng, audit_log, rate limit, Cloudflare trước mặt |
| Khách abuse (crypto mining, spam) | CPU/RAM limit theo tier, egress rate limit, ToS + suspend |

## 12. Lộ trình triển khai

- **Phase 0 — hoàn thành ✅**: single-tenant + dashboard auth + tunnel public (nền tảng control plane).
- **Phase 1 — Tenant hóa control plane** (~1 tuần): schema v5 (tenants, agent_instances, credentials, audit), users gắn tenant, dashboard scoping, CLI `bomclaw tenant add/list/suspend`. Agent vẫn chạy như cũ cho tenant đầu tiên (chính bạn).
- **Phase 2 — Agent runtime container hóa** (~2 tuần): Dockerfile cho `agentd` (bomclaw + claude CLI + node), volume layout §6, Provisioner (docker API: create/start/stop), gateway proxy `/t/<tenant>/`, BYOK flow + mã hóa credentials. Beta trên OrbStack với 2-3 khách tin cậy.
- **Phase 3 — Thương mại hóa** (~2 tuần): Stripe billing, tier enforcement (cpu/mem/disk limits), backup tự động, trang landing + onboarding self-service (tạo tenant → dán API key → dán bot token → chat).
- **Phase 4 — Production scale**: chuyển data plane sang server Linux (gVisor), monitoring (uptime per agent, alert), multi-host scheduler khi >50 tenant, cân nhắc Postgres cho control plane.

## 13. Rủi ro & câu hỏi mở

1. **Máy Mac M1 hiện tại không phải hạ tầng production** — OK cho beta ≤5 khách qua OrbStack; cần server Linux trước khi bán thật. (Chi phí: ~$50/th server đầu tiên ≈ hòa vốn ở 2-3 khách Pro.)
2. **Anthropic ToS**: BYOK là an toàn; "AI included" cần API key thương mại + kiểm tra kỹ điều khoản resale.
3. **Claude CLI trong container**: cần image có node + claude CLI; OAuth flow trong container cần web terminal hoặc `claude setup-token` — cần PoC sớm (rủi ro kỹ thuật lớn nhất của Phase 2).
4. **Support load**: mỗi khách một agent = mỗi khách một kiểu hỏng. Cần log tập trung (đọc được metadata, không đọc nội dung chat — privacy promise).
5. Mở: có hỗ trợ khách tự host model local (Ollama) không? Có cho phép custom skills/MCP per tenant không (tăng attack surface)?

---
*Tài liệu này là bản thiết kế để review trước khi bắt đầu Phase 1. Nguồn tham khảo pricing/mô hình: [MyClaw.ai](https://myclaw.ai/), [myclaw.ai/pricing](https://myclaw.ai/pricing), [myclaw.ai/openclaw](https://myclaw.ai/openclaw).*
