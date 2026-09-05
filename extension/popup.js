const $ = (id) => document.getElementById(id);
const DEFAULT_URL = "ws://127.0.0.1:18789/ext";

function render(s) {
  const dot = $("dot");
  dot.className = "dot" + (s.connected ? " on" : s.wantConnected ? " wait" : "");
  $("state").textContent = s.connected
    ? "Connected to " + (s.agentName || "agent")
    : s.wantConnected ? "Connecting…" : "Not connected";
  $("error").hidden = !s.lastError;
  $("error").textContent = s.lastError || "";
  $("connect").hidden = s.connected || s.wantConnected;
  $("disconnect").hidden = !(s.connected || s.wantConnected);
}

chrome.storage.local.get(["url", "token"], (v) => {
  $("url").value = v.url || DEFAULT_URL;
  $("token").value = v.token || "";
});

chrome.runtime.sendMessage({ type: "getStatus" }).then(render).catch(() => {});
chrome.runtime.onMessage.addListener((msg) => { if (msg && msg.type === "status") render(msg); });

$("connect").addEventListener("click", () => {
  const url = $("url").value.trim() || DEFAULT_URL;
  const token = $("token").value.trim();
  if (!token) { $("error").hidden = false; $("error").textContent = "Paste a pairing token first."; return; }
  chrome.runtime.sendMessage({ type: "connect", url, token }).then(render).catch(() => {});
});

$("disconnect").addEventListener("click", () => {
  chrome.runtime.sendMessage({ type: "disconnect" }).then(render).catch(() => {});
});
