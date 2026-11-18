---
name: connectors
description: 'Plug a project into Stripe, Resend, Gmail, OpenAI… Use when an app needs payments, email, AI or maps. NOT for: deploy keys.'
---

# connectors

A project has **Connectors**, the way Lovable does: a service is connected once
for the machine (its keys stored, never shown again), then enabled on a project,
and the project's code gets the keys as environment variables — in Preview and,
after publish, in PROD. The person can do it in the dashboard (Connectors
button in the editor and the room) or by asking you; you have the same panel as
tools from the `bomclaw-connectors` MCP server.

## The tools

- `connectors_list` — the catalog, what is connected, what the project enables.
- `connector_guide` — variables (secret or public) and a working request. Read
  it before writing code that calls the service.
- `connector_connect` — store keys the person gave you (`values`), then check
  them. Google connectors (gmail, google-drive, google-sheets, google-calendar,
  google-search-console) take no values: the answer is a sign-in link to give
  the person.
- `connector_verify` — does the key work, which account/mode it reaches.
- `connector_enable` / `connector_disable` — on the project (bomclaw.json).
- `connector_disconnect` — delete stored keys. Only when asked.

`project` defaults to the project you are working in.

## The flow for "thêm thanh toán Stripe", "gửi email khi có đơn"…

1. `connectors_list` → is it connected? If not, ask the person for the keys
   (say where to create them — the tool names the page) or send the sign-in
   link; `connector_connect` them.
2. `connector_enable` on the project.
3. `connector_guide`, then write the code. Secrets only on a server: a static
   site needs a Worker script (`deploy.main`) or server route for them.
4. Test in Preview (it restarts with the keys by itself), or run a script with
   `bomclaw connectors exec -- <cmd>`.

## Rules

- **Never write a key into the project**, a commit, a log or your answer — not
  even to test. The code reads `env.NAME` / `process.env.NAME`.
- **Never invent, reuse or guess keys.** Only values the person gave in this
  conversation, or a sign-in they completed.
- Prefer **test/sandbox keys** (sk_test_, pdl_sdbx_) until the person says live.
- A check that fails: say what the service answered and ask for a corrected key.
