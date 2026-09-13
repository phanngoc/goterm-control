# Browser Control — Gap Analysis vs OpenClaw & Thiết kế nâng cấp

**Trạng thái**: DRAFT — design only, chưa implement.
**Ngày**: 2026-07-18
**Câu hỏi gốc**: BomClaw đã hỗ trợ control browser giống OpenClaw chưa?

**Trả lời ngắn**: **Đã có, ở mức cơ bản (~60%)**. BomClaw có sẵn 12 tool `browser_*`
chạy trên native CDP (không Playwright). Thiếu so với OpenClaw: security guard
(SSRF/navigation policy), quản lý tab/dialog/download, profiles, trusted input
events, và config. **Bug nghiêm trọng nhất: không chạy được trên macOS** —
`chromePaths` chỉ có đường dẫn Linux, trong khi gateway đang deploy trên macOS.

---

## 1. Bối cảnh & mục tiêu

BomClaw là gateway bridge chat channel (Telegram, CLI) → agent loop → tool
executor chạy local. Browser control cho phép agent thao tác web thay user:
đọc trang JS-heavy, điền form, thao tác trên session đã đăng nhập.

OpenClaw (open-source assistant cùng phân khúc) được lấy làm **kiến trúc tham
chiếu** vì browser tool của nó trưởng thành nhất trong nhóm: profiles cách ly,
SSRF policy, tab management, dialog/upload/download, extension relay.

Mục tiêu tài liệu này:
1. Chốt hiện trạng browser control của BomClaw (có gì, thiếu gì — evidence cụ thể).
2. Gap analysis so với OpenClaw.
3. Thiết kế phần cần bổ sung, kèm roadmap — **không implement trong scope này**.

---

## 2. Hiện trạng BomClaw (verified 2026-07-18)

### 2.1 Cấu trúc code

```
internal/browser/
├── chrome.go       # Launcher: tìm binary, spawn với --remote-debugging-port=9222,
│                   #   user-data-dir ~/.goterm/browser/user-data, poll CDP ready
├── cdp.go          # WithCdpSocket: mở WebSocket tới tab, multiplex request/response
└── operations.go   # Navigate, CaptureScreenshot, EvalJS, SnapshotDOM (ref n1,n2...),
                    #   Click/Fill/TypeText/SelectOption (JS-injection), Scroll,
                    #   GetText, GoBack, WaitFor

internal/tools/browser.go   # BrowserTool: EnsureChrome() lazy-launch, Shutdown()
cmd/bomclaw/main.go:507-519 # Đăng ký 12 tool defs
cmd/bomclaw/main.go:861-903 # JSON schema cho từng tool
```

Dependencies: `chromedp/cdproto` (chỉ dùng type/protocol constants), `gorilla/websocket`.
**Không dùng Playwright, không dùng MCP** — tool chạy in-process trong executor.

### 2.2 Tool đã có (12)

| Tool | Chức năng | Cơ chế |
|------|-----------|--------|
| `browser_navigate` | Mở URL, auto-launch Chrome | CDP `Page.navigate` |
| `browser_snapshot` | DOM tree + element refs (n1, n2…) | JS injection |
| `browser_click` | Click theo ref | JS `element.click()` |
| `browser_fill` | Clear + gõ text vào input | JS value setter |
| `browser_type` | Append text | JS value setter |
| `browser_select` | Chọn option dropdown | JS |
| `browser_scroll` | Cuộn trang | JS |
| `browser_screenshot` | Chụp trang (PNG) | CDP `Page.captureScreenshot` |
| `browser_get_text` | Lấy text/HTML/value/title/URL | JS |
| `browser_eval` | Chạy JavaScript tùy ý | CDP `Runtime.evaluate` |
| `browser_wait` | Chờ ref/text/timeout | Poll JS |
| `browser_back` | Back history | JS `history.back()` |

### 2.3 Vấn đề đã xác nhận trong code hiện tại

1. **Không chạy trên macOS** — `chrome.go:27-37` chỉ liệt kê
   `/usr/bin/google-chrome`, `/snap/bin/chromium`… (Linux). Fallback
   `exec.LookPath("google-chrome")` cũng không match trên macOS
   (binary nằm ở `/Applications/Google Chrome.app/Contents/MacOS/`).
   Gateway hiện deploy bằng launchd trên macOS → **browser tools đang chết
   trên môi trường production chính**.
2. **CDP port cố định 9222** (`chrome.go:17`) — xung đột nếu user/tool khác
   đang chạy Chrome debug; hai instance BomClaw không chạy song song được.
3. **Không có bất kỳ guard nào** — agent có thể navigate tới
   `http://169.254.169.254/`, `http://localhost:8443` (dashboard/gateway của
   chính BomClaw), `file://` path. Kết hợp prompt-injection từ nội dung web
   → SSRF/local access. Nghiêm trọng vì gateway chạy daemon với
   `--permission-mode bypassPermissions`.
4. **Interaction bằng JS injection, không phải trusted events** — `element.click()`
   và value-setter fail trên site check `isTrusted` (nhiều SPA, anti-bot,
   payment form). OpenClaw/Playwright dùng input events thật.
5. **Chrome orphan** — `chrome.go:98` set `Setpgid: true` (Chrome tách process
   group để sống sót khi gateway crash giữa launch), nhưng khi gateway crash
   thật thì không ai kill Chrome → orphan giữ port 9222, instance mới launch
   fail. Cùng class bug với orphan Claude CLI đã ghi trong `CLAUDE.md`.
6. **Screenshot path cố định** `/tmp/browser-screenshot.png`
   (`internal/tools/browser.go:13`) — hai session chụp cùng lúc ghi đè nhau.
7. **Không có config** — không tắt được browser, không đổi được binary path,
   không có headless mode. Chrome luôn mở cửa sổ trên desktop của máy chạy
   gateway.
8. **Chỉ thao tác 1 tab** — `PageWsURL()` lấy tab "page" đầu tiên; popup/tab
   mới do site mở ra là agent mất kiểm soát.

---

## 3. Kiến trúc tham chiếu: OpenClaw browser control

Tóm tắt (nguồn: deepwiki openclaw/openclaw, wiki §3.4.4):

- **Hybrid CDP + Playwright**: CDP cho screenshot/eval/ARIA snapshot;
  Playwright (connect qua CDP) cho click/type/drag/download — trusted input.
- **Profiles**: `openclaw` (managed, cách ly khỏi profile cá nhân — mặc định),
  `user` (attach Chrome đang chạy qua CDP), `chrome` (extension relay),
  custom `cdpUrl` (browserless/grid). Config `browser.profiles.<name>`.
- **Tool actions**: navigate/open/focus/close tab; snapshot (AI/ARIA format,
  `--urls`); screenshot (`--full-page`, `--ref`, `--labels`); click/type/press/
  hover/drag/select/fill; upload/download/dialog; evaluate (tắt được bằng
  `browser.evaluateEnabled`); set cookies/storage/timezone/locale/media/offline;
  resize viewport.
- **Security**: SSRF policy trên mọi navigation target + CDP endpoint
  (`browser.ssrfPolicy`: chặn private network mặc định, `hostnameAllowlist`);
  `navigation-guard` kiểm cả redirect chain; loopback HTTP API có
  shared-secret auth; sandbox agent mặc định không được control host browser.
- **Ops**: `doctor` health check, `tabCleanup` tự dọn tab idle,
  `browser.headless`, `browser.executablePath`.
- **Exposure**: tool là bundled plugin trong agent loop + standalone loopback
  HTTP API (opt-in) cho CLI/service khác gọi.

---

## 4. Gap analysis

| Năng lực | OpenClaw | BomClaw | Gap |
|----------|----------|---------|-----|
| Launch managed Chrome, profile cách ly | ✅ | ✅ (`~/.goterm/browser/user-data`) | — |
| Hỗ trợ macOS | ✅ | ❌ Linux-only paths | **P0** |
| Navigate / snapshot có ref / click / fill / select / scroll / eval / wait / back | ✅ | ✅ | — |
| Screenshot full-page | ✅ | ✅ | — |
| SSRF / navigation guard | ✅ policy + redirect check | ❌ | **P0** |
| Tắt `eval` qua config | ✅ | ❌ | **P0** |
| Config (enabled/headless/executable_path/port) | ✅ | ❌ hardcode | **P0** |
| Chrome lifecycle sạch (không orphan, health check) | ✅ doctor | ⚠️ orphan risk | **P1** |
| Trusted input events (click/type thật) | ✅ Playwright | ❌ JS injection | **P1** |
| Tab management (list/open/focus/close) | ✅ | ❌ 1 tab | **P1** |
| Dialog (alert/confirm/prompt) handling | ✅ | ❌ dialog treo là kẹt | **P1** |
| Download / upload file | ✅ | ❌ | **P2** |
| Hover / press key / drag | ✅ | ❌ | **P2** |
| Attach Chrome đang chạy của user (`cdp_url`) | ✅ | ❌ | **P2** |
| Named profiles nhiều cấu hình | ✅ | ❌ | **P3** |
| Set cookies / storage / viewport / timezone | ✅ | ❌ | **P3** |
| Extension relay | ✅ | ❌ | Không làm |
| Standalone loopback HTTP API | ✅ | ❌ | Không làm |
| AI-format snapshot / PDF export | ✅ | ❌ | Không làm |

Kết luận: **nền tảng đã tương đương** (managed Chrome + CDP + ref-based
snapshot/act — đúng mô hình OpenClaw dùng), khác biệt nằm ở **độ an toàn,
độ tương thích và độ bền vận hành**, không phải ở kiến trúc.

---

## 5. Nguyên tắc thiết kế

1. **Giữ native CDP, không thêm Playwright** — Playwright kéo theo Node runtime
   hoặc playwright-go (kém maintain); mọi thứ Playwright làm qua CDP thì
   BomClaw gọi CDP trực tiếp được (Input domain, Target domain, Page domain).
   Trade-off chấp nhận: tự viết nhiều hơn, không có auto-wait tinh vi.
2. **Không mở HTTP API riêng cho browser** — tool chạy in-process trong
   executor, không có consumer thứ hai. YAGNI. (Khác OpenClaw vì OpenClaw có
   CLI/plugin ecosystem cần gọi từ ngoài.)
3. **Security mặc định chặt** — gateway chạy daemon không có người ngồi cạnh
   approve; nội dung web là input không tin được (prompt injection). Mặc định:
   chặn private network, tắt được eval, allowlist opt-in.
4. **Config-driven, zero-config vẫn chạy** — không có section `browser:` trong
   `config.yaml` thì behavior như hiện tại (trừ guard bật mặc định).
5. **Từng phase độc lập ship được** — không big-bang.

---

## 6. Thiết kế đề xuất

### 6.1 Config schema (mới — `internal/config/config.go`)

```yaml
browser:
  enabled: true                # false = không đăng ký browser_* tools
  executable_path: ""          # override; rỗng = auto-detect
  headless: false              # true = --headless=new
  cdp_port: 0                  # 0 = chọn port ephemeral (fix xung đột 9222)
  attach_url: ""               # CDP URL có sẵn (browserless/Chrome user) — P2
  eval_enabled: true           # false = bỏ đăng ký browser_eval
  guard:
    allow_private_network: false   # chặn RFC1918, loopback, link-local, file://
    hostname_allowlist: []         # ["*.internal.corp"] — bypass guard có chủ đích
    hostname_blocklist: []         # chặn thêm ngoài private network
```

### 6.2 P0.a — macOS support + port động

`FindChrome()` thêm nhánh theo `runtime.GOOS`:

```go
// darwin
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
"/Applications/Chromium.app/Contents/MacOS/Chromium",
```

Port: bind `net.Listen("tcp", "127.0.0.1:0")` lấy port trống → đóng → truyền
vào `--remote-debugging-port`. Lưu port vào struct `Chrome` (đã có field).
`config.browser.cdp_port != 0` thì dùng giá trị cố định (debug).

### 6.3 P0.b — Navigation guard (SSRF policy)

Module mới `internal/browser/guard.go`:

```
CheckURL(rawURL, policy) error
  ├─ scheme ∉ {http, https}         → deny (chặn file://, chrome://, javascript:)
  ├─ hostname match allowlist        → allow
  ├─ hostname match blocklist        → deny
  ├─ resolve DNS → mọi IP là private/loopback/link-local/metadata
  │    && !allow_private_network     → deny
  └─ else                            → allow
```

Điểm hook:
1. `Navigate()` — check trước khi gửi `Page.navigate`.
2. **Redirect + client-side navigation**: sau navigate và trước mỗi
   snapshot/act, đọc URL hiện tại; nếu vi phạm policy → trả lỗi
   `navigation blocked by guard: <url>` và tự navigate về `about:blank`.
   (Đơn giản hơn OpenClaw — không chặn realtime từng redirect hop, nhưng chặn
   được việc agent *thao tác tiếp* trên trang cấm; DNS-rebinding nâng cao ghi
   nhận ở §8.)
3. `browser_eval` bị loại khỏi tool list khi `eval_enabled: false` — vì eval
   là con đường vòng qua guard (`fetch()` trong page context).

### 6.4 P1.a — Lifecycle: hết orphan + doctor

- Ghi PID + port vào `~/.goterm/browser/chrome.json` khi launch. Khi
  `EnsureChrome()` chạy mà file tồn tại: probe `/json/version` — sống thì
  **adopt** (reuse) thay vì launch mới; chết thì xóa file, kill PID nếu còn.
  → gateway restart không tạo orphan, không chết vì port bận.
- `Shutdown()` kill cả process group (`syscall.Kill(-pgid, SIGTERM)`) — hiện
  chỉ signal process chính.
- Thêm reachability check vào lệnh `bomclaw doctor`/status hiện có (nếu có):
  binary tìm thấy?, CDP reachable?, guard config hợp lệ?

### 6.5 P1.b — Trusted input qua CDP Input domain

Thay JS injection bằng CDP thật cho các action tương tác:

| Action | Hiện tại | Đề xuất |
|--------|----------|---------|
| click | `el.click()` | scrollIntoView → `DOM.getBoxModel` lấy tọa độ → `Input.dispatchMouseEvent` (moved, pressed, released) |
| fill/type | value setter + event giả | focus element → `Input.insertText` / `Input.dispatchKeyEvent` từng phím |
| press (mới) | — | `Input.dispatchKeyEvent` (Enter, Tab, Escape…) |
| hover (mới) | — | `Input.dispatchMouseEvent` type=mouseMoved |

Ref resolution giữ nguyên cơ chế snapshot hiện có (map ref → node), chỉ đổi
tầng thực thi. JS-injection giữ làm fallback khi `getBoxModel` fail
(element ẩn/0-size).

### 6.6 P1.c — Tab + dialog

- Tool mới `browser_tabs` `{action: list|open|focus|close, url?, tab_id?}` —
  dùng CDP `Target.getTargets / createTarget / activateTarget / closeTarget`.
  `Chrome` struct thêm `activeTargetID`; mọi operation route theo target đang
  focus thay vì "tab page đầu tiên".
- Dialog: subscribe `Page.javascriptDialogOpening` trên socket; auto-dismiss
  mặc định + ghi message vào tool result (`[dialog dismissed: "..."]`).
  Tool mới `browser_dialog` `{action: accept|dismiss, text?}` cho case agent
  cần accept có chủ đích. → hết deadlock khi site bật `confirm()`.

### 6.7 P2 — Download/upload, attach existing browser

- Download: `Browser.setDownloadBehavior` → thư mục
  `~/.goterm/browser/downloads/<session>/`; event `Browser.downloadProgress`
  → tool `browser_download_wait` trả path file.
- Upload: `DOM.setFileInputFiles` theo ref → tool `browser_upload {ref, paths}`.
  Chỉ cho phép path trong workspace của session (tránh exfil file hệ thống).
- `attach_url` trong config: skip launch, connect thẳng CDP URL — dùng được
  browserless/container hoặc Chrome thật của user (user tự mở với
  `--remote-debugging-port`). Guard vẫn áp dụng.

### 6.8 P3 — Nice-to-have (chưa cam kết)

Named profiles, set cookies/storage/viewport/timezone, snapshot kèm URL list.
Chỉ làm khi có use case thật từ vận hành.

### 6.9 Những gì KHÔNG làm theo OpenClaw (quyết định chủ động)

| Thứ OpenClaw có | Lý do bỏ |
|-----------------|----------|
| Playwright layer | §5.1 — CDP thuần đủ, tránh dependency nặng |
| Standalone loopback HTTP API | Không có consumer ngoài executor |
| Chrome extension relay | Phức tạp cao, use case (điều khiển browser user đang mở khi vắng máy) chưa cần |
| AI-format snapshot, PDF export | Snapshot ref hiện tại đủ cho agent loop |

---

## 7. Tác động lên multi-tenant (agents-as-a-service)

Design AaaS (xem `docs/design/agents-as-a-service.md`) đặt agent trong
container per-tenant. Browser control khi đó **không được** dùng Chrome trên
host: mỗi container tự chạy Chromium headless, hoặc trỏ `attach_url` tới
browserless pool. Config `browser.attach_url` ở §6.1 chính là điểm nối cho
hướng này — thiết kế phase 2 nên làm sớm hơn nếu AaaS đi tiếp. Guard private
network càng bắt buộc trong môi trường multi-tenant (chặn tenant A scan mạng
nội bộ qua browser của data plane).

---

## 8. ⚠️ Rủi ro & trade-off

1. **Prompt injection qua nội dung web** — trang web độc có thể chứa chỉ thị
   khiến agent dùng browser_eval/navigate làm việc ngoài ý muốn. Guard giảm
   blast radius (không vào private network) nhưng **không chặn được** hành vi
   trên public internet (đăng nhập, gửi form). Trade-off chấp nhận, giống
   OpenClaw; giảm nhẹ bằng: eval tắt được, audit log tool call đã có transcript.
2. **DNS rebinding** — guard resolve DNS lúc check, Chrome resolve lại lúc
   navigate; attacker đổi record ở giữa. Fix triệt để cần proxy toàn bộ traffic
   (nặng). Ghi nhận là known limitation, mitigate một phần bằng re-check URL
   sau navigate (§6.3.2).
3. **Trusted input phức tạp hơn tưởng** — element trong iframe, shadow DOM,
   element che khuất → tọa độ sai. Giữ JS fallback nghĩa là hai code path
   phải maintain.
4. **Adopt-orphan có thể adopt nhầm** Chrome debug của user nếu user tự chạy
   9222 — vì vậy §6.2 chuyển sang ephemeral port + PID file là điều kiện tiên
   quyết của §6.4.
5. **Headless bị nhiều site chặn** (anti-bot) — mặc định giữ headful trên
   máy có display; headless chỉ là option.

---

## 9. Roadmap

| Phase | Nội dung | Phụ thuộc | Cỡ |
|-------|----------|-----------|-----|
| **P0** | macOS paths + ephemeral port (§6.2); config `browser:` (§6.1); navigation guard + eval toggle (§6.3) | — | S–M |
| **P1** | Lifecycle/orphan + doctor (§6.4); trusted input (§6.5); tabs + dialog (§6.6) | P0 | M |
| **P2** | Download/upload; `attach_url` (§6.7) | P1 | M |
| **P3** | Profiles, state mgmt (§6.8) | có use case | — |

P0 là điều kiện để nói "browser control hoạt động và an toàn trên môi trường
deploy thật"; hiện tại tính năng chỉ chạy đúng trên Linux dev box.

---

## 10. Open questions

1. Guard mặc định `allow_private_network: false` có phá use case nội bộ nào
   đang dùng không (dashboard localhost, dev server)? → cần quyết trước P0.
2. Trusted input (§6.5) làm toàn bộ hay chỉ click/fill trước, type/hover sau?
3. Chrome nên là **một instance dùng chung** mọi session (hiện tại) hay
   per-session (cách ly cookie giữa các chat)? Per-session sạch hơn nhưng tốn
   RAM đáng kể trên máy cá nhân.
4. Screenshot path per-session (`/tmp/browser-screenshot-<session>.png` hoặc
   vào data dir) — sửa luôn ở P0 hay để P1?
5. Có cần `browser_snapshot` format gọn hơn cho model nhỏ (token budget) không?
