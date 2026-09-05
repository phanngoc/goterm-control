// background.js — the BomClaw Browser Bridge service worker.
//
// It keeps one WebSocket open to the gateway's /ext endpoint, authenticates
// with the pairing token, and executes the actions the agent sends. Actions
// run against a dedicated "agent tab" (a separate window, so your own tabs are
// left alone) unless you point the agent at one of your tabs with
// `bomclaw browser tabs focus <id>`.
//
// Wire protocol (see docs/browser-bridge.md):
//   ext→gw {type:"hello",token,client,browser}
//   gw→ext {type:"welcome",agent,name} | close 4001 on a bad token
//   gw→ext {type:"call",id,action,params}
//   ext→gw {type:"result",id,ok,result|error}
//   both   {type:"ping"} / {type:"pong"}

importScripts("page.js");

const CLIENT = "bomclaw-bridge/0.1.0";
const RECONNECT_MIN = 1000;
const RECONNECT_MAX = 30000;

let ws = null;
let connected = false;      // welcome received
let agentName = "";
let reconnectDelay = RECONNECT_MIN;
let reconnectTimer = null;
let wantConnected = false;  // user asked to be connected
let lastError = "";
let agentTabId = null;      // the tab the agent drives by default
let currentTabId = null;    // active target for snapshot/act (agent tab, or a focused user tab)

// --- lifecycle ---

chrome.runtime.onStartup.addListener(() => maybeAutoConnect());
chrome.runtime.onInstalled.addListener(() => maybeAutoConnect());

async function maybeAutoConnect() {
  const cfg = await getConfig();
  if (cfg.token && cfg.url && cfg.autoConnect !== false) connect();
}

async function getConfig() {
  return new Promise((resolve) => {
    chrome.storage.local.get(["token", "url", "autoConnect"], (v) => resolve(v || {}));
  });
}

// --- connection ---

function connect() {
  wantConnected = true;
  clearTimeout(reconnectTimer);
  getConfig().then((cfg) => {
    if (!cfg.token || !cfg.url) {
      lastError = "Set the endpoint and token first.";
      broadcastStatus();
      return;
    }
    try {
      ws = new WebSocket(cfg.url);
    } catch (e) {
      scheduleReconnect("bad endpoint: " + e.message);
      return;
    }
    ws.onopen = () => {
      lastError = "";
      ws.send(JSON.stringify({
        type: "hello",
        token: cfg.token,
        client: CLIENT,
        browser: navigator.userAgent,
      }));
    };
    ws.onmessage = (ev) => onFrame(ev.data);
    ws.onclose = (ev) => {
      connected = false;
      agentName = "";
      broadcastStatus();
      if (ev.code === 4001) {
        lastError = "The gateway rejected the pairing token. Copy a fresh one from `bomclaw browser token`.";
        wantConnected = false; // a wrong token will not fix itself
        broadcastStatus();
        return;
      }
      if (wantConnected) scheduleReconnect(ev.reason || ("closed (" + ev.code + ")"));
    };
    ws.onerror = () => { /* onclose carries the useful signal */ };
  });
}

function disconnect() {
  wantConnected = false;
  clearTimeout(reconnectTimer);
  connected = false;
  agentName = "";
  if (ws) { try { ws.close(1000, "user disconnected"); } catch (e) {} ws = null; }
  broadcastStatus();
}

function scheduleReconnect(reason) {
  lastError = reason || "";
  broadcastStatus();
  clearTimeout(reconnectTimer);
  reconnectTimer = setTimeout(connect, reconnectDelay);
  reconnectDelay = Math.min(reconnectDelay * 2, RECONNECT_MAX);
}

function onFrame(data) {
  let f;
  try { f = JSON.parse(data); } catch (e) { return; }
  switch (f.type) {
    case "welcome":
      connected = true;
      agentName = f.name || f.agent || "agent";
      reconnectDelay = RECONNECT_MIN;
      lastError = "";
      broadcastStatus();
      break;
    case "call":
      handleCall(f);
      break;
    case "ping":
      send({ type: "pong" });
      break;
  }
}

function send(obj) {
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify(obj));
  }
}

// --- action dispatch ---

async function handleCall(f) {
  try {
    const result = await runAction(f.action, f.params || {});
    // An action can report a per-element failure without throwing.
    if (result && result.error) {
      send({ type: "result", id: f.id, ok: false, error: result.error });
    } else {
      send({ type: "result", id: f.id, ok: true, result: result });
    }
  } catch (e) {
    send({ type: "result", id: f.id, ok: false, error: String(e && e.message || e) });
  }
}

async function runAction(action, p) {
  switch (action) {
    case "navigate": return navigate(p.url);
    case "snapshot": return injectFn(await targetTab(), bcSnapshot, [p.selector || ""]);
    case "click":    return injectFn(await targetTab(), bcClick, [p.ref]);
    case "fill":     return injectFn(await targetTab(), bcFill, [p.ref, p.text || ""]);
    case "type":     return injectFn(await targetTab(), bcType, [p.ref, p.text || ""]);
    case "select":   return injectFn(await targetTab(), bcSelect, [p.ref, p.value || ""]);
    case "scroll":   return injectFn(await targetTab(), bcScroll, [p.direction || "down", p.pixels || 0]);
    case "text":     return injectFn(await targetTab(), bcText, [p.ref || "", p.property || "text"]);
    case "back":     return goBack();
    case "wait":     return waitFor(p);
    case "screenshot": return screenshot();
    case "eval":     return evalJS(p.expression || "");
    case "tabs":     return tabsAction(p);
    default: throw new Error("unknown action: " + action);
  }
}

// targetTab returns the tab the agent is currently acting on, creating the
// agent tab if none exists yet.
async function targetTab() {
  if (currentTabId != null && await tabExists(currentTabId)) return currentTabId;
  if (agentTabId != null && await tabExists(agentTabId)) { currentTabId = agentTabId; return agentTabId; }
  const tab = await createAgentTab("about:blank");
  return tab.id;
}

async function tabExists(id) {
  try { await chrome.tabs.get(id); return true; } catch (e) { return false; }
}

async function createAgentTab(url) {
  // A separate window keeps the agent's page out of the user's tab strip.
  const win = await chrome.windows.create({ url, focused: false });
  const tab = win.tabs[0];
  agentTabId = tab.id;
  currentTabId = tab.id;
  return tab;
}

async function navigate(url) {
  if (!url) throw new Error("url is required");
  let tabId = agentTabId;
  if (tabId == null || !(await tabExists(tabId))) {
    const tab = await createAgentTab(url);
    tabId = tab.id;
  } else {
    await chrome.tabs.update(tabId, { url });
  }
  currentTabId = tabId;
  await waitForLoad(tabId, 15000);
  return { message: "Navigated to " + url };
}

async function goBack() {
  const tabId = await targetTab();
  await injectFn(tabId, () => { history.back(); return true; }, []);
  return { message: "Navigated back" };
}

async function waitFor(p) {
  const ms = p.ms || 0;
  if (ms > 0 && !p.ref && !p.text) {
    await sleep(ms);
    return { message: "Waited " + ms + "ms" };
  }
  const timeout = ms > 0 ? ms : 10000;
  const deadline = Date.now() + timeout;
  const tabId = await targetTab();
  while (Date.now() < deadline) {
    const r = await injectFn(tabId, bcExists, [p.ref || "", p.text || ""]);
    if (r && r.found) return { message: "Condition met" };
    await sleep(200);
  }
  throw new Error("timed out waiting for " + (p.ref || JSON.stringify(p.text)));
}

async function screenshot() {
  const tabId = await targetTab();
  const tab = await chrome.tabs.get(tabId);
  // captureVisibleTab needs the target window to be the one captured; it does
  // not need focus stolen from the user, only the window id.
  const dataUrl = await chrome.tabs.captureVisibleTab(tab.windowId, { format: "png" });
  const base64 = dataUrl.replace(/^data:image\/png;base64,/, "");
  return { data: base64, format: "png" };
}

async function evalJS(expression) {
  if (!expression) throw new Error("expression is required");
  const tabId = await targetTab();
  try {
    const [res] = await chrome.scripting.executeScript({
      target: { tabId },
      world: "MAIN",
      func: (expr) => {
        try { return { result: String(eval(expr)) }; }
        catch (e) { return { error: String(e && e.message || e) }; }
      },
      args: [expression],
    });
    return res.result;
  } catch (e) {
    throw new Error(await injectionBlockReason(tabId, e));
  }
}

// tabsAction lists/opens/focuses/closes tabs. list and focus/close work on the
// user's own tabs by id; focus also makes that tab the agent's active target.
async function tabsAction(p) {
  const action = p.action || "list";
  switch (action) {
    case "list": {
      const tabs = await chrome.tabs.query({});
      return {
        tabs: tabs.map((t) => ({
          id: String(t.id),
          title: t.title || "",
          url: t.url || "",
          active: t.id === currentTabId,
        })),
      };
    }
    case "open": {
      if (!p.url) throw new Error("url is required");
      const tab = await chrome.tabs.create({ url: p.url, active: false });
      currentTabId = tab.id;
      await waitForLoad(tab.id, 15000);
      return { tabId: String(tab.id), message: "Opened tab " + tab.id };
    }
    case "focus": {
      const id = parseInt(p.tab_id, 10);
      if (!(await tabExists(id))) throw new Error("no tab with id " + p.tab_id);
      currentTabId = id;
      const t = await chrome.tabs.get(id);
      await chrome.tabs.update(id, { active: true });
      await chrome.windows.update(t.windowId, { focused: true });
      return { message: "Now acting on tab " + id };
    }
    case "close": {
      const id = parseInt(p.tab_id, 10);
      await chrome.tabs.remove(id);
      if (currentTabId === id) currentTabId = null;
      if (agentTabId === id) agentTabId = null;
      return { message: "Closed tab " + id };
    }
    default:
      throw new Error("unknown tabs action: " + action);
  }
}

// --- helpers ---

// injectFn runs a page.js function in the target tab's MAIN world and returns
// its value. A ref/param is passed as args, never string-interpolated.
async function injectFn(tabId, fn, args) {
  try {
    const [res] = await chrome.scripting.executeScript({
      target: { tabId },
      world: "MAIN",
      func: fn,
      args: args,
    });
    return res && res.result;
  } catch (e) {
    throw new Error(await injectionBlockReason(tabId, e));
  }
}

// injectionBlockReason turns Chrome's refusal to inject into something the
// agent can act on. Chrome reports every one of these as "Cannot access
// contents of url … Extension manifest must request permission to access this
// host", which reads as a broken install — and the commonest cause is simply
// that the agent's tab is still the empty one it was opened with, where the
// fix is to navigate somewhere rather than to touch the manifest.
async function injectionBlockReason(tabId, cause) {
  let url = "";
  try {
    url = (await chrome.tabs.get(tabId)).url || "";
  } catch (e) {
    return "the tab is gone — open one with `bomclaw browser navigate <url>`";
  }
  if (!url || url === "about:blank" || url.startsWith("about:")) {
    return "the agent's tab is empty (" + (url || "no page") +
      ") — load a page first with `bomclaw browser navigate <url>`, " +
      "or point the agent at one of the user's tabs with `bomclaw browser tabs focus <id>`";
  }
  if (/^(chrome|chrome-untrusted|edge|brave|devtools|view-source|file|data):/i.test(url)) {
    return "Chrome does not let extensions script " + url.split(":")[0] +
      ": pages — navigate to an http(s) page instead";
  }
  if (/^https:\/\/(chromewebstore\.google\.com|chrome\.google\.com\/webstore)/i.test(url)) {
    return "Chrome blocks extensions from scripting the Web Store";
  }
  // Not a URL restriction we recognise — pass Chrome's own words through.
  return String((cause && cause.message) || cause || "script injection failed");
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

// waitForLoad resolves when the tab finishes loading, or after a timeout —
// navigation should not hang an action forever on a slow page.
function waitForLoad(tabId, timeout) {
  return new Promise((resolve) => {
    let done = false;
    const finish = () => { if (!done) { done = true; chrome.tabs.onUpdated.removeListener(listener); resolve(); } };
    const listener = (id, info) => { if (id === tabId && info.status === "complete") finish(); };
    chrome.tabs.onUpdated.addListener(listener);
    chrome.tabs.get(tabId, (t) => { if (t && t.status === "complete") finish(); });
    setTimeout(finish, timeout);
  });
}

// --- popup messaging ---

function statusPayload() {
  return { type: "status", connected, wantConnected, agentName, lastError };
}

function broadcastStatus() {
  chrome.runtime.sendMessage(statusPayload()).catch(() => {});
}

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  switch (msg && msg.type) {
    case "getStatus":
      sendResponse(statusPayload());
      return false;
    case "connect":
      chrome.storage.local.set({ token: msg.token, url: msg.url, autoConnect: true }, () => {
        reconnectDelay = RECONNECT_MIN;
        connect();
        sendResponse(statusPayload());
      });
      return true;
    case "disconnect":
      chrome.storage.local.set({ autoConnect: false }, () => {
        disconnect();
        sendResponse(statusPayload());
      });
      return true;
  }
  return false;
});
