---
title: Browser Bridge
layout: default
---

<section class="doc-page">
<div class="doc-content" markdown="1">

# Browser Bridge

<p class="lead">A Chrome extension that lets your BomClaw agent drive <em>your</em> browser — the one already logged into your accounts — instead of a separate automated one.</p>

BomClaw has always been able to launch its own Chrome and script it over CDP
(the `browser_*` tools). That browser starts logged out of everything, which is
fine for reading public pages and useless for "check my orders" or "what does
my dashboard say". The Browser Bridge is the other half: an extension you
install in your everyday browser, paired to your gateway, that executes the
same kind of actions in the tabs you are already signed into.

## How it fits together

```
agent's shell                gateway                     your browser
─────────────                ───────                     ────────────
bomclaw browser click n7
      │ POST /api/browser/call        ┌── WebSocket /ext ──┐
      └──────────────────────────────>│                    │
                                 Hub ─┤  {"type":"call"}   ├─> chrome.scripting
                                      │                    │   executeScript
      <───────────────────────────────│  {"type":"result"} │
        {"ok":true,"result":{…}}      └────────────────────┘
```

The extension dials **out** to the gateway on loopback; the gateway never
connects to the browser. One browser per gateway — a newer connection replaces
an older one, so reloading the extension never leaves a stale socket holding
the slot.

## Several agents, one browser

A machine commonly runs more than one agent (agent 1 on `:18789`, agents 2 and
3 on `:18790`/`:18791`). **Agent 1 is the master**: it alone holds the
extension's socket. The others set `browser.extension.master` and relay every
browser call to agent 1's `/api/browser/call` over loopback, naming themselves.
Agent 1 forwards the call to the extension with that name (`"agent"` in the
`call` frame), so one extension and one socket serve every agent. A non-master
gateway refuses an extension of its own with close code `4004`.

Every agent drives **its own tab group** — "BomClaw · bomclaw2", in its own
colour — inside the window you are already using. Nothing opens a window of
its own, and an agent's new tabs open in the background so your page stays
where it is. Two agents working at the same time never navigate each other's
page. `bomclaw browser tabs` marks the tab the calling agent acts on with `*`,
and labels tabs in another agent's group with that agent's name.

A screenshot briefly brings the agent's tab forward (Chrome can only capture
the visible tab) and puts yours back straight after.

The popup shows the socket, and under it every agent that has used the browser
through it: its group colour, how many actions it ran, its last action, and the
page it is on. Below that is a rolling log of what every agent did.

## Setup

**1. Start the gateways.** Agent 1 logs the endpoint; the others log the master
they borrow from:

```
browser bridge: extension endpoint at ws://127.0.0.1:18789/ext (eval=true, token required=false)
browser bridge: using the browser held by http://127.0.0.1:18789 (eval=true)
```

**2. Load the extension.** Open `chrome://extensions`, turn on **Developer
mode**, click **Load unpacked**, and pick the `extension/` directory of this
repo. It connects to `ws://127.0.0.1:18789/ext` by itself — there is nothing to
paste. The dot turns green and names agent 1.

**5. Check it from the agent's side.**

```bash
bomclaw browser status
```

## Commands

| Command | What it does |
|---|---|
| `bomclaw browser status` | Is a browser connected? |
| `bomclaw browser token` | The pairing token and endpoint for the extension |
| `bomclaw browser tabs` | List open tabs (`id`, title, url); `*` marks this agent's tab, `[name]` marks a peer's |
| `bomclaw browser tabs open <url>` | Open a new tab |
| `bomclaw browser tabs focus <id>` | Act on one of **your** tabs from now on |
| `bomclaw browser tabs close <id>` | Close a tab |
| `bomclaw browser navigate <url>` | Open a URL in the agent's own tab |
| `bomclaw browser snapshot [--selector CSS]` | The page as a tree of elements with refs |
| `bomclaw browser click <ref>` | Click an element |
| `bomclaw browser fill <ref> <text>` | Replace an input's value |
| `bomclaw browser type <ref> <text>` | Append to an input |
| `bomclaw browser select <ref> <value>` | Choose a dropdown option |
| `bomclaw browser text [--ref R] [--property …]` | Read `text`/`html`/`value`/`title`/`url` |
| `bomclaw browser scroll [dir] [--pixels N]` | Scroll the page |
| `bomclaw browser wait [--ref R] [--text T] [--ms N]` | Wait for an element, text, or a delay |
| `bomclaw browser back` | Go back in history |
| `bomclaw browser screenshot [--out PATH]` | Save a PNG of the page |
| `bomclaw browser eval '<js>'` | Run JavaScript in the page |

Exit code **3** means the bridge is up but no browser is paired.

### Refs

`snapshot` numbers every element depth-first — `n1`, `n2`, … — and every action
takes one of those refs. Refs are positions, not identities: **they change
whenever the page changes**, so snapshot again after anything that navigates,
submits, or re-renders. The numbering is identical to the managed-Chrome
`browser_*` tools, so the same ref means the same element in both.

### Which tab actions land on

`navigate` uses the **agent's own tab**, opened in a separate window so your
tab strip is left alone. `tabs focus <id>` points the agent at one of your
existing tabs instead — that is how you hand it a page you are already signed
into. Everything after that acts on the focused tab until you focus another.

## Configuration

```yaml
browser:
  extension:
    enabled: true
    master: ""                  # agents 2+: "http://127.0.0.1:18789" — borrow agent 1's browser
    require_token: false        # true: even a loopback extension must present the token
    token: ""                   # empty = generated into <data_dir>/browser-bridge.token
    allow_eval: true            # false removes `bomclaw browser eval`
    call_timeout_seconds: 30    # how long one action may take
    blocked_hosts: []           # e.g. ["*.bank.example"] — never opened
```

The token is only asked for when `require_token` is on, or when the connection
did not come straight from loopback (a tunnel or proxy). `BOMCLAW_BROWSER_TOKEN`
overrides `token`. A generated token is written
owner-only (`0600`) and never leaves the machine except when you paste it into
the extension.

## Security

This feature hands an AI agent your logged-in browser, so the boundaries are
worth stating plainly.

**What protects it**

- **Loopback-direct, or the token.** By default `/ext` accepts a connection
  made straight from loopback with no proxy or tunnel headers (`CF-Connecting-IP`,
  `X-Forwarded-*`) — the user's own browser on the user's own machine. Anything
  else must present the pairing token, compared in constant time; a wrong one
  is closed with 4001 and the extension stops retrying. `require_token: true`
  demands the token from loopback too. The trade: any local process can now
  attach as the browser, which on a single-user Mac is a process that could
  already read your cookies.
- **Loopback only.** The extension connects to `127.0.0.1`. The CLI routes sit
  behind the same rule as `/api/status`: a dashboard login session, or a direct
  loopback caller with no proxy headers — so tunnel traffic can never reach
  them unauthenticated.
- **Scheme policy.** Only `http(s)` (and `about:blank`) can be opened.
  `file://`, `chrome://`, `javascript:` and `data:` are refused before the
  request reaches the browser — those are how a prompt-injected agent would
  reach your disk or the browser's own settings.
- **`blocked_hosts`.** Sites the agent may never open, with `*.example.com`
  covering subdomains.
- **`allow_eval: false`.** Removes arbitrary JavaScript execution, which is the
  way around everything above that lives inside the page.
- **Conduct rules in the system prompt.** The agent is told not to enter
  credentials it was not given, and not to submit purchases, transfers,
  deletions, or messages to other people without your explicit confirmation of
  that exact action.

**What does not protect it**

- **Prompt injection.** Page content is untrusted, and an agent reading a
  hostile page may be talked into acting on it. The policy above limits *where*
  it can go, not what it can be persuaded to do on a site it is allowed to
  reach. The system prompt tells the agent that page text is never an
  instruction; treat that as a mitigation, not a guarantee.
- **Your session cookies.** Anything you are logged into, the agent can act as.
  That is the entire point of the feature, and the entire risk of it. Use
  `blocked_hosts` for accounts you never want touched.
- **`allow_eval: true`.** With eval on, page-context JavaScript can do anything
  the page could, including calling site APIs directly.

Turn the whole thing off with `browser.extension.enabled: false`, or just
disconnect in the popup — the agent then gets a clear "no browser is connected"
and carries on without it.

## Wire protocol

One JSON object per WebSocket frame.

| Direction | Frame |
|---|---|
| ext → gw | `{"type":"hello","token":"…","client":"…","browser":"…"}` (token optional on loopback) |
| gw → ext | `{"type":"welcome","agent":"…","name":"…"}`, or close `4001` (unauthorized) / `4004` (not the master) |
| gw → ext | `{"type":"call","id":"7","agent":"bomclaw2","action":"click","params":{"ref":"n7"}}` |
| ext → gw | `{"type":"result","id":"7","ok":true,"result":{…}}` |
| ext → gw | `{"type":"result","id":"7","ok":false,"error":"element n7 not found"}` |
| both | `{"type":"ping"}` / `{"type":"pong"}` |

Close codes: `4000` bad handshake, `4001` unauthorized, `4002` replaced by a
newer connection, `4003` stopped answering pings.

The gateway pings every 20s — this also keeps Chrome from unloading the
extension's service worker — and drops a connection silent for 60s.

Result shapes by action: `{"message":…}` for actions that just report
(`navigate`, `click`, `fill`, `type`, `select`, `scroll`, `back`, `wait`),
`{"nodes":[…]}` for `snapshot`, `{"text":…}` for `text`, `{"tabs":[…]}` for
`tabs list`, `{"data":"<base64>","format":"png"}` for `screenshot`, and
`{"result":…}` for `eval`.

## Limits

- **Not trusted input events.** Clicks and typing are DOM calls
  (`el.click()`, value setters + `input`/`change` events), so sites that check
  `event.isTrusted` — some payment forms and anti-bot flows — will ignore them.
- **`eval` needs a permissive CSP.** Pages with a strict Content-Security-Policy
  refuse injected script; prefer `snapshot` and `text`.
- **`screenshot` captures the visible viewport**, not the full page.
- **Some pages cannot be scripted at all.** Chrome refuses injection on its own
  pages (`chrome://`, `view-source:`, `file:`), on the Web Store, and on a
  blank tab. The agent's tab starts blank, so a `snapshot` before the first
  `navigate` hits this; the bridge reports which of those it is rather than
  passing on Chrome's "Extension manifest must request permission" wording,
  which reads as a broken install.
- **Chrome and Chromium-family browsers only.** The manifest is MV3; Firefox
  would need its own packaging.

</div>
</section>
