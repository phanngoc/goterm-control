# Design: ZeroClaw AaaS trên Docker (Mac, single-node) — PinchTab dùng chung, cô lập bằng tầng ứng dụng

**Trạng thái**: DRAFT v3 — design first, chưa implement.
**Ngày**: 2026-07-18
**Lịch sử**: v1 = k8s namespace-per-tenant → v2 = k8s 1 namespace → **v3 = bỏ k8s, Docker
thuần trên Mac** (quyết định của owner: tiết kiệm ~1-1.5GB overhead k8s control plane) và
**PinchTab dùng chung cho mọi user, cô lập bằng cơ chế profile riêng** (quyết định của
owner), gia cố bằng broker ở tầng ứng dụng.
**Liên quan**: `agents-as-a-service.md` (AaaS trên nền bomclaw), `browser-control.md`
(guard browser cho bomclaw — v3 tái dùng ý tưởng URL guard ở broker).

---

## 1. Tầm nhìn & ràng buộc

Dịch vụ "personal AI agent": mỗi user một instance **ZeroClaw** chạy trong container
Docker riêng, chat qua Telegram/dashboard, điều khiển browser qua **PinchTab dùng chung**.
Đội tàu do **control panel Rust (`clawpanel`)** quản lý qua Docker API.

Ràng buộc cứng (đã chốt với owner):
1. Chạy trên **máy Mac này, thông qua Docker** (Docker Desktop hoặc OrbStack — khuyến nghị
   OrbStack vì VM nhẹ hơn). Không Kubernetes.
2. Mỗi user = 1 container ZeroClaw (`ghcr.io/zeroclaw-labs/zeroclaw`).
3. **Một PinchTab duy nhất dùng chung** cho mọi user; độc lập dữ liệu browser giữa user
   dựa trên cơ chế **profile** của PinchTab (mỗi profile = user-data-dir Chrome riêng:
   cookies, login, storage tách nhau).
4. Panel Rust điều khiển vòng đời container, network, volume.
5. Cô lập data & security giữa user là bắt buộc, enforce bằng **tầng logic ứng dụng**
   (panel là single writer, broker chặn trước PinchTab, egress proxy) — vì Docker thuần
   không có namespace/NetworkPolicy/admission webhook như k8s.

Ngoài phạm vi: billing chi tiết, token pool/rotation (xem `agents-as-a-service.md` §8,
§12 — mặc định **BYO API key**).

## 2. Thành phần bên thứ ba — đã khảo sát

### 2.1 ZeroClaw

- Runtime AI assistant **Rust**, RAM idle thấp (~vài chục MB) → mật độ cao trên 1 máy.
- Config **TOML** `~/.zeroclaw/config.toml`; override bằng env `ZEROCLAW_<path>__<key>`.
- Channels: Telegram (long-poll outbound), CLI, HTTP/WS gateway + dashboard **:42617**.
- Image: `:latest` (distroless) / `:debian` (bash/git/curl — chọn bản này cho coding
  agent). State `/zeroclaw-data` (`memory/`, `sessions/`, `state/`, `.secret_key`).
- MCP client (`mcp.servers`), có tool `http` sẵn — dùng để gọi browser-broker.
- ⚠️ Đang tái kiến trúc → pin image theo digest.

### 2.2 PinchTab

- Go binary ~12–15MB, tự quản Chrome, HTTP API mặc định `127.0.0.1:9867`.
- Mô hình tài nguyên: **profiles** (browser state bền) → **instances** (process Chrome
  per profile, start/stop theo yêu cầu) → **tabs**. Endpoint chính:
  `POST /profiles`, `POST /instances/start`, `POST /instances/{id}/tabs/open`,
  `POST /tabs/{tabId}/action` (`{"kind":"click","ref":"e5"}`), `GET /tabs/{tabId}/snapshot`
  (a11y-tree, ref ổn định, ~800 token/trang).
- Chrome chỉ chạy khi instance được start → idle gần như miễn phí. Profile data tại `/data`.
- **Không có authentication, không có khái niệm tenant**: ai gọi được API là thấy được
  *mọi* profile/instance/tab. → Dùng chung nhiều user **bắt buộc phải có broker** đứng
  trước làm tenancy layer (§6). Đây là hệ quả thiết kế quan trọng nhất của v3.

## 3. Nguyên tắc thiết kế

1. **Panel là single writer của Docker**: chỉ `clawpanel` được mount `docker.sock`. Mọi
   container/network/volume đều do panel tạo từ template, gắn label `com.claw.tenant=<id>`,
   `com.claw.role=<agent|browser|broker|proxy|panel>`. Không ai `docker run` tay.
2. **Không có admission webhook trong Docker** → bù bằng **audit-reconcile loop**: panel
   định kỳ liệt kê toàn bộ container/network/volume, đối chiếu với trạng thái mong muốn
   trong DB; mọi sai khác (mount lạ, network attach lạ, container không label) → alert +
   tự sửa/cô lập.
3. **Tenant không bao giờ nói chuyện trực tiếp với PinchTab.** Toàn bộ browser traffic đi
   qua **browser-broker** (Rust): xác thực token per-tenant, map tenant → profile, chặn
   cross-tenant, ép quota, URL guard.
4. **Network cô lập bằng per-tenant internal network + multi-attach**: mỗi tenant một
   Docker network `internal: true` (không NAT ra ngoài); các service dùng chung (broker,
   egress-proxy, panel) được attach vào *từng* network tenant. Tenant A và B không chung
   network nào → không route tới nhau.
5. **Egress đi qua proxy có allowlist**: container tenant không có đường internet trực
   tiếp; ra ngoài qua egress-proxy (FQDN allowlist: LLM providers, Telegram). SSRF/scan
   LAN bị chặn ở đây.
6. **Tiết kiệm trước**: PinchTab một bản, Chrome on-demand, hibernate = `docker stop`,
   SQLite thay Postgres. Nhưng các chốt an ninh logic (broker auth, single-writer, audit
   loop) không được đánh đổi.

## 4. Kiến trúc tổng thể

```
 Mac ── Docker Desktop / OrbStack (Linux VM)
┌──────────────────────────────────────────────────────────────────────────┐
│                                                                          │
│  [clawpanel]  Rust ──► /var/run/docker.sock (duy nhất)                   │
│    ├─ REST API + Web UI  ◄──HTTPS── admin & users                        │
│    ├─ Reverse proxy dashboard: /t/{tenant}/ ─► agent-{tenant}:42617      │
│    ├─ Reconciler (bollard): DB desired state → docker containers        │
│    ├─ Audit loop: docker thực tế ⇄ desired state (phát hiện lệch)        │
│    └─ SQLite (tenants, secrets mã hoá, usage, audit)                     │
│                                                                          │
│  [egress-proxy]        FQDN allowlist (anthropic, openrouter, telegram…) │
│    attach: net-alice, net-bob, …, bridge(ra internet)                    │
│                                                                          │
│  [browser-broker] Rust  ──► [pinchtab] :9867 ──► Chrome (per profile)    │
│    attach: net-alice,        attach: net-browser (chỉ broker+pinchtab)   │
│            net-bob, …,       volume: pinchtab-data (profile per tenant)  │
│            net-browser                                                   │
│    auth: Bearer token/tenant; map tenant→profile; URL guard; quota       │
│                                                                          │
│  net-alice (internal)              net-bob (internal)                    │
│  ┌────────────────────┐            ┌────────────────────┐                │
│  │ [agent-alice]      │            │ [agent-bob]        │                │
│  │  zeroclaw :42617   │            │  zeroclaw :42617   │                │
│  │  volume data-alice │            │  volume data-bob   │                │
│  └────────────────────┘            └────────────────────┘                │
│                                                                          │
│  Đường đi: agent ──net-tenant──► broker ──net-browser──► pinchtab        │
│            agent ──net-tenant──► egress-proxy ──bridge──► internet       │
│            agent-A ⇄ agent-B : KHÔNG TỒN TẠI đường route                 │
└──────────────────────────────────────────────────────────────────────────┘
   User Telegram ⇄ long-poll outbound (qua egress-proxy), không cần mở port vào
```

## 5. Data plane — container per tenant

### 5.1 Container spec (panel render từ template)

```
docker run (tương đương — panel gọi bollard):
  name: agent-alice
  image: ghcr.io/zeroclaw-labs/zeroclaw@sha256:<pinned>     # :debian
  labels: com.claw.tenant=alice, com.claw.role=agent
  networks: [net-alice]                                     # internal, duy nhất
  volumes:  data-alice:/zeroclaw-data                       # named volume, duy nhất
  env:
    ZEROCLAW_providers__models__anthropic__default__api_key=…   # từ secret store panel
    ZEROCLAW_channels__telegram__default__token=…
    HTTP_PROXY=http://egress-proxy:3128                     # ép mọi egress qua proxy
    HTTPS_PROXY=http://egress-proxy:3128
    NO_PROXY=browser-broker,egress-proxy
    CLAW_BROWSER_URL=http://browser-broker:8700             # cho skill/MCP browser
    CLAW_BROWSER_TOKEN=<token-alice>                        # bearer per tenant
  security:
    user: non-root; no-new-privileges; cap-drop ALL
    read-only rootfs + tmpfs /tmp (image debian cho phép)
    memory limit 768m, cpus 1.0, pids-limit 256
  restart: unless-stopped
```

Điểm chốt:
- **Không mount docker.sock, không host network, không privileged** — bất biến (D-series §7.2).
- Docker **không có Secret object** như k8s → secrets (API key, Telegram token, browser
  token) sống trong **secret store mã hoá của panel** (SQLite + AES-GCM, master key trong
  file quyền 0600 hoặc macOS Keychain), chỉ giải mã lúc render env khi tạo container.
  `docker inspect` thấy env — chấp nhận được vì chỉ panel có socket.
- Naming convention là bất biến: tenant `alice` ⇒ `agent-alice`, `net-alice`, `data-alice`,
  token browser riêng. Audit loop đối chiếu đúng bộ ba này.
- Docker address pool cấu hình sẵn (vd `10.200.0.0/16` chia `/28`) để đủ subnet cho
  hàng trăm network tenant.

### 5.2 Config injection

Panel render `config.toml` (mount read-only từ thư mục config do panel quản) + env
override `ZEROCLAW_…` (env thắng file). `.secret_key` của zeroclaw sinh lần đầu trên
volume tenant.

## 6. Browser plane — PinchTab dùng chung + browser-broker

### 6.1 Phân vai

```
[zeroclaw alice] ──token alice──► [browser-broker] ──► [pinchtab] ──► Chrome(profile alice)
[zeroclaw bob]   ──token bob────►        │                         └─► Chrome(profile bob)
                                         │
                              tenancy layer (Rust):
                              1. AuthN: Bearer token → tenant
                              2. Mapping: tenant → profile pinchtab (tạo lần đầu)
                              3. AuthZ: mọi instanceId/tabId trong request phải
                                 thuộc profile của tenant đó (broker giữ bảng sở hữu)
                              4. Quota: ≤1 instance/tenant, ≤K instance toàn máy,
                                 idle timeout → stop instance
                              5. URL guard: chặn navigate tới private CIDR,
                                 metadata, localhost (như browser-control.md §6.3)
                              6. Audit: log (tenant, action, url) — không log nội dung
```

- **Cô lập dữ liệu browser** = profile PinchTab (mỗi profile một user-data-dir Chrome:
  cookie/login/history tách nhau) — đúng cơ chế owner chọn.
- **Cô lập truy cập API** = broker. Thiếu broker thì mọi tenant thấy tab của nhau vì
  PinchTab không auth (§2.2). Broker là thành phần *bắt buộc*, không phải tối ưu.
- PinchTab container chỉ nằm trên `net-browser` cùng broker — không tenant nào route tới
  :9867 trực tiếp.

### 6.2 API broker (phác thảo — cố ý hẹp hơn PinchTab)

```
POST /v1/browser/navigate   {url}                → mở/tái dùng instance+tab của tenant
GET  /v1/browser/snapshot   ?filter=interactive  → a11y snapshot (ref e1,e2…)
POST /v1/browser/act        {kind, ref, text?}   → click/fill/press…
GET  /v1/browser/tabs       / POST open / close  → trong phạm vi profile tenant
GET  /v1/browser/screenshot
POST /v1/browser/stop                            → trả Chrome về idle
```

Không expose endpoint quản trị (`/profiles`, `/instances` của PinchTab) cho tenant.
Giai đoạn 1: zeroclaw gọi broker bằng tool `http` + skill mô tả API. Giai đoạn 2:
broker expose thêm **MCP transport** để khai vào `mcp.servers` của zeroclaw → tool
`browser__*` có schema chuẩn (kiểm chứng transport zeroclaw hỗ trợ ở P0 — câu hỏi mở #2).

### 6.3 Giới hạn thẳng thắn của mô hình dùng chung

Profile cô lập **dữ liệu**, không phải **ranh giới an ninh runtime**: mọi Chrome process
của mọi tenant chạy chung một container PinchTab. Một trang web độc khai thác được lỗ
hổng Chrome (RCE) trong phiên của tenant A ⇒ code chạy trong container pinchtab ⇒ đọc
được profile của *mọi* tenant. Giảm nhẹ:
- Chrome/PinchTab pin bản mới, cập nhật nhanh (Chrome vá RCE liên tục);
- container pinchtab: non-root, cap-drop, no-new-privileges, chỉ có `net-browser`
  + egress qua proxy (hạn chế exfil);
- **escape hatch trong thiết kế**: schema đã có `browser_mode: shared | dedicated` per
  tenant — tenant nhạy cảm/trả tiền cao cấp được cấp pinchtab container riêng (panel chỉ
  cần đổi template, broker trỏ endpoint khác). Mặc định: shared (quyết định owner).

Rủi ro tồn dư này được chấp nhận có chủ đích để đổi lấy mật độ (ghi ở §10.3).

## 7. Cô lập & network — bảng bù cho việc không có k8s

### 7.1 Ma trận cô lập

| Tầng | Cơ chế | Chống gì |
|------|--------|----------|
| Compute | container riêng per tenant, non-root, cap-drop, pids/mem/cpu limit | noisy neighbor, leo thang trong container |
| Disk | named volume per tenant, chỉ mount vào container của chính tenant (audit loop kiểm) | user đụng phân vùng nhau |
| Browser data | profile PinchTab per tenant | lẫn cookie/session giữa user |
| Browser API | broker: token per tenant + bảng sở hữu instance/tab | tenant A điều khiển browser tenant B |
| Network | per-tenant internal net; service chung multi-attach; không ICC chéo | lateral movement |
| Egress | proxy FQDN allowlist; tenant net không NAT trực tiếp | SSRF, scan LAN/metadata, exfil bừa |
| Docker API | chỉ panel mount docker.sock | tenant thao túng hạ tầng |
| Secrets | store mã hoá trong panel, giải mã lúc create container | đọc chéo credential |
| Host Mac | Docker VM boundary (Desktop/OrbStack) | sự cố tồi nhất bị nhốt trong VM |

### 7.2 Bất biến (D1–D8) — panel enforce khi tạo + audit loop kiểm định kỳ

```
D1. Container role=agent: đúng 1 label tenant, đúng 1 network net-<tenant>,
    đúng 1 volume data-<tenant>. Không mount gì khác.
D2. Không container nào ngoài clawpanel được mount docker.sock.
D3. Không container tenant nào: privileged, host network/pid/ipc, cap-add,
    mount đường dẫn host.
D4. pinchtab chỉ attach net-browser; không publish port nào ra host.
D5. broker là container duy nhất attach đồng thời net-browser + các net tenant.
D6. Mọi container thuộc hệ thống phải có label com.claw.* ; container lạ trong
    phạm vi quản lý → alert.
D7. Image chỉ từ digest allowlist trong DB panel.
D8. Port publish ra host: duy nhất clawpanel :443 (API/UI/proxy). Không gì khác.
```

Audit loop chạy mỗi 30–60s: `list containers/networks/volumes` → so khớp desired state
→ lệch thì sửa (stop/disconnect) + ghi audit + notify. Đây là "webhook thay thế" của v3.

### 7.3 Egress-proxy

- Squid hoặc proxy Rust nhỏ (ưu tiên viết Rust chung repo — CONNECT proxy + allowlist,
  ~vài trăm dòng, khỏi kéo image lạ).
- Per-tenant policy: allowlist mặc định (`api.anthropic.com`, `openrouter.ai`,
  `api.telegram.org`, registry skill…) + tenant mở rộng qua panel (có audit).
- Chrome trong pinchtab cũng cần đi qua proxy (container pinchtab nằm trên net nội bộ):
  PinchTab cho đặt Chrome binary/flags hay proxy per-profile → kiểm chứng ở P0; nếu
  không đặt được `--proxy-server` per instance thì cho pinchtab attach bridge (egress
  trực tiếp) và dựa vào URL guard của broker + chấp nhận residual DNS-rebinding
  (câu hỏi mở #3).

## 8. Control panel — `clawpanel` (Rust)

| Lớp | Chọn | Ghi chú |
|-----|------|---------|
| Docker API | **bollard** | async, đầy đủ container/network/volume/events |
| HTTP API + UI + reverse proxy | **axum** + tower | một framework ba vai |
| DB | **SQLite + sqlx** (WAL) | repo pattern để lên Postgres khi cần |
| Secret store | AES-GCM trong SQLite, master key file 0600 / macOS Keychain | Docker không có Secret object |
| AuthN | password/magic-link tenant; 2FA admin | |
| Observability | `tracing` + file log xoay vòng; metrics endpoint | không bắt buộc Prometheus |

Một binary, các task: API/UI, reverse proxy dashboard, reconciler (desired state trong DB
→ Docker qua bollard), audit loop (§7.2), capacity ledger.

- **Capacity ledger**: budget máy (CPU/RAM cấp cho Docker VM) − Σ limits đã cấp; vượt →
  từ chối provision/resume. Cộng thêm semaphore Chrome toàn máy (≤K instance, K≈6–8).
- **Reverse proxy dashboard**: user → panel (auth session, scope tenant) → attach sẵn vào
  net tenant → `agent-<tenant>:42617`. Chỉ panel publish port ra host (D8).
- **API** (rút gọn): `POST/GET/PATCH/DELETE /api/tenants`, `PUT /api/tenants/{id}/secrets/*`
  (write-only), `/usage`, `/health`, `/restart`, `GET /api/audit`. DB:
  `tenants/plans/secrets(enc)/usage_daily/audit_log/images_allowlist`.
- **Flow provision**: INSERT tenant → reconciler: volume → network → (connect broker+proxy
  vào net mới) → container agent → broker cấp token + tạo profile pinchtab lần đầu dùng
  → Ready, hướng dẫn nhập Telegram token/API key. Mục tiêu < 30s (image pre-pulled).

## 9. Vòng đời & vận hành

- **Hibernate** = `docker stop agent-<t>` (volume giữ; Telegram offset trong state → wake
  đọc backlog). Auto-hibernate sau N giờ idle — đòn bẩy mật độ chính. Chrome instance
  idle do broker tự stop (timeout).
- **Delete**: soft-delete → 30 ngày → xoá container/network/volume + profile pinchtab
  (broker gọi xoá profile) theo label selector; orphan-sweep nằm sẵn trong audit loop.
- **Upgrade zeroclaw/pinchtab**: đổi digest trong allowlist → reconciler recreate theo
  ring (vài tenant canary trước). PinchTab nâng bản cần đọc changelog phần bind/auth (§10.2).
- **Backup**: job định kỳ `docker run --rm -v data-<t>:/src -v backup:/dst tar` + restic
  ra đĩa ngoài; pinchtab-data backup cả cụm (profile theo thư mục).
- **Sự cố máy Mac ngủ/restart**: Docker Desktop/OrbStack tự khởi động lại, container
  `restart: unless-stopped`; panel lên trước (depends) rồi audit loop tự đối soát.
- **Ước tính mật độ** (VM Docker cấp 8 CPU / 12–16Gi):
  - Hạ tầng thường trực: panel + broker + proxy + pinchtab idle ≈ **300–400Mi** (so với
    ~1.5Gi nếu còn k8s).
  - Tenant idle ≈ zeroclaw ~50–128Mi → **70–100 tenant idle** về RAM.
  - Chrome ~400Mi–1Gi/phiên → **6–8 phiên browse đồng thời** (semaphore K).

## 10. Threat model & rủi ro chính

| # | Mối đe doạ | Kiểm soát | Tồn dư |
|---|------------|-----------|--------|
| 1 | Tenant A điều khiển browser/tab của B | broker auth + bảng sở hữu (§6.1); pinchtab không route trực tiếp (D4/D5) | thấp |
| 2 | Cross-tenant network | per-tenant internal net, không net chung giữa tenant | thấp |
| 3 | **Chrome RCE trong pinchtab chung** → đọc mọi profile | pin/update nhanh, hardening container, egress proxy chặn exfil; escape hatch `dedicated` | **trung bình — chấp nhận có chủ đích** (quyết định shared của owner) |
| 4 | SSRF/scan LAN/metadata (prompt injection) | tenant net internal + egress-proxy allowlist; URL guard broker cho Chrome | DNS-rebinding nếu Chrome không qua proxy (mở #3) |
| 5 | Tenant đụng volume nhau | D1 + audit loop; named volume chỉ mount bởi panel | thấp |
| 6 | Chiếm panel = chiếm tất cả (docker.sock = root VM) | panel là bề mặt tấn công chính: 2FA admin, secrets mã hoá, audit, cập nhật deps (cargo-audit), chỉ :443 publish | trung bình — SPOF có chủ đích |
| 7 | Thao tác docker tay phá bất biến | D-series + audit loop tự sửa + alert | thấp |
| 8 | Noisy neighbor | mem/cpu/pids limit + capacity ledger + Chrome semaphore | thấp |
| 9 | Supply chain (image) | D7 digest allowlist | thấp |
| 10 | ZeroClaw upstream gãy API config | pin digest, canary ring | thấp |

So với k8s (v2): mất PSA/webhook/NetworkPolicy chuẩn → thay bằng D-series + audit loop
+ broker, tất cả nằm trong code panel ⇒ **test suite cho D1–D8 và broker AuthZ là
deliverable bắt buộc của P1**, không phải nice-to-have.

## 11. Lộ trình

| Phase | Nội dung | Nghiệm thu |
|-------|----------|------------|
| **P0 — PoC** (tay, docker compose) | 2 tenant: agent+net+volume; pinchtab + broker stub (auth token cứng); egress-proxy; zeroclaw gọi broker qua tool http | A không gọi được B (:42617), không thấy tab của B, không ra được LAN/private; browse OK; đo RAM idle |
| **P1 — Panel MVP** | clawpanel: bollard reconciler + audit loop D1–D8 + API/UI + secret store + capacity ledger + hibernate; broker hoàn chỉnh (ownership, quota, URL guard) | provision < 30s không docker tay; test suite D-series + broker AuthZ pass |
| **P2 — Product hoá** | broker MCP transport; reverse-proxy dashboard + auth; metering; backup restic; Chrome semaphore tinh chỉnh; `browser_mode: dedicated` | 20–50 tenant thật trên máy này |
| **P3 — Mở rộng** | billing; managed-key proxy; đường lên multi-node (Postgres + quay lại k8s design v2 nếu vượt 1 máy) | theo traction |

## 12. Câu hỏi mở

1. OrbStack hay Docker Desktop? (khuyến nghị OrbStack: VM nhẹ, network nhanh hơn trên Mac;
   Docker Desktop phổ biến hơn). Budget CPU/RAM cấp cho VM là bao nhiêu?
2. zeroclaw `mcp.servers` hỗ trợ transport nào (stdio only hay có HTTP/streamable)? Quyết
   cách broker expose MCP ở P2; P0-P1 dùng tool `http` là đủ.
3. PinchTab có cho đặt Chrome `--proxy-server` (per profile/instance) không? Quyết việc
   Chrome đi qua egress-proxy hay chỉ dựa URL guard (§7.3).
4. Giới hạn profile/instance của PinchTab khi ~50-100 profile trong một container? (RAM
   map profile registry, tốc độ start Chrome) — đo ở P0.
5. Telegram long-poll (giữ container luôn chạy) vs webhook qua panel (wake-from-hibernate,
   tăng mật độ) — MVP long-poll; webhook đáng làm ở P2 nếu mật độ hibernate quan trọng.
6. Panel có cần chế độ read-only cho owner xem hội thoại tenant không? (mặc định thiết kế:
   KHÔNG — sessions nằm trong volume tenant, panel không đọc; nếu cần support phải có cơ
   chế consent per tenant.)

---

*Nguồn khảo sát: deepwiki `zeroclaw-labs/zeroclaw`; `github.com/pinchtab/pinchtab` README
+ pinchtab.com/docs (2026-07); các bản thiết kế trước: `agents-as-a-service.md`,
`browser-control.md`, và v1/v2 của chính file này (lịch sử git).*
