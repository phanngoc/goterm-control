package connectors

import (
	"strings"

	"github.com/ngocp/goterm-control/internal/secrets"
)

// f is a field: env name, label, secret?, required?
func f(env, label string, secret, required bool, opts ...func(*secrets.Field)) secrets.Field {
	x := secrets.Field{Env: env, Label: label, Secret: secret, Required: required}
	for _, o := range opts {
		o(&x)
	}
	return x
}

// Icon "" draws the brand's initial in its color: simpleicons.org has no mark
// for some brands (removed at their request), and a 404 is noise.
func public(x *secrets.Field)            { x.Public = true }
func managed(x *secrets.Field)           { x.Managed = true }
func ph(p string) func(*secrets.Field)   { return func(x *secrets.Field) { x.Placeholder = p } }
func help(h string) func(*secrets.Field) { return func(x *secrets.Field) { x.Help = h } }
func svc(id, title, cat, purpose, docs string) secrets.Service {
	return secrets.Service{ID: id, Title: title, Category: cat, Purpose: purpose, Docs: docs}
}

// googleIdentity every Google connector asks for besides its own: who signed in.
var googleIdentity = []string{"openid", "email"}

func google(id, title, cat, short, scope, tokenEnv, guide string, popular bool) *Connector {
	s := svc(id, title, cat, short+" Đăng nhập Google một lần; app dùng refresh token để gọi API.",
		"https://console.cloud.google.com/apis/library")
	s.Fields = []secrets.Field{f(tokenEnv, "Refresh token", true, true, managed,
		help("Có sau khi bấm Kết nối với Google (OAuth). Không cần nhập tay."))}
	return &Connector{
		Service: s, Short: short, Icon: iconFor(id), Color: "4285F4", Popular: popular,
		Auth: AuthOAuth, Requires: []string{"google-oauth"},
		Scopes: append(append([]string{}, googleIdentity...), strings.Fields(scope)...),
		Guide:  guide + "\n\n" + googleTokenGuide(tokenEnv), verify: verifyGoogle(tokenEnv, strings.Fields(scope)[0]),
	}
}

func iconFor(id string) string {
	return map[string]string{
		"gmail": "gmail", "google-drive": "googledrive", "google-sheets": "googlesheets",
		"google-calendar": "googlecalendar", "google-search-console": "googlesearchconsole",
	}[id]
}

func builtin() []*Connector {
	cs := []*Connector{
		// ── setup ──────────────────────────────────────────────────────────
		{
			Service: withFields(svc("google-oauth", "Google OAuth client", "setup",
				"Ứng dụng OAuth của bạn trên Google Cloud — dùng chung cho Gmail, Drive, Sheets, Calendar, Search Console.",
				"https://console.cloud.google.com/apis/credentials"),
				f("GOOGLE_CLIENT_ID", "Client ID", false, true, ph("…apps.googleusercontent.com"),
					help("Google Cloud → APIs & Services → Credentials → Create OAuth client ID → Web application.")),
				f("GOOGLE_CLIENT_SECRET", "Client secret", true, true,
					help("Thêm Authorized redirect URI hiện trong ô Kết nối; bật API tương ứng (Gmail API, Drive API…)."))),
			Short: "OAuth client cho các connector Google", Icon: "google", Color: "4285F4", Hidden: true,
			verify: verifyGoogleClient,
		},

		// ── grow ───────────────────────────────────────────────────────────
		{
			Service: withFields(svc("google-analytics", "Google Analytics", "grow",
				"Đo lượt truy cập: gtag.js trên trang, Measurement Protocol từ server.",
				"https://support.google.com/analytics/answer/9539598"),
				f("GA_MEASUREMENT_ID", "Measurement ID", false, true, public, ph("G-XXXXXXXXXX"),
					help("Admin → Data streams → Web stream → Measurement ID.")),
				f("GA_API_SECRET", "Measurement Protocol API secret", true, false,
					help("Cùng Web stream → Measurement Protocol API secrets. Chỉ khi gửi sự kiện từ server."))),
			Short: "Theo dõi lượt truy cập và sự kiện", Icon: "googleanalytics", Color: "E37400", Popular: true,
			verify: verifyGA,
			Guide: `Browser (public id): add gtag to index.html / the root layout:
  <script async src="https://www.googletagmanager.com/gtag/js?id=GA_MEASUREMENT_ID"></script>
  <script>window.dataLayer=window.dataLayer||[];function gtag(){dataLayer.push(arguments)}gtag('js',new Date());gtag('config','GA_MEASUREMENT_ID');</script>
In Vite read it as import.meta.env.VITE_GA_MEASUREMENT_ID (Next: process.env.NEXT_PUBLIC_GA_MEASUREMENT_ID).
Server events (needs GA_API_SECRET), never from the browser:
  fetch("https://www.google-analytics.com/mp/collect?measurement_id="+env.GA_MEASUREMENT_ID+"&api_secret="+env.GA_API_SECRET,
    {method:"POST", body: JSON.stringify({client_id, events:[{name:"purchase", params:{value, currency:"VND"}}]})})`,
		},
		google("google-search-console", "Google Search Console", "grow",
			"Dữ liệu tìm kiếm: từ khóa, lượt hiển thị, index.",
			"https://www.googleapis.com/auth/webmasters.readonly", "GOOGLE_SEARCH_CONSOLE_REFRESH_TOKEN",
			`Search Console API (server only):
  GET https://www.googleapis.com/webmasters/v3/sites  → the sites this account can read
  POST https://www.googleapis.com/webmasters/v3/sites/{encodeURIComponent(siteUrl)}/searchAnalytics/query
    body {"startDate":"2026-01-01","endDate":"2026-01-31","dimensions":["query"],"rowLimit":25}`, true),
		{
			Service: withFields(svc("google-maps", "Google Maps Platform", "grow",
				"Bản đồ, địa điểm, tìm đường, geocoding.",
				"https://console.cloud.google.com/google/maps-apis/credentials"),
				f("GOOGLE_MAPS_API_KEY", "API key", false, true, public,
					help("Khóa dùng trên trình duyệt: giới hạn theo HTTP referrer (tên miền của bạn) trong Google Cloud."))),
			Short: "Bản đồ, địa điểm, chỉ đường", Icon: "googlemaps", Color: "4285F4", Popular: true,
			verify: verifyMaps,
			Guide: `Browser key (public — restrict it by HTTP referrer in Google Cloud):
  Vite: import.meta.env.VITE_GOOGLE_MAPS_API_KEY · Next: process.env.NEXT_PUBLIC_GOOGLE_MAPS_API_KEY
  <script src="https://maps.googleapis.com/maps/api/js?key=KEY&callback=initMap" async></script>
  or npm @vis.gl/react-google-maps: <APIProvider apiKey={key}><Map defaultCenter={{lat:21.03,lng:105.85}} defaultZoom={12}/></APIProvider>
Server geocoding: fetch("https://maps.googleapis.com/maps/api/geocode/json?address="+encodeURIComponent(q)+"&key="+env.GOOGLE_MAPS_API_KEY)
  (a key restricted to referrers is refused from a server — use a second, unrestricted-by-referrer key there).`,
		},

		// ── sell ───────────────────────────────────────────────────────────
		{
			Service: withFields(svc("stripe", "Stripe", "sell",
				"Thanh toán thẻ, Checkout, subscription, webhook.",
				"https://dashboard.stripe.com/apikeys"),
				f("STRIPE_SECRET_KEY", "Secret key", true, true, ph("sk_test_… / sk_live_… / rk_…"),
					help("Developers → API keys. Bắt đầu bằng khóa test (sk_test_).")),
				f("STRIPE_PUBLISHABLE_KEY", "Publishable key", false, false, public, ph("pk_test_…")),
				f("STRIPE_WEBHOOK_SECRET", "Webhook signing secret", true, false, ph("whsec_…"),
					help("Developers → Webhooks → endpoint của app → Signing secret."))),
			Short: "Thanh toán online, subscription", Icon: "stripe", Color: "635BFF", Popular: true,
			verify: verifyStripe,
			Guide: `Server only (Worker env.STRIPE_SECRET_KEY / Node process.env.STRIPE_SECRET_KEY). Checkout session:
  const r = await fetch("https://api.stripe.com/v1/checkout/sessions", {method:"POST",
    headers:{Authorization:"Bearer "+env.STRIPE_SECRET_KEY, "Content-Type":"application/x-www-form-urlencoded"},
    body: new URLSearchParams({mode:"payment", success_url: origin+"/ok", cancel_url: origin+"/",
      "line_items[0][price_data][currency]":"vnd", "line_items[0][price_data][unit_amount]":"50000",
      "line_items[0][price_data][product_data][name]":"Áo", "line_items[0][quantity]":"1"})})
  → redirect the browser to (await r.json()).url
Node may use the stripe npm package; in a Cloudflare Worker use fetch as above (or stripe with Stripe.createFetchHttpClient()).
Webhooks: verify the Stripe-Signature header with STRIPE_WEBHOOK_SECRET before trusting the event.
The publishable key (pk_) is the only one allowed in the browser: VITE_STRIPE_PUBLISHABLE_KEY.`,
		},
		{
			Service: withFields(svc("paypal", "PayPal", "sell", "Thanh toán PayPal: Orders API, nút PayPal.",
				"https://developer.paypal.com/dashboard/applications"),
				f("PAYPAL_CLIENT_ID", "Client ID", false, true, public),
				f("PAYPAL_CLIENT_SECRET", "Secret", true, true),
				f("PAYPAL_ENV", "Môi trường", false, false, public, ph("sandbox | live"),
					help("Mặc định sandbox."))),
			Short: "Thanh toán PayPal", Icon: "paypal", Color: "003087", verify: verifyPayPal,
			Guide: `Base: https://api-m.sandbox.paypal.com (PAYPAL_ENV=live → https://api-m.paypal.com). Server only:
  token: POST {base}/v1/oauth2/token, Basic base64(CLIENT_ID:SECRET), body grant_type=client_credentials
  order: POST {base}/v2/checkout/orders {"intent":"CAPTURE","purchase_units":[{"amount":{"currency_code":"USD","value":"10.00"}}]}
  capture: POST {base}/v2/checkout/orders/{id}/capture
Browser button: <script src="https://www.paypal.com/sdk/js?client-id=PAYPAL_CLIENT_ID"> (client id is public).`,
		},
		{
			Service: withFields(svc("paddle", "Paddle", "sell", "Merchant of record: checkout, subscription, thuế.",
				"https://vendors.paddle.com/authentication-v2"),
				f("PADDLE_API_KEY", "API key", true, true, ph("pdl_sdbx_apikey_… / pdl_live_apikey_…")),
				f("PADDLE_CLIENT_TOKEN", "Client-side token", false, false, public, ph("test_… / live_…"))),
			Short: "Bán phần mềm, subscription", Icon: "paddle", Color: "FDDD35", Popular: true, verify: verifyPaddle,
			Guide: `API base: https://sandbox-api.paddle.com for sandbox keys (pdl_sdbx_…), else https://api.paddle.com.
Server: fetch(base+"/transactions", {method:"POST", headers:{Authorization:"Bearer "+env.PADDLE_API_KEY,"Content-Type":"application/json"},
  body: JSON.stringify({items:[{price_id:"pri_…", quantity:1}]})})
Browser: Paddle.js with the client-side token (public): Paddle.Initialize({token: import.meta.env.VITE_PADDLE_CLIENT_TOKEN}); Paddle.Checkout.open({items:[{priceId:"pri_…"}]})
Sandbox: call Paddle.Environment.set("sandbox") first.`,
		},
		{
			Service: withFields(svc("mollie", "Mollie", "sell", "Thanh toán châu Âu: iDEAL, thẻ, SEPA.",
				"https://my.mollie.com/dashboard/developers/api-keys"),
				f("MOLLIE_API_KEY", "API key", true, true, ph("test_… / live_…"))),
			Short: "Thanh toán châu Âu", Icon: "", Color: "000000", verify: verifyMollie,
			Guide: `Server only: POST https://api.mollie.com/v2/payments, Authorization: Bearer MOLLIE_API_KEY,
  {"amount":{"currency":"EUR","value":"10.00"},"description":"Order 12","redirectUrl":origin+"/ok","webhookUrl":origin+"/api/mollie"}
  → redirect to _links.checkout.href; the webhook receives id=tr_… — fetch the payment to read its status.`,
		},
		{
			Service: withFields(svc("shopify", "Shopify", "sell", "Sản phẩm, đơn hàng, khách hàng của cửa hàng Shopify.",
				"https://help.shopify.com/en/manual/apps/app-types/custom-apps"),
				f("SHOPIFY_STORE_DOMAIN", "Store domain", false, true, public, ph("my-store.myshopify.com")),
				f("SHOPIFY_ADMIN_TOKEN", "Admin API access token", true, true, ph("shpat_…"),
					help("Settings → Apps → Develop apps → custom app → Admin API access token.")),
				f("SHOPIFY_STOREFRONT_TOKEN", "Storefront API token", false, false, public)),
			Short: "Cửa hàng, sản phẩm, đơn hàng", Icon: "shopify", Color: "7AB55C", verify: verifyShopify,
			Guide: `Admin API (server only): POST https://{SHOPIFY_STORE_DOMAIN}/admin/api/2025-01/graphql.json
  headers X-Shopify-Access-Token: SHOPIFY_ADMIN_TOKEN · body {"query":"{ products(first:10){ nodes { id title } } }"}
Storefront (public token, browser OK): POST https://{domain}/api/2025-01/graphql.json, header X-Shopify-Storefront-Access-Token.`,
		},
		{
			Service: withFields(svc("woocommerce", "WooCommerce", "sell", "Sản phẩm và đơn hàng của shop WordPress.",
				"https://woocommerce.com/document/woocommerce-rest-api/"),
				f("WOOCOMMERCE_URL", "Store URL", false, true, public, ph("https://shop.example.com")),
				f("WOOCOMMERCE_CONSUMER_KEY", "Consumer key", true, true, ph("ck_…")),
				f("WOOCOMMERCE_CONSUMER_SECRET", "Consumer secret", true, true, ph("cs_…"))),
			Short: "Shop WordPress", Icon: "woocommerce", Color: "96588A", verify: verifyWoo,
			Guide: `Server only: GET {WOOCOMMERCE_URL}/wp-json/wc/v3/products?per_page=20 with Basic base64(KEY:SECRET).
Orders: /wp-json/wc/v3/orders. HTTPS is required for Basic auth.`,
		},

		// ── communicate ────────────────────────────────────────────────────
		{
			Service: withFields(svc("resend", "Resend", "communicate", "Gửi email giao dịch từ tên miền của bạn.",
				"https://resend.com/api-keys"),
				f("RESEND_API_KEY", "API key", true, true, ph("re_…")),
				f("RESEND_FROM", "Người gửi", false, false, ph("App <no-reply@yourdomain.com>"),
					help("Địa chỉ trên tên miền đã xác minh ở Resend → Domains. Chưa có: onboarding@resend.dev (chỉ gửi tới chính bạn)."))),
			Short: "Gửi email", Icon: "resend", Color: "000000", Popular: true, verify: verifyResend,
			Guide: `Server only: await fetch("https://api.resend.com/emails", {method:"POST",
  headers:{Authorization:"Bearer "+env.RESEND_API_KEY,"Content-Type":"application/json"},
  body: JSON.stringify({from: env.RESEND_FROM || "onboarding@resend.dev", to:["a@b.com"], subject:"Xin chào", html:"<p>…</p>"})})`,
		},
		{
			Service: withFields(svc("telegram", "Telegram", "communicate", "Bot Telegram của app: gửi thông báo, nhận tin.",
				"https://core.telegram.org/bots/tutorial#obtain-your-bot-token"),
				f("TELEGRAM_BOT_TOKEN", "Bot token", true, true, ph("123456:ABC…"),
					help("@BotFather → /newbot. Dùng bot RIÊNG cho app, không dùng bot của agent.")),
				f("TELEGRAM_CHAT_ID", "Chat ID mặc định", false, false, help("Nơi app gửi thông báo (người, nhóm, kênh)."))),
			Short: "Bot, thông báo qua Telegram", Icon: "telegram", Color: "26A5E4", Popular: true, verify: verifyTelegram,
			Guide: `Server only: fetch("https://api.telegram.org/bot"+env.TELEGRAM_BOT_TOKEN+"/sendMessage", {method:"POST",
  headers:{"Content-Type":"application/json"}, body: JSON.stringify({chat_id: env.TELEGRAM_CHAT_ID, text:"Đơn mới #12"})})
Receiving: setWebhook to https://<prod domain>/api/telegram (a Worker route), never getUpdates from two places at once.`,
		},
		{
			Service: withFields(svc("slack", "Slack", "communicate", "Gửi tin vào kênh Slack bằng bot.",
				"https://api.slack.com/apps"),
				f("SLACK_BOT_TOKEN", "Bot token", true, true, ph("xoxb-…"),
					help("Slack app → OAuth & Permissions → scope chat:write → Install → Bot User OAuth Token.")),
				f("SLACK_CHANNEL", "Kênh mặc định", false, false, ph("#general hoặc C0123…"))),
			Short: "Tin nhắn vào Slack", Icon: "", Color: "4A154B", verify: verifySlack,
			Guide: `Server only: fetch("https://slack.com/api/chat.postMessage", {method:"POST",
  headers:{Authorization:"Bearer "+env.SLACK_BOT_TOKEN,"Content-Type":"application/json; charset=utf-8"},
  body: JSON.stringify({channel: env.SLACK_CHANNEL, text:"Đơn mới"})}) — the bot must be invited to the channel.`,
		},
		{
			Service: withFields(svc("discord", "Discord", "communicate", "Gửi tin vào kênh Discord qua webhook.",
				"https://support.discord.com/hc/en-us/articles/228383668"),
				f("DISCORD_WEBHOOK_URL", "Webhook URL", true, true, ph("https://discord.com/api/webhooks/…"))),
			Short: "Thông báo vào Discord", Icon: "discord", Color: "5865F2", verify: verifyDiscord,
			Guide: `Server only: fetch(env.DISCORD_WEBHOOK_URL, {method:"POST", headers:{"Content-Type":"application/json"},
  body: JSON.stringify({content:"Đơn mới #12"})})`,
		},
		{
			Service: withFields(svc("twilio", "Twilio", "communicate", "SMS, cuộc gọi, WhatsApp.",
				"https://console.twilio.com/"),
				f("TWILIO_ACCOUNT_SID", "Account SID", false, true, ph("AC…")),
				f("TWILIO_AUTH_TOKEN", "Auth token", true, true),
				f("TWILIO_FROM_NUMBER", "Số gửi", false, false, ph("+1…"))),
			Short: "SMS và cuộc gọi", Icon: "", Color: "F22F46", verify: verifyTwilio,
			Guide: `Server only: POST https://api.twilio.com/2010-04-01/Accounts/{SID}/Messages.json
  Basic base64(SID:AUTH_TOKEN), form body To=+84…&From=TWILIO_FROM_NUMBER&Body=…`,
		},
		{
			Service: withFields(svc("sendgrid", "SendGrid", "communicate", "Gửi email qua Twilio SendGrid.",
				"https://app.sendgrid.com/settings/api_keys"),
				f("SENDGRID_API_KEY", "API key", true, true, ph("SG.…")),
				f("SENDGRID_FROM", "Người gửi đã xác minh", false, false, ph("no-reply@yourdomain.com"))),
			Short: "Gửi email", Icon: "", Color: "1A82E2", verify: verifySendGrid,
			Guide: `Server only: POST https://api.sendgrid.com/v3/mail/send, Authorization: Bearer SENDGRID_API_KEY,
  {"personalizations":[{"to":[{"email":"a@b.com"}]}],"from":{"email":SENDGRID_FROM},"subject":"…","content":[{"type":"text/html","value":"…"}]}`,
		},

		// ── work ───────────────────────────────────────────────────────────
		google("gmail", "Gmail", "work", "Gửi và đọc email bằng tài khoản Gmail.",
			"https://www.googleapis.com/auth/gmail.send https://www.googleapis.com/auth/gmail.readonly", "GOOGLE_GMAIL_REFRESH_TOKEN",
			`Gmail API (server only), with an access token from the refresh token:
  send: POST https://gmail.googleapis.com/gmail/v1/users/me/messages/send {"raw": base64url(RFC 2822 message)}
  list: GET https://gmail.googleapis.com/gmail/v1/users/me/messages?q=is:unread&maxResults=10
  read: GET …/messages/{id}?format=full`, true),
		google("google-drive", "Google Drive", "work", "Đọc, tải lên và chia sẻ file trong Drive.",
			"https://www.googleapis.com/auth/drive", "GOOGLE_DRIVE_REFRESH_TOKEN",
			`Drive API v3 (server only):
  list: GET https://www.googleapis.com/drive/v3/files?q=name contains 'report'&fields=files(id,name,mimeType)
  download: GET …/files/{id}?alt=media · Google Docs → GET …/files/{id}/export?mimeType=text/plain
  upload: POST https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart`, true),
		google("google-sheets", "Google Sheets", "work", "Đọc và ghi bảng tính — làm CMS, lưu form.",
			"https://www.googleapis.com/auth/spreadsheets", "GOOGLE_SHEETS_REFRESH_TOKEN",
			`Sheets API v4 (server only):
  read: GET https://sheets.googleapis.com/v4/spreadsheets/{sheetId}/values/Sheet1!A1:D100
  append: POST …/values/Sheet1!A1:append?valueInputOption=USER_ENTERED {"values":[["Nguyễn A","a@b.com"]]}
  create: POST https://sheets.googleapis.com/v4/spreadsheets {"properties":{"title":"Leads"}}`, true),
		google("google-calendar", "Google Calendar", "work", "Lịch, đặt hẹn, sự kiện.",
			"https://www.googleapis.com/auth/calendar", "GOOGLE_CALENDAR_REFRESH_TOKEN",
			`Calendar API v3 (server only):
  list: GET https://www.googleapis.com/calendar/v3/calendars/primary/events?timeMin=<RFC3339>&singleEvents=true&orderBy=startTime
  create: POST …/calendars/primary/events {"summary":"Hẹn","start":{"dateTime":"…+07:00"},"end":{"dateTime":"…"}}
  free/busy: POST https://www.googleapis.com/calendar/v3/freeBusy`, true),
		{
			Service: withFields(svc("notion", "Notion", "work", "Trang và database Notion: làm CMS, kéo nội dung vào app.",
				"https://www.notion.so/profile/integrations"),
				f("NOTION_TOKEN", "Internal integration secret", true, true, ph("ntn_… / secret_…"),
					help("Tạo integration, rồi trong Notion: trang → ••• → Connections → thêm integration đó."))),
			Short: "Trang, database Notion", Icon: "notion", Color: "000000", verify: verifyNotion,
			Guide: `Server only, headers Authorization: Bearer NOTION_TOKEN, Notion-Version: 2022-06-28:
  search: POST https://api.notion.com/v1/search {"query":"Blog"}
  database rows: POST https://api.notion.com/v1/databases/{id}/query
  page content: GET https://api.notion.com/v1/blocks/{pageId}/children
A page is only visible after it is shared with the integration (••• → Connections).`,
		},
		{
			Service: withFields(svc("airtable", "Airtable", "work", "Bảng Airtable làm backend dữ liệu.",
				"https://airtable.com/create/tokens"),
				f("AIRTABLE_TOKEN", "Personal access token", true, true, ph("pat…"),
					help("Scopes data.records:read/write, schema.bases:read; chọn base được truy cập.")),
				f("AIRTABLE_BASE_ID", "Base ID", false, false, ph("app…"))),
			Short: "Bảng dữ liệu Airtable", Icon: "airtable", Color: "18BFFF", verify: verifyAirtable,
			Guide: `Server only: GET https://api.airtable.com/v0/{AIRTABLE_BASE_ID}/{table}?maxRecords=50, Authorization: Bearer AIRTABLE_TOKEN
Create: POST same URL {"records":[{"fields":{"Name":"…"}}]}`,
		},
		{
			Service: withFields(svc("github", "GitHub", "work", "Repo, issue, file trên GitHub.",
				"https://github.com/settings/personal-access-tokens"),
				f("GITHUB_TOKEN", "Token", true, true, ph("github_pat_… / ghp_…"))),
			Short: "Repo, issue, PR", Icon: "github", Color: "181717", verify: verifyGitHub,
			Guide: `Server only: fetch("https://api.github.com/repos/{owner}/{repo}/issues", {headers:{Authorization:"Bearer "+env.GITHUB_TOKEN,
  Accept:"application/vnd.github+json","User-Agent":"app"}}) — a User-Agent header is required.`,
		},
		{
			Service: withFields(svc("linear", "Linear", "work", "Issue và dự án trên Linear.",
				"https://linear.app/settings/account/security"),
				f("LINEAR_API_KEY", "Personal API key", true, true, ph("lin_api_…"))),
			Short: "Issue tracking", Icon: "linear", Color: "5E6AD2", verify: verifyLinear,
			Guide: `Server only: POST https://api.linear.app/graphql, Authorization: LINEAR_API_KEY (no "Bearer" for personal keys),
  {"query":"mutation($t:String!,$team:String!){ issueCreate(input:{title:$t, teamId:$team}){ issue { url } } }"}`,
		},

		// ── ai ─────────────────────────────────────────────────────────────
		{
			Service: withFields(svc("openai", "OpenAI", "ai", "GPT, hình ảnh, embeddings, giọng nói.",
				"https://platform.openai.com/api-keys"),
				f("OPENAI_API_KEY", "API key", true, true, ph("sk-…"))),
			Short: "GPT, ảnh, embeddings", Icon: "", Color: "412991", verify: verifyOpenAI,
			Guide: `Server only (never call it from the browser with the key):
  POST https://api.openai.com/v1/responses, Authorization: Bearer OPENAI_API_KEY, {"model":"gpt-4.1-mini","input":"…"}
  Stream to the browser through your own /api route.`,
		},
		{
			Service: withFields(svc("anthropic", "Anthropic (Claude)", "ai", "Claude cho chat, tóm tắt, agent trong app.",
				"https://console.anthropic.com/settings/keys"),
				f("ANTHROPIC_API_KEY", "API key", true, true, ph("sk-ant-…"))),
			Short: "Claude trong app", Icon: "anthropic", Color: "191919", verify: verifyAnthropic,
			Guide: `Server only: POST https://api.anthropic.com/v1/messages
  headers x-api-key: ANTHROPIC_API_KEY, anthropic-version: 2023-06-01, content-type: application/json
  {"model":"claude-sonnet-5-5","max_tokens":1024,"messages":[{"role":"user","content":"…"}]}`,
		},
		{
			Service: withFields(svc("gemini", "Google Gemini", "ai", "Gemini: văn bản, ảnh, đa phương thức.",
				"https://aistudio.google.com/apikey"),
				f("GEMINI_API_KEY", "API key", true, true)),
			Short: "Gemini trong app", Icon: "googlegemini", Color: "8E75B2", verify: verifyGemini,
			Guide: `Server only: POST https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent
  header x-goog-api-key: GEMINI_API_KEY, {"contents":[{"parts":[{"text":"…"}]}]}`,
		},
		{
			Service: withFields(svc("elevenlabs", "ElevenLabs", "ai", "Chuyển văn bản thành giọng nói.",
				"https://elevenlabs.io/app/settings/api-keys"),
				f("ELEVENLABS_API_KEY", "API key", true, true)),
			Short: "Giọng nói AI", Icon: "elevenlabs", Color: "000000", verify: verifyElevenLabs,
			Guide: `Server only: POST https://api.elevenlabs.io/v1/text-to-speech/{voiceId}?output_format=mp3_44100_128
  header xi-api-key: ELEVENLABS_API_KEY, {"text":"Xin chào","model_id":"eleven_multilingual_v2"} → audio/mpeg bytes`,
		},
		{
			Service: withFields(svc("firecrawl", "Firecrawl", "ai", "Crawl và trích nội dung trang web thành markdown.",
				"https://www.firecrawl.dev/app/api-keys"),
				f("FIRECRAWL_API_KEY", "API key", true, true, ph("fc-…"))),
			Short: "Crawl web thành dữ liệu", Icon: "", Color: "FF6B00", Popular: true, verify: verifyFirecrawl,
			Guide: `Server only: POST https://api.firecrawl.dev/v1/scrape, Authorization: Bearer FIRECRAWL_API_KEY,
  {"url":"https://example.com","formats":["markdown"]} → data.markdown`,
		},
		{
			Service: withFields(svc("replicate", "Replicate", "ai", "Chạy model mã nguồn mở: ảnh, video, âm thanh.",
				"https://replicate.com/account/api-tokens"),
				f("REPLICATE_API_TOKEN", "API token", true, true, ph("r8_…"))),
			Short: "Model ảnh, video", Icon: "replicate", Color: "000000", verify: verifyReplicate,
			Guide: `Server only: POST https://api.replicate.com/v1/models/{owner}/{model}/predictions, Authorization: Bearer REPLICATE_API_TOKEN,
  header Prefer: wait, {"input":{"prompt":"…"}} → output (URL of the result)`,
		},
	}
	return cs
}

func withFields(s secrets.Service, fs ...secrets.Field) secrets.Service {
	s.Fields = fs
	return s
}

func googleTokenGuide(tokenEnv string) string {
	return `Access token (server only; cache it ~50 min):
  const t = await fetch("https://oauth2.googleapis.com/token", {method:"POST",
    headers:{"Content-Type":"application/x-www-form-urlencoded"},
    body: new URLSearchParams({client_id: env.GOOGLE_CLIENT_ID, client_secret: env.GOOGLE_CLIENT_SECRET,
      refresh_token: env.` + tokenEnv + `, grant_type:"refresh_token"})}).then(r => r.json())
  then Authorization: "Bearer " + t.access_token on every API call.
The refresh token is the account that signed in on the Connectors page; every visitor of the app acts as that account.`
}
