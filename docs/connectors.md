# Connectors

What a project can plug into — Stripe, PayPal, Paddle, Resend, Telegram, Slack,
Gmail, Google Drive/Sheets/Calendar, Notion, Airtable, OpenAI, Anthropic, Google
Maps, Analytics… — the way Lovable's Connectors work.

1. **Connect** a service once for the machine. Keys go to `secrets.yaml` (the
   same store as Admin → Kết nối), are checked against the service, and are never
   shown again — only a mask. Google connectors sign in with your own OAuth client
   (`google-oauth`: Client ID/secret, redirect URI
   `https://<dashboard>/api/connectors/oauth/callback`).
2. **Enable** it on a project: `bomclaw.json` gets `"connectors": ["stripe"]`.
3. The project's code reads the keys as **environment variables**:
   - Preview: the dev server is started with them and restarts by itself when
     they change. Public values (publishable keys, GA id, Maps key) also as
     `VITE_<NAME>` / `NEXT_PUBLIC_<NAME>`.
   - Publish: Cloudflare — Worker secrets (`wrangler secret bulk`, needs
     `deploy.main`); a static site only gets public values at build time.
     Fly.io — app secrets (`flyctl secrets import --stage`).
   - `bomclaw connectors exec -- <cmd>` runs a command with them, without
     printing them.

## Three ways in, one store

| | |
|---|---|
| Dashboard | **Connectors** button in the editor title bar and the room header; Admin → Connectors |
| Chat | the bundled `connectors` plugin gives every agent `bomclaw mcp connectors`: `connectors_list`, `connector_guide`, `connector_connect`, `connector_verify`, `connector_enable`, `connector_disable`, `connector_disconnect` |
| Shell | `bomclaw connectors list|guide|connect|verify|enable|disable|disconnect|exec` |

Adding a connector: one entry in `internal/connectors/catalog.go` (fields,
category, guide for the agent) and a check in `verify.go`.
