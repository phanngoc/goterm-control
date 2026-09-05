// background.js — the BomClaw Browser Bridge service worker.
//
// It keeps one WebSocket open PER PAIRED AGENT and executes the actions each
// one sends. A machine commonly runs several agents (agent 1 on :18789,
// agent 2 on :18790, …); a single connection would mean whichever agent you
// paired last owned the browser and the others were permanently unable to use
// it. Each agent therefore gets its own connection, its own reconnect loop,
// and — importantly — its own tab, so two agents working at once do not
// navigate each other's page out from under them.
//
// Wire protocol (see docs/browser-bridge.md):
//   ext→gw {type:"hello",token,client,browser}
//   gw→ext {type:"welcome",agent,name} | close 4001 on a bad token
//   gw→ext {type:"call",id,action,params}
//   ext→gw {type:"result",id,ok,result|error}
//   both   {type:"ping"} / {type:"pong"}

importScripts("page.js");

const CLIENT = "bomclaw-bridge/0.2.0";
const RECONNECT_MIN = 1000;
const RECONNECT_MAX = 30000;
const LOG_MAX = 40;

// agents maps a stable key (the endpoint URL) to its live connection state.
const agents = new Map();

// activity is a rolling log across all agents, newest first. The popup shows
// it because "connected" alone never explains what the agent actually did.
let activity = [];

// --- lifecycle ---

chrome.runtime.onStartup.addListener(() => restoreAgents());
chrome.runtime.onInstalled.addListener(() => restoreAgents());
restoreAgents();

async function restoreAgents() {
  const cfg = await storageGet(["agents", "url", "token", "autoConnect"]);
  let list = Array.isArray(cfg.agents) ? cfg.agents : [];

  // Migrate the single-agent shape this extension shipped with, so an upgrade
  // does not silently unpair the browser.
  if (list.length === 0 && cfg.url && cfg.token) {
    list = [{ url: cfg.url, token: cfg.token, autoConnect: cfg.autoConnect !== false }];
    await storageSet({ agents: list });
  }
  for (const a of list) {
    upsertAgent(a.url, a.token, a.name);
    if (a.autoConnect !== false) connect(a.url);
  }
  broadcastStatus();
}

function storageGet(keys) {
  return new Promise((resolve) => chrome.storage.local.get(keys, (v) => resolve(v || {})));
}
function storageSet(obj) {
  return new Promise((resolve) => chrome.storage.local.set(obj, resolve));
}

async function persistAgents() {
  const list = [...agents.values()].map((a) => ({
    url: a.url, token: a.token, name: a.name, autoConnect: a.wantConnected,
  }));
  await storageSet({ agents: list });
}

// --- agent registry ---

function upsertAgent(url, token, name) {
  let a = agents.get(url);
  if (!a) {
    a = {
      url, token, name: name || "",
      ws: null, connected: false, wantConnected: false,
      reconnectDelay: RECONNECT_MIN, reconnectTimer: null,
      lastError: "", actions: 0, lastAction: "", lastActionAt: 0,
      // Each agent drives its own tab so concurrent agents do not fight.
      agentTabId: null, currentTabId: null,
    };
    agents.set(url, a);
  } else {
    a.token = token || a.token;
    if (name) a.name = name;
  }
  return a;
}

function removeAgent(url) {
  const a = agents.get(url);
  if (!a) return;
  a.wantConnected = false;
  clearTimeout(a.reconnectTimer);
  if (a.ws) { try { a.ws.close(1000, "unpaired"); } catch (e) {} }
  agents.delete(url);
  persistAgents();
  broadcastStatus();
}

// --- connection ---

function connect(url) {
  const a = agents.get(url);
  if (!a) return;
  a.wantConnected = true;
  clearTimeout(a.reconnectTimer);

  if (!a.token || !a.url) {
    a.lastError = "Set the endpoint and token first.";
    broadcastStatus();
    return;
  }
  try {
    a.ws = new WebSocket(a.url);
  } catch (e) {
    scheduleReconnect(a, "bad endpoint: " + e.message);
    return;
  }
  a.ws.onopen = () => {
    a.lastError = "";
    send(a, { type: "hello", token: a.token, client: CLIENT, browser: navigator.userAgent });
  };
  a.ws.onmessage = (ev) => onFrame(a, ev.data);
  a.ws.onclose = (ev) => {
    a.connected = false;
    broadcastStatus();
    if (ev.code === 4001) {
      a.lastError = "The gateway rejected the pairing token. Copy a fresh one from `bomclaw browser token`.";
      a.wantConnected = false; // a wrong token will not fix itself
      persistAgents();
      broadcastStatus();
      return;
    }
    if (a.wantConnected) scheduleReconnect(a, ev.reason || ("closed (" + ev.code + ")"));
  };
  a.ws.onerror = () => { /* onclose carries the useful signal */ };
  persistAgents();
}

function disconnect(url) {
  const a = agents.get(url);
  if (!a) return;
  a.wantConnected = false;
  clearTimeout(a.reconnectTimer);
  a.connected = false;
  if (a.ws) { try { a.ws.close(1000, "user disconnected"); } catch (e) {} a.ws = null; }
  persistAgents();
  broadcastStatus();
}

function scheduleReconnect(a, reason) {
  a.lastError = reason || "";
  broadcastStatus();
  clearTimeout(a.reconnectTimer);
  a.reconnectTimer = setTimeout(() => connect(a.url), a.reconnectDelay);
  a.reconnectDelay = Math.min(a.reconnectDelay * 2, RECONNECT_MAX);
}

function onFrame(a, data) {
  let f;
  try { f = JSON.parse(data); } catch (e) { return; }
  switch (f.type) {
    case "welcome":
      a.connected = true;
      a.name = f.name || f.agent || a.name || "agent";
      a.agentId = f.agent || "";
      a.reconnectDelay = RECONNECT_MIN;
      a.lastError = "";
      persistAgents();
      broadcastStatus();
      break;
    case "call":
      handleCall(a, f);
      break;
    case "ping":
      send(a, { type: "pong" });
      break;
  }
}

function send(a, obj) {
  if (a.ws && a.ws.readyState === WebSocket.OPEN) a.ws.send(JSON.stringify(obj));
}

// --- action dispatch ---

async function handleCall(a, f) {
  const started = Date.now();
  try {
    const result = await runAction(a, f.action, f.params || {});
    if (result && result.error) {
      note(a, f.action, f.params, result.error, started);
      send(a, { type: "result", id: f.id, ok: false, error: result.error });
    } else {
      note(a, f.action, f.params, "", started);
      send(a, { type: "result", id: f.id, ok: true, result: result });
    }
  } catch (e) {
    const msg = String((e && e.message) || e);
    note(a, f.action, f.params, msg, started);
    send(a, { type: "result", id: f.id, ok: false, error: msg });
  }
}

// note records one action for the popup. The detail is the single most useful
// parameter — a URL or a ref — because "click" alone says nothing about what
// the agent is doing to your browser.
function note(a, action, params, error, started) {
  a.actions++;
  a.lastAction = action;
  a.lastActionAt = Date.now();
  const p = params || {};
  const detail = p.url || p.ref || p.expression || p.text || p.action || "";
  activity.unshift({
    at: Date.now(),
    ms: Date.now() - started,
    agent: a.name || a.url,
    action,
    detail: String(detail).slice(0, 80),
    error: error || "",
  });
  if (activity.length > LOG_MAX) activity.length = LOG_MAX;
  broadcastStatus();
}

async function runAction(a, action, p) {
  switch (action) {
    case "navigate": return navigate(a, p.url);
    case "snapshot": return injectFn(a, await targetTab(a), bcSnapshot, [p.selector || ""]);
    case "click":    return injectFn(a, await targetTab(a), bcClick, [p.ref]);
    case "fill":     return injectFn(a, await targetTab(a), bcFill, [p.ref, p.text || ""]);
    case "type":     return injectFn(a, await targetTab(a), bcType, [p.ref, p.text || ""]);
    case "select":   return injectFn(a, await targetTab(a), bcSelect, [p.ref, p.value || ""]);
    case "scroll":   return injectFn(a, await targetTab(a), bcScroll, [p.direction || "down", p.pixels || 0]);
    case "text":     return injectFn(a, await targetTab(a), bcText, [p.ref || "", p.property || "text"]);
    case "back":     return goBack(a);
    case "wait":     return waitFor(a, p);
    case "screenshot": return screenshot(a);
    case "eval":     return evalJS(a, p.expression || "");
    case "tabs":     return tabsAction(a, p);
    default: throw new Error("unknown action: " + action);
  }
}

// targetTab returns the tab this agent is acting on, creating its own tab if
// it has none. Per-agent, so two agents never steer the same page.
async function targetTab(a) {
  if (a.currentTabId != null && await tabExists(a.currentTabId)) return a.currentTabId;
  if (a.agentTabId != null && await tabExists(a.agentTabId)) { a.currentTabId = a.agentTabId; return a.agentTabId; }
  const tab = await createAgentTab(a, "about:blank");
  return tab.id;
}

async function tabExists(id) {
  try { await chrome.tabs.get(id); return true; } catch (e) { return false; }
}

async function createAgentTab(a, url) {
  // A separate window keeps the agent's page out of the user's tab strip.
  const win = await chrome.windows.create({ url, focused: false });
  const tab = win.tabs[0];
  a.agentTabId = tab.id;
  a.currentTabId = tab.id;
  return tab;
}

async function navigate(a, url) {
  if (!url) throw new Error("url is required");
  let tabId = a.agentTabId;
  if (tabId == null || !(await tabExists(tabId))) {
    const tab = await createAgentTab(a, url);
    tabId = tab.id;
  } else {
    await chrome.tabs.update(tabId, { url });
  }
  a.currentTabId = tabId;
  await waitForLoad(tabId, 15000);
  return { message: "Navigated to " + url };
}

async function goBack(a) {
  const tabId = await targetTab(a);
  await injectFn(a, tabId, () => { history.back(); return true; }, []);
  return { message: "Navigated back" };
}

async function waitFor(a, p) {
  const ms = p.ms || 0;
  if (ms > 0 && !p.ref && !p.text) {
    await sleep(ms);
    return { message: "Waited " + ms + "ms" };
  }
  const timeout = ms > 0 ? ms : 10000;
  const deadline = Date.now() + timeout;
  const tabId = await targetTab(a);
  while (Date.now() < deadline) {
    const r = await injectFn(a, tabId, bcExists, [p.ref || "", p.text || ""]);
    if (r && r.found) return { message: "Condition met" };
    await sleep(200);
  }
  throw new Error("timed out waiting for " + (p.ref || JSON.stringify(p.text)));
}

async function screenshot(a) {
  const tabId = await targetTab(a);
  const tab = await chrome.tabs.get(tabId);
  const dataUrl = await chrome.tabs.captureVisibleTab(tab.windowId, { format: "png" });
  return { data: dataUrl.replace(/^data:image\/png;base64,/, ""), format: "png" };
}

async function evalJS(a, expression) {
  if (!expression) throw new Error("expression is required");
  const tabId = await targetTab(a);
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

// tabsAction lists/opens/focuses/closes tabs. The listing marks which tab THIS
// agent is driving, and which belong to another agent, so a two-agent setup is
// legible instead of a flat list of everything open.
async function tabsAction(a, p) {
  const action = p.action || "list";
  switch (action) {
    case "list": {
      const tabs = await chrome.tabs.query({});
      const owners = new Map();
      for (const other of agents.values()) {
        if (other.agentTabId != null) owners.set(other.agentTabId, other.name || other.url);
      }
      return {
        tabs: tabs.map((t) => ({
          id: String(t.id),
          title: t.title || "",
          url: t.url || "",
          active: t.id === a.currentTabId,
          owner: owners.get(t.id) || "",
        })),
      };
    }
    case "open": {
      if (!p.url) throw new Error("url is required");
      const tab = await chrome.tabs.create({ url: p.url, active: false });
      a.currentTabId = tab.id;
      await waitForLoad(tab.id, 15000);
      return { tabId: String(tab.id), message: "Opened tab " + tab.id };
    }
    case "focus": {
      const id = parseInt(p.tab_id, 10);
      if (!(await tabExists(id))) throw new Error("no tab with id " + p.tab_id);
      a.currentTabId = id;
      const t = await chrome.tabs.get(id);
      await chrome.tabs.update(id, { active: true });
      await chrome.windows.update(t.windowId, { focused: true });
      return { message: "Now acting on tab " + id };
    }
    case "close": {
      const id = parseInt(p.tab_id, 10);
      await chrome.tabs.remove(id);
      if (a.currentTabId === id) a.currentTabId = null;
      if (a.agentTabId === id) a.agentTabId = null;
      return { message: "Closed tab " + id };
    }
    default:
      throw new Error("unknown tabs action: " + action);
  }
}

// --- helpers ---

// injectFn runs a page.js function in the target tab's MAIN world and returns
// its value. A ref/param is passed as args, never string-interpolated.
async function injectFn(a, tabId, fn, args) {
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
  if (/^(chrome|chrome-untrusted|chrome-extension|edge|brave|devtools|view-source|file|data):/i.test(url)) {
    return "Chrome does not let extensions script " + url.split(":")[0] +
      ": pages — navigate to an http(s) page instead";
  }
  if (/^https:\/\/(chromewebstore\.google\.com|chrome\.google\.com\/webstore)/i.test(url)) {
    return "Chrome blocks extensions from scripting the Web Store";
  }
  return String((cause && cause.message) || cause || "script injection failed");
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

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

async function statusPayload() {
  const list = [];
  for (const a of agents.values()) {
    let tab = null;
    if (a.currentTabId != null) {
      try {
        const t = await chrome.tabs.get(a.currentTabId);
        tab = { id: String(t.id), title: t.title || "", url: t.url || "" };
      } catch (e) { /* the tab went away; report none */ }
    }
    list.push({
      url: a.url,
      name: a.name || "",
      agentId: a.agentId || "",
      connected: a.connected,
      wantConnected: a.wantConnected,
      lastError: a.lastError || "",
      actions: a.actions,
      lastAction: a.lastAction || "",
      lastActionAt: a.lastActionAt || 0,
      tab,
    });
  }
  list.sort((x, y) => x.url.localeCompare(y.url));
  return { type: "status", agents: list, activity };
}

function broadcastStatus() {
  statusPayload().then((p) => chrome.runtime.sendMessage(p).catch(() => {})).catch(() => {});
}

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  switch (msg && msg.type) {
    case "getStatus":
      statusPayload().then(sendResponse);
      return true;
    case "pair":
      upsertAgent(msg.url, msg.token);
      connect(msg.url);
      statusPayload().then(sendResponse);
      return true;
    case "connect":
      connect(msg.url);
      statusPayload().then(sendResponse);
      return true;
    case "disconnect":
      disconnect(msg.url);
      statusPayload().then(sendResponse);
      return true;
    case "forget":
      removeAgent(msg.url);
      statusPayload().then(sendResponse);
      return true;
    case "clearActivity":
      activity = [];
      statusPayload().then(sendResponse);
      return true;
  }
  return false;
});
