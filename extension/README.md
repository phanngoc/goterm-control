# BomClaw Browser Bridge

A Chrome extension that lets your BomClaw agent drive **your** browser — the one
already logged into your accounts — instead of a separate automated one.

Full reference, wire protocol and security notes: [`docs/browser-bridge.md`](../docs/browser-bridge.md).

## Install

1. Start the gateways. Agent 1 (`:18789`) is the master and holds the
   extension; agents 2 and 3 set `browser.extension.master:
   http://127.0.0.1:18789` and borrow it.

2. Open `chrome://extensions`, enable **Developer mode**, click **Load
   unpacked**, and choose this directory. Already loaded? Press the reload
   icon — 0.3.0 adds the `tabGroups` permission.

3. That is all. The extension connects to `ws://127.0.0.1:18789/ext` with no
   token. Each agent's tabs open in the background of your current window, in
   a tab group of its own ("BomClaw · bomclaw2").

4. From the agent's shell:

   ```bash
   bomclaw browser status
   bomclaw browser navigate https://example.com
   bomclaw browser snapshot
   ```

## Files

| File | Role |
|---|---|
| `manifest.json` | MV3 manifest: `tabs`, `tabGroups`, `scripting`, `storage`, `<all_urls>` |
| `background.js` | Service worker — the WebSocket client and action dispatcher |
| `page.js` | Functions injected into pages to snapshot and act on them |
| `popup.html` / `popup.js` / `popup.css` | Connection status, per-agent tab groups, activity log |

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
| `storage` | Remember the endpoint (and token, if one is set) between browser restarts |
| `tabGroups` | Keep each agent's tabs in a named, coloured group of its own |
| `activeTab` | Screenshot the visible tab |
| `<all_urls>` | The agent may be asked to work on any site you name |

The extension only ever talks to the gateway endpoint you paste in. It makes no
other network requests, and sends nothing anywhere until you press Connect.
