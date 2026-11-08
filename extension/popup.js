const $ = (id) => document.getElementById(id);
const DEFAULT_URL = "ws://127.0.0.1:18789/ext";

// Everything the popup shows is rendered from one status payload, so the view
// can never disagree with the service worker about who is connected.
function render(s) {
  renderAgents(s.agents || []);
  renderActivity(s.activity || []);
}

function renderAgents(list) {
  const host = $("agents");
  host.textContent = "";
  if (list.length === 0) {
    const p = el("p", "empty", "Not connected yet — the bridge connects to the master agent (ws://127.0.0.1:18789/ext) on its own.");
    host.appendChild(p);
    $("pair-box").open = true;
    return;
  }
  for (const a of list) host.appendChild(agentCard(a));
}

function agentCard(a) {
  const card = el("div", "agent");

  const top = el("div", "agent-top");
  const dot = el("span", "dot" + (a.connected ? " on" : a.wantConnected ? " wait" : ""));
  const name = el("span", "agent-name", a.name || a.agentId || hostOf(a.url));
  top.append(dot, name);
  card.appendChild(top);

  const state = a.connected
    ? `connected · ${a.actions} action${a.actions === 1 ? "" : "s"}` +
      (a.lastAction ? ` · last: ${a.lastAction} ${ago(a.lastActionAt)}` : "")
    : a.wantConnected ? "connecting…" : "disconnected";
  card.appendChild(el("div", "agent-meta", `${state}\n${a.url}`));

  // Every agent driving this browser through the socket, each with its tab
  // group colour and the page it is on — the question the popup exists to
  // answer once more than one agent is at work.
  for (const act of a.actors || []) {
    const row = el("div", "agent-tab");
    const head = el("span", "t", "");
    head.appendChild(el("span", "swatch swatch-" + act.color, ""));
    head.appendChild(document.createTextNode(
      act.id + ` · ${act.actions} action${act.actions === 1 ? "" : "s"}` +
      (act.lastAction ? ` · ${act.lastAction} ${ago(act.lastActionAt)}` : "")));
    row.appendChild(head);
    if (act.tab) row.appendChild(el("span", "u", (act.tab.title || "(untitled tab)") + " — " + (act.tab.url || "")));
    card.appendChild(row);
  }

  if (a.lastError) card.appendChild(el("p", "error", a.lastError));

  const row = el("div", "agent-actions");
  const toggle = el("button", "", a.connected || a.wantConnected ? "Disconnect" : "Connect");
  toggle.onclick = () => sendMsg({
    type: a.connected || a.wantConnected ? "disconnect" : "connect", url: a.url,
  });
  const forget = el("button", "", "Forget");
  forget.onclick = () => { if (confirm("Unpair " + (a.name || a.url) + "?")) sendMsg({ type: "forget", url: a.url }); };
  row.append(toggle, forget);
  card.appendChild(row);
  return card;
}

function renderActivity(list) {
  const host = $("activity");
  host.textContent = "";
  if (list.length === 0) {
    host.appendChild(el("li", "empty", "Nothing yet."));
    return;
  }
  for (const a of list) {
    const li = document.createElement("li");
    li.appendChild(el("span", "act-time", clock(a.at)));
    const body = el("div", "act-body");
    const head = document.createElement("div");
    head.appendChild(el("span", "act-action" + (a.error ? " act-err" : " act-ok"), a.action));
    head.appendChild(document.createTextNode(" "));
    head.appendChild(el("span", "act-agent", a.agent));
    body.appendChild(head);
    if (a.detail) body.appendChild(el("div", "act-detail", a.detail));
    if (a.error) body.appendChild(el("div", "act-err", a.error));
    li.appendChild(body);
    host.appendChild(li);
  }
}

// --- helpers ---

function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
}

function hostOf(url) {
  try { return new URL(url).host; } catch (e) { return url; }
}

function clock(ms) {
  const d = new Date(ms);
  return String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0") + ":" +
    String(d.getSeconds()).padStart(2, "0");
}

function ago(ms) {
  if (!ms) return "";
  const s = Math.round((Date.now() - ms) / 1000);
  if (s < 60) return s + "s ago";
  if (s < 3600) return Math.round(s / 60) + "m ago";
  return Math.round(s / 3600) + "h ago";
}

function sendMsg(msg) {
  return chrome.runtime.sendMessage(msg).then(render).catch(() => {});
}

// --- wiring ---

sendMsg({ type: "getStatus" });
chrome.runtime.onMessage.addListener((msg) => { if (msg && msg.type === "status") render(msg); });

chrome.storage.local.get(["agents"], (v) => {
  const used = new Set((v.agents || []).map((a) => a.url));
  // Every local agent goes through the master, so another endpoint is only
  // ever a gateway on another machine.
  $("url").value = used.has(DEFAULT_URL) ? "" : DEFAULT_URL;
});

$("pair").addEventListener("click", () => {
  const url = $("url").value.trim() || DEFAULT_URL;
  const token = $("token").value.trim();
  const err = $("pair-error");
  // A token is optional: only a gateway with require_token, or one reached
  // through a tunnel, asks for it.
  err.hidden = true;
  sendMsg({ type: "pair", url, token }).then(() => {
    $("token").value = "";
    $("pair-box").open = false;
  });
});

$("clear").addEventListener("click", () => sendMsg({ type: "clearActivity" }));

// Keep "last: click 12s ago" honest while the popup stays open.
setInterval(() => sendMsg({ type: "getStatus" }), 3000);
