# BomClaw Browser Bridge

A Chrome extension that lets your BomClaw agent drive **your** browser — the one
already logged into your accounts — instead of a separate automated one.

Full reference, wire protocol and security notes: [`docs/browser-bridge.md`](../docs/browser-bridge.md).

## Install

1. Start the gateway. It prints the endpoint and, the first time, generates a
   pairing token:

   ```
   browser bridge: extension endpoint at ws://127.0.0.1:18789/ext (eval=true)
   ```

2. Get the token:

   ```bash
   bomclaw browser token
   ```

3. Open `chrome://extensions`, enable **Developer mode**, click **Load
   unpacked**, and choose this directory.

4. Click the extension icon, paste the endpoint and token, press **Pair**.

   Running more than one agent? Repeat with that agent's own port and token
   (agent 2 is usually `ws://127.0.0.1:18790/ext`). The extension holds one
   connection per agent, and each agent drives its own tab.

5. From the agent's shell:

   ```bash
   bomclaw browser status
   bomclaw browser navigate https://example.com
   bomclaw browser snapshot
   ```

## Files

| File | Role |
|---|---|
| `manifest.json` | MV3 manifest: `tabs`, `scripting`, `storage`, `<all_urls>` |
| `background.js` | Service worker — the WebSocket client and action dispatcher |
| `page.js` | Functions injected into pages to snapshot and act on them |
| `popup.html` / `popup.js` / `popup.css` | Pairing UI and connection status |

## Why `page.js` repeats itself

`chrome.scripting.executeScript` serialises **one** function by source into the
target page. A shared helper would be undefined there, so each injected
function carries its own copy of the element lookup. The ref numbering must
also stay identical to the Go side (`internal/browser/`) — same depth-first
walk, refs counted over every element visited — so `n12` means the same element
whether the agent is driving the managed Chrome or this extension.

## Permissions, and why each is needed

| Permission | Why |
|---|---|
| `tabs` | List, open, focus and close tabs; read titles and URLs |
| `scripting` | Run the snapshot/act functions in the page |
| `storage` | Remember the endpoint and token between browser restarts |
| `activeTab` | Screenshot the visible tab |
| `<all_urls>` | The agent may be asked to work on any site you name |

The extension only ever talks to the gateway endpoint you paste in. It makes no
other network requests, and sends nothing anywhere until you press Connect.
