// page.js — the functions injected into a page to snapshot and act on it.
//
// These run in the MAIN world of the target tab. chrome.scripting.executeScript
// serialises ONE function by source, so each must be self-contained: no shared
// helpers, no closure over anything in the service worker. That is why the
// element lookup is repeated inline rather than factored out — extracting it
// would leave every action calling an undefined function in the page.
//
// The ref scheme MUST match the Go side (internal/browser/operations.go and
// snapshot.go): a depth-first walk of the element tree, refs "n1","n2",… in
// document order, counted over EVERY element visited. Then "click n12" means
// the same element whether the agent drives the managed Chrome or this
// extension, and a --selector snapshot still yields refs the actions resolve.
// Keep the traversal identical if you touch either side.

function bcSnapshot(selector) {
  const maxNodes = 800, maxText = 220;
  const nodes = [];
  const root = document.documentElement;
  if (!root) return { nodes };
  // A selector narrows what is REPORTED, not where the walk starts: refs stay
  // global-DFS positions so the act functions below resolve them unchanged.
  const scope = selector ? document.querySelector(selector) : null;
  if (selector && !scope) return { nodes, error: "no element matches " + selector };
  let scopeDepth = 0;
  let visited = 0;
  const stack = [{ el: root, depth: 0, parentRef: null }];
  while (stack.length && nodes.length < maxNodes) {
    const cur = stack.pop();
    const el = cur.el;
    if (!el || el.nodeType !== 1) continue;
    const ref = "n" + (visited + 1);
    visited++;
    const inScope = !scope || el === scope || scope.contains(el);
    if (inScope) {
      if (el === scope) scopeDepth = cur.depth;
      const tag = (el.tagName || "").toLowerCase();
      const id = el.id || undefined;
      const role = (el.getAttribute && el.getAttribute("role")) || undefined;
      const name = (el.getAttribute && el.getAttribute("aria-label")) || undefined;
      let text = "";
      try { text = (el.innerText || "").trim(); } catch (e) {}
      if (text.length > maxText) text = text.slice(0, maxText) + "...";
      const href = el.href || undefined;
      const type = el.type || undefined;
      const value = (el.value !== undefined && el.value !== null && el.value !== "")
        ? String(el.value).slice(0, 500) : undefined;
      nodes.push({
        ref, parentRef: cur.parentRef, depth: cur.depth - scopeDepth,
        tag, id, role, name, text, href, type, value,
      });
    }
    const children = el.children ? Array.from(el.children) : [];
    for (let i = children.length - 1; i >= 0; i--) {
      stack.push({ el: children[i], depth: cur.depth + 1, parentRef: ref });
    }
  }
  return { nodes };
}

function bcClick(ref) {
  const find = (r) => {
    const idx = parseInt(String(r).replace("n", ""), 10) - 1;
    if (isNaN(idx) || idx < 0) return null;
    const stack = [document.documentElement];
    let count = 0;
    while (stack.length) {
      const el = stack.pop();
      if (!el || el.nodeType !== 1) continue;
      if (count === idx) return el;
      count++;
      const ch = el.children ? Array.from(el.children) : [];
      for (let i = ch.length - 1; i >= 0; i--) stack.push(ch[i]);
    }
    return null;
  };
  const el = find(ref);
  if (!el) return { error: "element " + ref + " not found" };
  el.scrollIntoView({ block: "center", inline: "center" });
  el.click();
  return { message: "Clicked " + ref };
}

function bcFill(ref, text) {
  const find = (r) => {
    const idx = parseInt(String(r).replace("n", ""), 10) - 1;
    if (isNaN(idx) || idx < 0) return null;
    const stack = [document.documentElement];
    let count = 0;
    while (stack.length) {
      const el = stack.pop();
      if (!el || el.nodeType !== 1) continue;
      if (count === idx) return el;
      count++;
      const ch = el.children ? Array.from(el.children) : [];
      for (let i = ch.length - 1; i >= 0; i--) stack.push(ch[i]);
    }
    return null;
  };
  const el = find(ref);
  if (!el) return { error: "element " + ref + " not found" };
  el.focus();
  el.value = text;
  el.dispatchEvent(new Event("input", { bubbles: true }));
  el.dispatchEvent(new Event("change", { bubbles: true }));
  return { message: "Filled " + ref };
}

function bcType(ref, text) {
  const find = (r) => {
    const idx = parseInt(String(r).replace("n", ""), 10) - 1;
    if (isNaN(idx) || idx < 0) return null;
    const stack = [document.documentElement];
    let count = 0;
    while (stack.length) {
      const el = stack.pop();
      if (!el || el.nodeType !== 1) continue;
      if (count === idx) return el;
      count++;
      const ch = el.children ? Array.from(el.children) : [];
      for (let i = ch.length - 1; i >= 0; i--) stack.push(ch[i]);
    }
    return null;
  };
  const el = find(ref);
  if (!el) return { error: "element " + ref + " not found" };
  el.focus();
  el.value = (el.value || "") + text;
  el.dispatchEvent(new Event("input", { bubbles: true }));
  return { message: "Typed into " + ref };
}

function bcSelect(ref, value) {
  const find = (r) => {
    const idx = parseInt(String(r).replace("n", ""), 10) - 1;
    if (isNaN(idx) || idx < 0) return null;
    const stack = [document.documentElement];
    let count = 0;
    while (stack.length) {
      const el = stack.pop();
      if (!el || el.nodeType !== 1) continue;
      if (count === idx) return el;
      count++;
      const ch = el.children ? Array.from(el.children) : [];
      for (let i = ch.length - 1; i >= 0; i--) stack.push(ch[i]);
    }
    return null;
  };
  const el = find(ref);
  if (!el) return { error: "element " + ref + " not found" };
  el.value = value;
  el.dispatchEvent(new Event("change", { bubbles: true }));
  return { message: "Selected " + JSON.stringify(value) + " in " + ref };
}

function bcScroll(direction, pixels) {
  const px = pixels > 0 ? pixels : 300;
  let dx = 0, dy = 0;
  if (direction === "up") dy = -px;
  else if (direction === "left") dx = -px;
  else if (direction === "right") dx = px;
  else dy = px;
  window.scrollBy(dx, dy);
  return { message: "Scrolled " + (direction || "down") };
}

function bcText(ref, property) {
  const prop = property || "text";
  if (!ref) {
    if (prop === "title") return { text: document.title };
    if (prop === "url") return { text: location.href };
    if (prop === "html") return { text: document.documentElement.outerHTML.slice(0, 200000) };
    return { text: ((document.body && document.body.innerText) || "").slice(0, 200000) };
  }
  const find = (r) => {
    const idx = parseInt(String(r).replace("n", ""), 10) - 1;
    if (isNaN(idx) || idx < 0) return null;
    const stack = [document.documentElement];
    let count = 0;
    while (stack.length) {
      const el = stack.pop();
      if (!el || el.nodeType !== 1) continue;
      if (count === idx) return el;
      count++;
      const ch = el.children ? Array.from(el.children) : [];
      for (let i = ch.length - 1; i >= 0; i--) stack.push(ch[i]);
    }
    return null;
  };
  const el = find(ref);
  if (!el) return { error: "element " + ref + " not found" };
  if (prop === "html") return { text: el.outerHTML || "" };
  if (prop === "value") return { text: String(el.value || "") };
  return { text: el.innerText || "" };
}

function bcExists(ref, text) {
  if (ref) {
    const idx = parseInt(String(ref).replace("n", ""), 10) - 1;
    if (isNaN(idx) || idx < 0) return { found: false };
    const stack = [document.documentElement];
    let count = 0;
    while (stack.length) {
      const el = stack.pop();
      if (!el || el.nodeType !== 1) continue;
      if (count === idx) return { found: true };
      count++;
      const ch = el.children ? Array.from(el.children) : [];
      for (let i = ch.length - 1; i >= 0; i--) stack.push(ch[i]);
    }
    return { found: false };
  }
  if (text) return { found: ((document.body && document.body.innerText) || "").includes(text) };
  return { found: true };
}
