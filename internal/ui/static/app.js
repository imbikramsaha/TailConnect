"use strict";
/* TailConnect UI — a dependency-free single page talking to the local control API. */

// ---------- api ----------
const api = {
  async req(method, path, body) {
    const opt = { method, headers: { "X-TailConnect": "1" }, credentials: "same-origin" };
    if (body !== undefined) {
      opt.headers["Content-Type"] = "application/json";
      opt.body = JSON.stringify(body);
    }
    const r = await fetch(path, opt);
    const text = await r.text();
    let data = null;
    try { data = text ? JSON.parse(text) : null; } catch { data = null; }
    if (!r.ok) throw new Error((data && data.error) || text.trim() || r.statusText);
    return data;
  },
  get(p) { return this.req("GET", p); },
  post(p, b) { return this.req("POST", p, b === undefined ? {} : b); },
  put(p, b) { return this.req("PUT", p, b); },
  patch(p, b) { return this.req("PATCH", p, b); },
  del(p) { return this.req("DELETE", p); },
};
const enc = encodeURIComponent;

// ---------- state ----------
let S = null;                 // last /api/state
let route = parseRoute();
let pendingRender = false;
const busy = new Set();       // in-flight action keys
const mon = { id: null, hist: [], last: null, prevNet: null, rate: null, err: null, timer: null, inflight: false };

function parseRoute() {
  const p = location.hash.replace(/^#\/?/, "").split("/").map(decodeURIComponent);
  return { name: p[0] || "", arg: p[1] || "", sub: p[2] || "" };
}
window.addEventListener("hashchange", () => { route = parseRoute(); render(); });

// ---------- helpers ----------
function h(s) {
  return String(s ?? "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}
function bytes(n) {
  if (!n && n !== 0) return "–";
  const u = ["B", "KB", "MB", "GB", "TB", "PB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (n >= 100 || i === 0 ? n.toFixed(0) : n.toFixed(1)) + " " + u[i];
}
function pct(n) { return n == null || n < 0 ? "–" : Math.round(n) + "%"; }
function uptime(s) {
  if (!s) return "–";
  const d = Math.floor(s / 86400), hh = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
  return (d ? d + "d " : "") + (d || hh ? hh + "h " : "") + m + "m";
}
function ago(t) {
  if (!t) return "";
  const s = Math.max(0, (Date.now() - new Date(t).getTime()) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return Math.floor(s / 60) + " min ago";
  if (s < 86400) return Math.floor(s / 3600) + " h ago";
  return Math.floor(s / 86400) + " d ago";
}
function osLabel(os) {
  return { linux: "Linux", darwin: "macOS", macOS: "macOS", windows: "Windows", android: "Android", iOS: "iOS", ios: "iOS" }[os] || (os || "?");
}
function osShort(os) {
  return { linux: "LNX", darwin: "MAC", macOS: "MAC", windows: "WIN", android: "AND", iOS: "IOS", ios: "IOS" }[os] || "•";
}
function toast(msg, bad) {
  const el = document.createElement("div");
  el.className = "toast" + (bad ? " bad" : "");
  el.textContent = msg;
  document.getElementById("toasts").appendChild(el);
  setTimeout(() => el.remove(), bad ? 6500 : 3200);
}
async function copy(text) {
  try { await navigator.clipboard.writeText(text); toast("Copied"); }
  catch { toast("Copy failed — select the text manually", true); }
}
function devById(id) { return S && S.devices.find(d => d.id === id); }
function selfOS() { return S ? S.self.os : ""; }

const ICONS = {
  terminal: '<path d="M4 17l6-5-6-5"/><path d="M12 19h8"/>',
  code: '<path d="M16 18l6-6-6-6"/><path d="M8 6l-6 6 6 6"/>',
  cursor: '<path d="M4 4l7 17 2.5-7.5L21 11z"/>',
  wind: '<path d="M3 8h10a3 3 0 1 0-3-3"/><path d="M3 12h15a3 3 0 1 1-3 3"/><path d="M3 16h6"/>',
  zed: '<path d="M5 5h14L5 19h14"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  gear: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>',
  play: '<path d="M7 5l12 7-12 7z"/>',
  stop: '<rect x="6" y="6" width="12" height="12" rx="1.5"/>',
  ext: '<path d="M14 4h6v6"/><path d="M10 14L20 4"/><path d="M19 13v6a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1h6"/>',
  edit: '<path d="M4 20h4L19 9l-4-4L4 16z"/>',
  trash: '<path d="M4 7h16"/><path d="M10 11v6M14 11v6"/><path d="M6 7l1 13h10l1-13"/><path d="M9 7V4h6v3"/>',
  link: '<path d="M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1"/><path d="M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1"/>',
  power: '<path d="M12 3v9"/><path d="M6.3 7.3a8 8 0 1 0 11.4 0"/>',
  back: '<path d="M15 18l-6-6 6-6"/>',
  copy: '<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a1 1 0 0 1 1-1h10"/>',
};
function icon(name) {
  return `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICONS[name] || ""}</svg>`;
}
const APP_ICON = { terminal: "terminal", vscode: "code", "vscode-insiders": "code", cursor: "cursor", windsurf: "wind", zed: "zed" };

function statusOf(d) {
  if (d.section === "discovered") {
    if (d.pairing === "requesting" || d.pairing === "waiting") return { dot: "busy", text: d.pairing === "waiting" ? "Waiting for approval" : "Pairing…", cls: "warn" };
    if (!d.online) return { dot: "", text: "Offline", cls: "" };
    return { dot: "idle", text: "Ready to pair", cls: "" };
  }
  if (d.section === "tailnet") return d.online ? { dot: "idle", text: "Online · no TailConnect", cls: "" } : { dot: "", text: "Offline", cls: "" };
  switch (d.state) {
    case "connected": return { dot: "on", text: "Connected", cls: "ok" };
    case "connecting": return { dot: "busy", text: "Connecting…", cls: "warn" };
    case "reconnecting": return { dot: "busy", text: "Reconnecting…", cls: "warn" };
    case "error": return { dot: "err", text: "Connection failed", cls: "bad" };
  }
  if (!d.online) return { dot: "", text: "Offline", cls: "" };
  return { dot: "idle", text: d.kind === "peer" ? "Online" : "Ready", cls: "" };
}

// ---------- rendering ----------
const app = document.getElementById("app");

function render() {
  if (!S) return;
  applyTheme();
  const ae = document.activeElement;
  if (ae && app.contains(ae) && ae.matches("input:not([type=checkbox]),textarea,select")) {
    pendingRender = true; // don't clobber typing; render on blur
    return;
  }
  pendingRender = false;
  app.className = "";
  const ts = S.tailnet.state;
  if (ts !== "Running" && route.name !== "settings") {
    app.innerHTML = viewOnboarding();
  } else if (route.name === "settings") {
    app.innerHTML = shell(viewSettings(route.arg || "general"), true);
  } else {
    app.innerHTML = shell(viewDevice(), false);
  }
  syncMonitor();
  if (route.name === "add" && ts === "Running") {
    history.replaceState(null, "", "#/");
    route = parseRoute();
    addDeviceDialog();
  }
}
app.addEventListener("focusout", () => setTimeout(() => { if (pendingRender) render(); }, 0));

function applyTheme() {
  const t = S.settings.theme;
  if (t === "light" || t === "dark") document.documentElement.dataset.theme = t;
  else delete document.documentElement.dataset.theme;
}

function shell(content, settingsMode) {
  const tn = S.tailnet;
  const acct = tn.state === "Running"
    ? `<span class="pill ok acct" title="${h(tn.tailnet || "")}"><span class="dot on"></span>${h(tn.loginName || "Connected")}</span>`
    : `<span class="pill warn acct"><span class="dot busy"></span>${h(tn.state)}</span>`;
  return `
  <div class="shell">
    <header class="topbar">
      <a class="brand" href="#/"><img src="/icon.png" alt="">TailConnect</a>
      <span class="spacer"></span>
      ${acct}
      <button class="btn sm" data-act="add-device">${icon("plus")}Add device</button>
      <a class="btn sm icon ${settingsMode ? "primary" : ""}" href="${settingsMode ? "#/" : "#/settings"}" title="Settings" aria-label="Settings">${icon("gear")}</a>
    </header>
    <div class="banners">${banners()}</div>
    ${settingsMode ? `<div class="content">${content}</div>` : `<div class="main">${sidebar()}<main class="content">${content}</main></div>`}
  </div>`;
}

function banners() {
  let out = "";
  for (const r of S.pairRequests) {
    out += `<div class="banner">
      <div class="grow"><b>${h(r.name)}</b> wants to pair with this device
        <div class="small muted">${h(osLabel(r.os))} · ${h(r.loginName)}${r.sameUser ? " (your account)" : ""} · ${h(ago(r.created))}. Pairing lets it open terminals, editors and forwarded ports here.</div></div>
      <button class="btn sm" data-act="pair-decide" data-id="${h(r.id)}" data-v="deny">Decline</button>
      <button class="btn sm primary" data-act="pair-decide" data-id="${h(r.id)}" data-v="approve">Approve</button>
    </div>`;
  }
  if (S.installWarning) {
    out += `<div class="banner"><div class="grow">${h(S.installWarning)}</div></div>`;
  }
  if (S.restartRequired) {
    out += `<div class="banner info"><div class="grow">Restart TailConnect to apply your Tailscale server change.</div></div>`;
  }
  return out;
}

function sidebar() {
  const sel = selectedDevice();
  const item = d => {
    const st = statusOf(d);
    const sub = d.section === "mine" && d.state === "disconnected" && d.online ? (d.link || st.text) : st.text;
    let right = "";
    if (d.section === "discovered" && !(d.pairing === "requesting" || d.pairing === "waiting") && d.online)
      right = `<button class="btn sm" data-act="pair" data-id="${h(d.id)}">Pair</button>`;
    if (d.section === "tailnet" && d.online)
      right = `<button class="btn sm ghost" data-act="add-device" data-host="${h(d.tailnetIP)}" data-name="${h(d.name)}" title="Add as SSH host">${icon("plus")}SSH</button>`;
    const tag = d.section === "mine" ? "a" : "div";
    const href = d.section === "mine" ? ` href="#/device/${enc(d.id)}"` : "";
    return `<${tag} class="dev-item ${sel && sel.id === d.id ? "sel" : ""}"${href}>
      <span class="os-badge">${h(osShort(d.os))}<span class="dot ${st.dot}"></span></span>
      <span class="meta"><div class="name">${h(d.name)}</div><div class="sub">${h(sub)}</div></span>${right}</${tag}>`;
  };
  const mine = S.devices.filter(d => d.section === "mine");
  const disc = S.devices.filter(d => d.section === "discovered");
  const other = S.devices.filter(d => d.section === "tailnet");
  return `<aside class="sidebar">
    <div class="side-section"><div class="side-title">My devices</div>
      ${mine.length ? mine.map(item).join("") : `<div class="side-empty">No devices yet. Install TailConnect on another computer and sign in with the same Tailscale account — it will appear here.</div>`}
    </div>
    ${disc.length ? `<div class="side-section"><div class="side-title">Available to pair</div>${disc.map(item).join("")}</div>` : ""}
    ${other.length ? `<div class="side-section"><div class="side-title">Other tailnet devices</div>${other.map(item).join("")}</div>` : ""}
    <div class="side-foot">This device: <b>${h(S.self.name)}</b>${S.tailnet.ipv4 ? ` · <span class="mono copy" data-act="copy" data-text="${h(S.tailnet.ipv4)}">${h(S.tailnet.ipv4)}</span>` : ""}</div>
  </aside>`;
}

function selectedDevice() {
  const mine = S.devices.filter(d => d.section === "mine");
  if (route.name === "device") return mine.find(d => d.id === route.arg) || null;
  return mine[0] || null;
}

function viewDevice() {
  const d = selectedDevice();
  if (!d) {
    const disc = S.devices.filter(x => x.section === "discovered");
    return `<div class="welcome">
      <h1>Connect your first device</h1>
      <p class="muted">Install TailConnect on another computer and sign in with <b>${h(S.tailnet.loginName || "the same account")}</b>. Devices on your account pair automatically${S.settings.autoTrustOwnDevices ? "" : " (auto-pairing is off in Settings)"}.</p>
      ${disc.length ? `<p>${disc.length} TailConnect device${disc.length > 1 ? "s are" : " is"} ready to pair in the sidebar.</p>` : ""}
      <p class="muted">Or add any Linux/macOS machine you can SSH into:</p>
      <button class="btn primary" data-act="add-device">${icon("plus")}Add an SSH host</button>
    </div>`;
  }
  const st = statusOf(d);
  const connected = d.state === "connected";
  const working = d.state === "connecting" || d.state === "reconnecting";
  const connBtn = connected || working
    ? `<button class="btn" data-act="disconnect" data-id="${h(d.id)}">${working ? "Cancel" : "Disconnect"}</button>`
    : `<button class="btn primary" data-act="connect" data-id="${h(d.id)}" ${!d.online ? "disabled" : ""}>Connect</button>`;
  const pills = [`<span class="pill ${st.cls}"><span class="dot ${st.dot}"></span>${h(st.text)}</span>`];
  if (d.link && d.online) pills.push(`<span class="pill">${icon("link")}${h(d.link)}</span>`);
  pills.push(`<span class="pill">${h(d.kind === "peer" ? "TailConnect" + (d.version ? " " + d.version : "") : "SSH host")}</span>`);
  if (d.os) pills.push(`<span class="pill">${h(osLabel(d.os))}</span>`);

  let alerts = "";
  if (d.state === "error" && d.error) alerts += `<div class="alert bad">${h(d.error)}</div>`;
  else if (d.state === "reconnecting" && d.error) alerts += `<div class="alert">${h(d.error)} — retrying…</div>`;
  if (d.kind === "peer" && d.online && !d.trustsUs && d.pairing !== "waiting")
    alerts += `<div class="alert">This device doesn't trust this computer yet. <button class="btn sm" data-act="pair" data-id="${h(d.id)}">Pair again</button></div>`;
  if (d.pairing === "waiting" || d.pairing === "requesting") alerts += `<div class="alert info"><span class="spinner sm"></span> ${h(d.pairingMsg || "Pairing…")}</div>`;
  else if (d.pairing === "denied" || d.pairing === "error") alerts += `<div class="alert">${h(d.pairingMsg || "Pairing did not complete.")}</div>`;
  if (d.kind === "peer" && d.online && d.trustsUs && !d.ssh)
    alerts += `<div class="alert">SSH is off on ${h(d.name)}, so Terminal and editors can't connect. Port forwards and the resource monitor still work. ${h(sshHint(d.os))}</div>`;

  const apps = S.builtins.filter(b => b.enabled);
  const canLaunch = d.online && (d.kind !== "peer" || (d.ssh && d.trustsUs));
  const tiles = apps.map(b => {
    const dis = !b.available || !canLaunch;
    const sub = !b.available ? "Not installed" : b.id === "terminal" ? "SSH session" : "Remote window";
    return `<button class="app-tile" data-act="launch" data-id="${h(d.id)}" data-app="${h(b.id)}" ${dis ? "disabled" : ""} title="${h(!b.available ? b.name + " was not found on this computer" : "Open " + b.name + " on " + d.name)}">
      <span class="ico">${icon(APP_ICON[b.id] || "code")}</span><span class="t">${h(b.name)}</span><span class="s">${h(sub)}</span></button>`;
  }).join("");

  return `
    <div class="dev-head">
      <span class="os-badge">${h(osShort(d.os))}</span>
      <div style="flex:1;min-width:0"><h1>${h(d.name)}</h1><div class="pills">${pills.join("")}</div></div>
      ${connBtn}
      <button class="btn icon" data-act="rename" data-id="${h(d.id)}" title="Rename" aria-label="Rename">${icon("edit")}</button>
      <button class="btn icon danger" data-act="remove" data-id="${h(d.id)}" title="Remove device" aria-label="Remove device">${icon("trash")}</button>
    </div>
    ${alerts}
    <h2 class="sec">Apps<span class="spacer"></span><a class="btn sm ghost" href="#/settings/apps">Choose apps</a></h2>
    <div class="apps">${tiles || `<div class="muted">No apps enabled. <a href="#/settings/apps">Choose apps</a></div>`}</div>
    <h2 class="sec">Custom apps &amp; ports<span class="spacer"></span><button class="btn sm ghost" data-act="edit-app">${icon("plus")}New</button></h2>
    ${customApps(d)}
    <h2 class="sec">Resource monitor</h2>
    <div id="monitor">${viewMonitor(d)}</div>
    <h2 class="sec">Details</h2>
    ${details(d)}`;
}

function sshHint(os) {
  if (os === "darwin") return "Turn on System Settings → General → Sharing → Remote Login.";
  if (os === "windows") return "Enable Settings → System → Optional features → OpenSSH Server.";
  return "Install openssh-server (e.g. sudo apt install openssh-server).";
}

function customApps(d) {
  if (!S.apps.length) return `<div class="card empty">No custom apps yet. Forward a port (e.g. Jupyter on 8888) to this computer with one click. <button class="btn sm" data-act="edit-app">Add custom app</button></div>`;
  const runMap = Object.fromEntries((d.running || []).map(r => [r.appID, r]));
  const rows = S.apps.map(a => {
    const r = runMap[a.id];
    const key = `app:${d.id}:${a.id}`;
    const pending = busy.has(key);
    let status = `Remote port ${a.port}${a.script ? " · launch script" : ""}${a.openBrowser ? " · opens browser" : ""}${a.inTerminal ? " · in terminal" : ""}`;
    let btns;
    if (r) {
      status = r.status === "running"
        ? `Running · <span class="mono">localhost:${r.localPort}</span>${r.localPort !== r.remotePort ? ` (remote ${r.remotePort})` : ""}`
        : h(r.status);
      if (r.error) status += ` · <span style="color:var(--warn)">${h(r.error)}</span>`;
      btns = `<a class="btn sm" href="${h(r.url)}" target="_blank" rel="noopener">${icon("ext")}Open</a>
              <button class="btn sm" data-act="app-stop" data-id="${h(d.id)}" data-app="${h(a.id)}">${icon("stop")}Stop</button>`;
    } else {
      btns = `<button class="btn sm primary" data-act="app-start" data-id="${h(d.id)}" data-app="${h(a.id)}" ${pending || !d.online ? "disabled" : ""}>${pending ? '<span class="spinner sm"></span>' : icon("play")}Start</button>`;
    }
    return `<div class="row"><div class="grow"><div class="title">${h(a.name)}</div><div class="sub">${r ? status : h(status)}</div></div>
      ${btns}<button class="btn sm ghost icon" data-act="edit-app" data-app="${h(a.id)}" title="Edit" aria-label="Edit">${icon("edit")}</button></div>`;
  }).join("");
  return `<div class="card list">${rows}</div>`;
}

function details(d) {
  const rows = [];
  if (d.alias) rows.push(["SSH", `<span class="mono copy" data-act="copy" data-text="ssh ${h(d.alias)}">ssh ${h(d.alias)}</span>`]);
  if (d.user) rows.push(["User", h(d.user)]);
  if (d.home) rows.push(["Home", `<span class="mono">${h(d.home)}</span>`]);
  if (d.kind === "ssh") rows.push(["Address", `<span class="mono">${h(d.host)}:${h(d.port || 22)}</span> · ${d.via === "tailnet" ? "via Tailscale" : "direct"}`]);
  if (d.tailnetIP) rows.push(["Tailnet IP", `<span class="mono copy" data-act="copy" data-text="${h(d.tailnetIP)}">${h(d.tailnetIP)}</span>`]);
  if (d.dnsName) rows.push(["DNS name", `<span class="mono">${h(d.dnsName)}</span>`]);
  if (d.loginName) rows.push(["Owner", h(d.loginName)]);
  if (d.kind === "peer") rows.push(["Access to this computer", d.trustedByUs ? "Allowed (paired both ways)" : "Not allowed"]);
  return `<div class="card" style="padding:14px 16px"><dl class="kv">${rows.map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join("")}</dl></div>`;
}

// ---------- resource monitor ----------
function monitorEligible(d) {
  if (!d || !d.online) return false;
  if (d.kind === "peer") return d.trustsUs;
  return d.state === "connected";
}

function syncMonitor() {
  const d = route.name === "settings" ? null : (S && S.tailnet.state === "Running" ? selectedDevice() : null);
  const id = d && monitorEligible(d) ? d.id : null;
  if (id !== mon.id) {
    clearInterval(mon.timer);
    Object.assign(mon, { id, hist: [], last: null, prevNet: null, rate: null, err: null, timer: null });
    if (id) {
      pollStats();
      mon.timer = setInterval(pollStats, 2000);
    }
  }
}

async function pollStats() {
  if (!mon.id || mon.inflight || document.hidden) return;
  const id = mon.id;
  mon.inflight = true;
  try {
    const s = await api.get(`/api/devices/${enc(id)}/stats`);
    if (id !== mon.id) return;
    const t = new Date(s.time).getTime();
    if (mon.prevNet && t > mon.prevNet.t) {
      const dt = (t - mon.prevNet.t) / 1000;
      mon.rate = { rx: Math.max(0, (s.netRx - mon.prevNet.rx) / dt), tx: Math.max(0, (s.netTx - mon.prevNet.tx) / dt) };
    }
    mon.prevNet = { t, rx: s.netRx, tx: s.netTx };
    mon.last = s;
    mon.err = null;
    mon.hist.push({ cpu: s.cpuPercent, mem: s.memTotal ? 100 * s.memUsed / s.memTotal : 0, gpu: s.gpus && s.gpus.length ? s.gpus[0].util : null });
    if (mon.hist.length > 90) mon.hist.shift();
  } catch (e) {
    if (id === mon.id) mon.err = e.message;
  } finally {
    mon.inflight = false;
  }
  const el = document.getElementById("monitor");
  const d = devById(id);
  if (el && d) el.innerHTML = viewMonitor(d);
}
document.addEventListener("visibilitychange", () => { if (!document.hidden) pollStats(); });

function spark(vals, cls) {
  const pts = vals.filter(v => v != null);
  if (pts.length < 2) return `<svg class="spark ${cls || ""}" viewBox="0 0 100 40" preserveAspectRatio="none"></svg>`;
  const n = 90, step = 100 / (n - 1), off = n - vals.length;
  let line = "";
  vals.forEach((v, i) => {
    if (v == null) return;
    const x = (off + i) * step, y = 40 - Math.max(0, Math.min(100, v)) * 0.38 - 1;
    line += (line ? " L" : "M") + x.toFixed(2) + " " + y.toFixed(2);
  });
  const firstX = (off + vals.findIndex(v => v != null)) * step;
  const area = line + ` L100 40 L${firstX.toFixed(2)} 40 Z`;
  return `<svg class="spark ${cls || ""}" viewBox="0 0 100 40" preserveAspectRatio="none"><path class="area" d="${area}"/><path class="line" d="${line}"/></svg>`;
}
function barCls(p) { return p >= 90 ? "bad" : p >= 75 ? "warn" : ""; }

function viewMonitor(d) {
  if (!monitorEligible(d)) {
    if (!d.online) return `<div class="card empty">Device is offline.</div>`;
    if (d.kind === "ssh") return `<div class="card empty">Connect to see live CPU, memory, GPU and disk usage. <button class="btn sm primary" data-act="connect" data-id="${h(d.id)}">Connect</button></div>`;
    return `<div class="card empty">Pair with this device to see its resources.</div>`;
  }
  const s = mon.last;
  if (!s) return mon.err ? `<div class="card empty">Telemetry unavailable: ${h(mon.err)}</div>` : `<div class="card empty"><span class="spinner sm"></span> Collecting…</div>`;
  const memP = s.memTotal ? 100 * s.memUsed / s.memTotal : 0;
  const cards = [];
  cards.push(`<div class="card"><div class="k"><span>CPU</span><span>${h((s.perCore || []).length)} cores</span></div>
    <div class="v">${pct(s.cpuPercent)}${s.load && s.load.length ? ` <small>load ${s.load.map(x => x.toFixed(2)).join(" ")}</small>` : ""}</div>
    ${spark(mon.hist.map(x => x.cpu))}
    <div class="cores" title="Per-core usage">${(s.perCore || []).map(c => `<i style="height:${Math.max(2, Math.min(100, c)).toFixed(0)}%"></i>`).join("")}</div></div>`);
  cards.push(`<div class="card"><div class="k"><span>Memory</span><span>${bytes(s.memTotal)}</span></div>
    <div class="v">${pct(memP)} <small>${bytes(s.memUsed)} used</small></div>
    ${spark(mon.hist.map(x => x.mem), "sky")}
    ${s.swapTotal ? `<div class="disk"><div class="lbl"><span>Swap</span><span>${bytes(s.swapUsed)} / ${bytes(s.swapTotal)}</span></div><div class="bar ${barCls(100 * s.swapUsed / s.swapTotal)}"><i style="width:${(100 * s.swapUsed / s.swapTotal).toFixed(1)}%"></i></div></div>` : ""}</div>`);
  (s.gpus || []).forEach((g, i) => {
    const vram = g.memTotalMiB > 0 ? `${bytes(g.memUsedMiB * 1048576)} / ${bytes(g.memTotalMiB * 1048576)}` : "Unified memory";
    cards.push(`<div class="card"><div class="k"><span>GPU${s.gpus.length > 1 ? " " + i : ""}</span><span title="${h(g.name)}">${h(g.name.replace(/^NVIDIA /, ""))}</span></div>
      <div class="v">${pct(g.util)}${g.tempC >= 0 ? ` <small>${g.tempC.toFixed(0)}°C</small>` : ""}${g.powerW >= 0 ? ` <small>${g.powerW.toFixed(0)} W</small>` : ""}</div>
      ${i === 0 ? spark(mon.hist.map(x => x.gpu)) : ""}
      <div class="disk"><div class="lbl"><span>VRAM</span><span>${vram}</span></div>${g.memTotalMiB > 0 ? `<div class="bar ${barCls(100 * g.memUsedMiB / g.memTotalMiB)}"><i style="width:${(100 * g.memUsedMiB / g.memTotalMiB).toFixed(1)}%"></i></div>` : ""}</div></div>`);
  });
  if ((s.disks || []).length) {
    cards.push(`<div class="card"><div class="k"><span>Disks</span><span></span></div>${s.disks.map(dk => {
      const p = dk.total ? 100 * dk.used / dk.total : 0;
      return `<div class="disk"><div class="lbl"><span class="mono">${h(dk.mount)}</span><span>${bytes(dk.used)} / ${bytes(dk.total)}</span></div><div class="bar ${barCls(p)}"><i style="width:${p.toFixed(1)}%"></i></div></div>`;
    }).join("")}</div>`);
  }
  cards.push(`<div class="card"><div class="k"><span>System</span><span>up ${uptime(s.uptime)}</span></div>
    <dl class="kv" style="margin-top:8px">
      <dt>OS</dt><dd>${h(s.platform || osLabel(s.os))}</dd>
      ${s.kernel ? `<dt>Kernel</dt><dd>${h(s.kernel)}</dd>` : ""}
      ${s.cpuModel ? `<dt>CPU</dt><dd>${h(s.cpuModel)}</dd>` : ""}
      <dt>Network</dt><dd>${mon.rate ? `↓ ${bytes(mon.rate.rx)}/s · ↑ ${bytes(mon.rate.tx)}/s` : "measuring…"}</dd>
    </dl></div>`);
  return `<div class="mon">${cards.join("")}</div>${mon.err ? `<div class="small muted" style="margin-top:6px">Telemetry interrupted: ${h(mon.err)}</div>` : ""}`;
}

// ---------- onboarding ----------
function viewOnboarding() {
  const tn = S.tailnet;
  const signingIn = !!tn.authURL;
  const starting = tn.state === "NoState" || tn.state === "Starting";
  let action;
  if (signingIn) {
    action = `<a class="btn primary lg" href="${h(tn.authURL)}" target="_blank" rel="noopener">${icon("ext")}Continue sign-in in browser</a>
      <p class="small muted" style="margin-top:12px"><span class="spinner sm"></span> Waiting for you to finish signing in…</p>`;
  } else if (starting && !busy.has("login")) {
    action = `<button class="btn primary lg" data-act="login">Sign in with Tailscale</button>
      <p class="small muted" style="margin-top:12px"><span class="spinner sm"></span> Starting secure networking…</p>`;
  } else {
    action = `<button class="btn primary lg" data-act="login" ${busy.has("login") ? "disabled" : ""}>${busy.has("login") ? '<span class="spinner sm"></span>' : ""}Sign in with Tailscale</button>`;
  }
  return `<div class="center"><div class="hero">
    <img src="/icon.png" alt="">
    <h1>Welcome to TailConnect</h1>
    <p>Reach every computer you own — terminals, editors, web apps and live resource stats — over your private, encrypted Tailscale network.</p>
    ${action}
    ${tn.error ? `<div class="alert bad" style="text-align:left">${h(tn.error)}</div>` : ""}
    <div class="steps">
      <div class="step"><b>1</b><div><b>Sign in</b> with Google, Microsoft, GitHub or Apple via Tailscale. A free account is created if you don't have one.</div></div>
      <div class="step"><b>2</b><div><b>Install TailConnect on your other devices</b> and sign in with the same account.</div></div>
      <div class="step"><b>3</b><div>They <b>pair automatically</b> and show up here, ready for one-click Terminal, VS Code, Cursor and port forwards.</div></div>
    </div>
    <p class="small faint" style="margin-top:22px">Self-hosting Headscale? <a href="#/settings/tailscale">Use a custom control server</a></p>
  </div></div>`;
}

// ---------- settings ----------
const TABS = [["general", "General"], ["apps", "Apps"], ["custom", "Custom apps"], ["access", "Devices & access"], ["tailscale", "Tailscale"], ["about", "About"]];

function toggle(key, checked, extra) {
  return `<label class="switch"><input type="checkbox" data-set="${key}" ${checked ? "checked" : ""} ${extra || ""}><span></span></label>`;
}
function field(lbl, help, ctl) {
  return `<div class="field"><div><div class="lbl">${lbl}</div>${help ? `<div class="help">${help}</div>` : ""}</div><div class="ctl">${ctl}</div></div>`;
}

function viewSettings(tab) {
  const st = S.settings;
  let body = "";
  if (tab === "general") {
    const terms = S.terminals || [];
    body = `<div class="card">
      ${field("Pair automatically with my devices", "TailConnect devices signed in to the same Tailscale account are trusted and connected without prompts.", toggle("autoTrustOwnDevices", st.autoTrustOwnDevices))}
      ${field("Start at login", "Launch TailConnect in the tray when you log in.", toggle("startAtLogin", st.startAtLogin, S.headless ? "disabled" : ""))}
      ${field("Terminal", "Used for SSH sessions and custom apps that run in a terminal.",
        `<select data-set="terminal"><option value="">Automatic</option>${terms.map(t => `<option ${t === st.terminal ? "selected" : ""}>${h(t)}</option>`).join("")}</select>`)}
      ${field("Theme", "", `<select data-set="theme">${["system", "light", "dark"].map(t => `<option value="${t}" ${t === st.theme ? "selected" : ""}>${t[0].toUpperCase() + t.slice(1)}</option>`).join("")}</select>`)}
      ${field("Add devices to ~/.ssh/config", `Lets any tool use <code>ssh tc-&lt;device&gt;</code>. Hosts live in <code>${h(S.self.sshConfig)}</code>.`, toggle("manageSSHConfig", st.manageSSHConfig))}
      ${field("Local SSH server port", "Where this computer's SSH server listens (shared with paired devices).", `<input type="number" min="1" max="65535" style="width:96px" data-set-num="localSSHPort" value="${h(st.localSSHPort)}">`)}
    </div>`;
  } else if (tab === "apps") {
    body = `<div class="card">${S.builtins.map(b => field(h(b.name),
      b.available ? "Installed" : `Not found on this computer${b.id === "terminal" ? "" : " — install it, or make sure its command-line launcher is on PATH"}`,
      `<label class="switch"><input type="checkbox" data-app-toggle="${h(b.id)}" ${b.enabled ? "checked" : ""}><span></span></label>`)).join("")}</div>
      <p class="small muted">Editors open a remote window over SSH (VS Code-family editors need the Remote - SSH extension).</p>`;
  } else if (tab === "custom") {
    body = `<div class="card list">${S.apps.length ? S.apps.map(a => `<div class="row"><div class="grow"><div class="title">${h(a.name)}</div>
        <div class="sub">Port ${a.port}${a.script ? " · launch script" : ""}${a.openBrowser ? " · opens http://localhost:" + a.port + "/" + h(a.path || "") : ""}${a.inTerminal ? " · runs in terminal" : ""}</div></div>
        <button class="btn sm" data-act="edit-app" data-app="${h(a.id)}">${icon("edit")}Edit</button>
        <button class="btn sm ghost icon danger" data-act="delete-app" data-app="${h(a.id)}" title="Delete" aria-label="Delete">${icon("trash")}</button></div>`).join("")
      : `<div class="empty">No custom apps.</div>`}</div>
      <p><button class="btn primary" data-act="edit-app">${icon("plus")}Add custom app</button></p>
      <p class="small muted">A custom app forwards a port from the device to <code>localhost</code> on this computer. An optional bash launch script starts the service on the device (Linux/macOS with SSH). Scripts and logs live in <code>~/.config/tailconnect</code> on the device.</p>`;
  } else if (tab === "access") {
    body = `<p class="muted">These devices can open terminals, editors and forwarded ports on <b>${h(S.self.name)}</b>.</p>
      <div class="card list">${S.trusted.length ? S.trusted.map(t => `<div class="row"><div class="grow"><div class="title">${h(t.name)}</div>
        <div class="sub">${h(t.loginName || "")} · paired ${h(ago(t.trustedAt))}</div></div>
        <button class="btn sm danger" data-act="revoke" data-node="${h(t.nodeID)}">Revoke</button></div>`).join("") : `<div class="empty">No devices can access this computer.</div>`}</div>
      <h2 class="sec">This computer</h2>
      <div class="card" style="padding:14px 16px"><dl class="kv">
        <dt>SSH server</dt><dd>${S.self.ssh ? `Running on port ${S.self.sshPort}` : `Not detected on port ${S.self.sshPort} — other devices can forward ports but not open terminals here. ${h(sshHint(S.self.os))}`}</dd>
        <dt>Public key</dt><dd><span class="mono copy" data-act="copy" data-text="${h(S.self.publicKey)}">${h(S.self.publicKey.slice(0, 48))}…</span></dd>
      </dl></div>`;
  } else if (tab === "tailscale") {
    const tn = S.tailnet;
    body = `<div class="card" style="padding:14px 16px"><dl class="kv">
        <dt>Status</dt><dd>${h(tn.state)}</dd>
        ${tn.loginName ? `<dt>Account</dt><dd>${h(tn.loginName)}</dd>` : ""}
        ${tn.tailnet ? `<dt>Tailnet</dt><dd>${h(tn.tailnet)}</dd>` : ""}
        ${tn.dnsName ? `<dt>This device</dt><dd class="mono">${h(tn.dnsName)}</dd>` : ""}
        ${tn.ipv4 ? `<dt>IP</dt><dd class="mono">${h(tn.ipv4)}</dd>` : ""}
      </dl>
      <div style="margin-top:14px;display:flex;gap:8px">${tn.state === "Running"
        ? `<button class="btn danger" data-act="logout">Sign out</button>`
        : `<button class="btn primary" data-act="login">Sign in</button>`}</div></div>
      <h2 class="sec">Advanced</h2>
      <div class="card">${field("Control server", "Leave empty for Tailscale. Set a Headscale URL to self-host. Takes effect after restart.",
        `<input type="url" placeholder="https://headscale.example.com" style="width:260px" id="controlURL" value="${h(st.controlURL)}"><button class="btn" data-act="save-control">Save</button>`)}</div>
      <p class="small muted">TailConnect runs its own Tailscale node (it appears as <code>tc-…</code> in your admin console) — you don't need the Tailscale app installed. It works alongside it if you have it.</p>`;
  } else if (tab === "about") {
    body = `<div class="card" style="padding:14px 16px"><dl class="kv">
        <dt>Version</dt><dd>${h(S.version)}</dd>
        <dt>Device</dt><dd>${h(S.self.name)} · ${h(osLabel(S.self.os))} ${h(S.self.arch)}</dd>
        ${S.logDir ? `<dt>Logs</dt><dd class="mono">${h(S.logDir)}</dd>` : ""}
      </dl>
      <div style="margin-top:14px;display:flex;gap:8px;flex-wrap:wrap">
        <button class="btn" data-act="open-logs">Open logs folder</button>
        <a class="btn" href="https://github.com/imbikramsaha/TailConnect" target="_blank" rel="noopener">${icon("ext")}Project page</a>
        <button class="btn danger" data-act="quit">${icon("power")}Quit TailConnect</button>
      </div></div>`;
  }
  return `<div class="settings">
    <nav class="tabs">${TABS.map(([k, v]) => `<a class="tab ${k === tab ? "sel" : ""}" href="#/settings/${k}">${v}</a>`).join("")}</nav>
    <section><h1 style="margin:0 0 16px;font-size:22px">${h((TABS.find(t => t[0] === tab) || ["", "Settings"])[1])}</h1>${body}</section>
  </div>`;
}

// ---------- dialogs ----------
const root = document.getElementById("dialog-root");
let dialogSubmit = null;

function openDialog(title, sub, bodyHTML, okLabel, onSubmit, opts = {}) {
  root.innerHTML = `<div class="overlay"><form class="dialog" novalidate>
    <header><h3>${h(title)}</h3>${sub ? `<p>${sub}</p>` : ""}</header>
    <div class="body">${bodyHTML}</div>
    <footer><span class="err"></span><button type="button" class="btn" data-dlg="cancel">Cancel</button>
      <button type="submit" class="btn ${opts.danger ? "danger" : "primary"}">${h(okLabel)}</button></footer>
  </form></div>`;
  dialogSubmit = onSubmit;
  const first = root.querySelector("input:not([type=hidden]),textarea,select");
  if (first) first.focus();
}
function closeDialog() { root.innerHTML = ""; dialogSubmit = null; if (pendingRender) render(); }
root.addEventListener("click", e => {
  if (e.target.matches(".overlay") || e.target.closest("[data-dlg=cancel]")) closeDialog();
  const seg = e.target.closest(".seg button");
  if (seg) {
    e.preventDefault();
    seg.parentElement.querySelectorAll("button").forEach(b => b.classList.toggle("sel", b === seg));
    seg.parentElement.dataset.value = seg.dataset.v;
  }
});
root.addEventListener("submit", async e => {
  e.preventDefault();
  if (!dialogSubmit) return;
  const form = e.target;
  const btn = form.querySelector("button[type=submit]");
  const err = form.querySelector(".err");
  const label = btn.textContent;
  btn.disabled = true;
  btn.innerHTML = '<span class="spinner sm"></span> ' + h(label);
  err.textContent = "";
  try {
    await dialogSubmit(form);
    closeDialog();
  } catch (ex) {
    err.textContent = ex.message;
    btn.disabled = false;
    btn.textContent = label;
  }
});
document.addEventListener("keydown", e => { if (e.key === "Escape" && root.innerHTML) closeDialog(); });

function addDeviceDialog(prefHost, prefName) {
  openDialog("Add an SSH host", "Any Linux or macOS machine you can SSH into — on your LAN, the internet, or your tailnet. Your password is used once to install TailConnect's key and is never stored.", `
    <label class="f"><span>Name</span><input type="text" name="name" placeholder="GPU server" value="${h(prefName || "")}"></label>
    <div class="grid2">
      <label class="f"><span>Hostname or IP</span><input type="text" name="host" required placeholder="192.168.1.20" value="${h(prefHost || "")}" autocapitalize="off" spellcheck="false"></label>
      <label class="f"><span>SSH port</span><input type="number" name="port" value="22" min="1" max="65535"></label>
    </div>
    <label class="f"><span>Username</span><input type="text" name="user" required autocapitalize="off" spellcheck="false"></label>
    <label class="f"><span>Password</span><input type="password" name="password" autocomplete="off"><small>Leave empty if TailConnect's key is already installed.</small></label>
    <div class="f"><span>Connect</span><div class="seg" data-name="via" data-value="${prefHost ? "tailnet" : ""}">
      <button type="button" data-v="" class="${prefHost ? "" : "sel"}">Auto</button>
      <button type="button" data-v="direct">Directly</button>
      <button type="button" data-v="tailnet" class="${prefHost ? "sel" : ""}">Through Tailscale</button></div></div>`,
    "Add device", async form => {
      const f = Object.fromEntries(new FormData(form));
      const dev = await api.post("/api/devices/ssh", {
        name: f.name, host: f.host, port: parseInt(f.port, 10) || 22, user: f.user, password: f.password,
        via: form.querySelector(".seg").dataset.value || "",
      });
      toast(`Added ${dev.name} — connect with “ssh ${dev.alias}”`);
      location.hash = "#/device/" + enc(dev.id);
    });
}

function appDialog(appID) {
  const a = (S.apps.find(x => x.id === appID)) || { name: "", port: "", script: "", openBrowser: true, path: "", inTerminal: false };
  openDialog(appID ? "Edit custom app" : "New custom app", "Forward a port from the connected device to localhost on this computer — optionally starting the service first.", `
    <div class="grid2">
      <label class="f"><span>Name</span><input type="text" name="name" required placeholder="JupyterLab" value="${h(a.name)}"></label>
      <label class="f"><span>Port</span><input type="number" name="port" required min="1" max="65535" placeholder="8888" value="${h(a.port)}"></label>
    </div>
    <label class="f"><span>Launch script (bash, optional)</span><textarea name="script" rows="5" spellcheck="false" placeholder="jupyter lab --no-browser --port=8888">${h(a.script || "")}</textarea>
      <small>Runs from your home directory on the device. Leave empty to only forward a port that's already in use.</small></label>
    <label class="check"><input type="checkbox" name="openBrowser" ${a.openBrowser ? "checked" : ""}> Open in browser at <span class="mono">http://localhost:PORT/</span><input type="text" name="path" placeholder="path" style="height:28px;width:120px" value="${h(a.path || "")}"></label>
    <label class="check"><input type="checkbox" name="inTerminal" ${a.inTerminal ? "checked" : ""}> Run the script in a terminal window (stays open if it fails)</label>`,
    appID ? "Save" : "Create", async form => {
      const f = new FormData(form);
      await api.post("/api/apps", {
        id: appID || "", name: f.get("name"), port: parseInt(f.get("port"), 10) || 0, script: f.get("script"),
        openBrowser: f.get("openBrowser") === "on", path: f.get("path"), inTerminal: f.get("inTerminal") === "on",
      });
      toast(appID ? "Saved" : "Custom app created");
    });
}

// ---------- actions ----------
async function run(key, fn, okMsg) {
  if (busy.has(key)) return;
  busy.add(key);
  render();
  try {
    await fn();
    if (okMsg) toast(okMsg);
  } catch (e) {
    toast(e.message, true);
  } finally {
    busy.delete(key);
    await refresh();
  }
}

const actions = {
  "add-device": el => addDeviceDialog(el.dataset.host, el.dataset.name),
  login: () => run("login", async () => {
    const r = await api.post("/api/login");
    if (r && r.authURL) S.tailnet.authURL = r.authURL;
  }),
  logout: () => {
    openDialog("Sign out of Tailscale?", "This computer leaves your tailnet; paired devices stay saved and reconnect when you sign in again.", "", "Sign out",
      async () => { await api.post("/api/logout"); }, { danger: true });
  },
  connect: el => run("conn:" + el.dataset.id, () => api.post(`/api/devices/${enc(el.dataset.id)}/connect`)),
  disconnect: el => run("conn:" + el.dataset.id, () => api.post(`/api/devices/${enc(el.dataset.id)}/disconnect`)),
  pair: el => run("pair:" + el.dataset.id, () => api.post(`/api/devices/${enc(el.dataset.id)}/pair`), "Pairing request sent"),
  launch: el => {
    const b = S.builtins.find(x => x.id === el.dataset.app);
    run("launch:" + el.dataset.id + el.dataset.app, () => api.post(`/api/devices/${enc(el.dataset.id)}/launch`, { app: el.dataset.app }), `Opening ${b ? b.name : "app"}…`);
  },
  "app-start": el => run(`app:${el.dataset.id}:${el.dataset.app}`, () => api.post(`/api/devices/${enc(el.dataset.id)}/apps/${enc(el.dataset.app)}/start`)),
  "app-stop": el => run(`app:${el.dataset.id}:${el.dataset.app}`, () => api.post(`/api/devices/${enc(el.dataset.id)}/apps/${enc(el.dataset.app)}/stop`)),
  "edit-app": el => appDialog(el.dataset.app),
  "delete-app": el => {
    const a = S.apps.find(x => x.id === el.dataset.app);
    openDialog(`Delete “${a ? a.name : "app"}”?`, "It is stopped on every device.", "", "Delete", () => api.del(`/api/apps/${enc(el.dataset.app)}`), { danger: true });
  },
  rename: el => {
    const d = devById(el.dataset.id);
    openDialog("Rename device", "The SSH alias changes to match.", `<label class="f"><span>Name</span><input type="text" name="name" required value="${h(d.name)}"></label>`, "Rename",
      form => api.patch(`/api/devices/${enc(d.id)}`, { name: new FormData(form).get("name") }));
  },
  remove: el => {
    const d = devById(el.dataset.id);
    openDialog(`Remove ${d.name}?`, d.kind === "peer"
      ? "Both computers stop trusting each other and it won't be paired automatically again. You can pair it again later."
      : "TailConnect forgets this host. Its key stays in the host's authorized_keys until you remove it there.",
      "", "Remove", async () => { await api.del(`/api/devices/${enc(d.id)}`); location.hash = "#/"; }, { danger: true });
  },
  "pair-decide": el => run("decide:" + el.dataset.id, () => api.post(`/api/pair-requests/${enc(el.dataset.id)}/${el.dataset.v}`),
    el.dataset.v === "approve" ? "Device approved" : "Request declined"),
  revoke: el => {
    const t = S.trusted.find(x => x.nodeID === el.dataset.node);
    openDialog(`Revoke ${t ? t.name : "device"}?`, "It can no longer open terminals, editors or ports on this computer. Its key is removed from authorized_keys.", "", "Revoke",
      () => api.del(`/api/trusted/${enc(el.dataset.node)}`), { danger: true });
  },
  "save-control": () => run("control", () => api.put("/api/settings", { controlURL: document.getElementById("controlURL").value.trim() }), "Saved — restart TailConnect to apply"),
  "open-logs": () => run("logs", () => api.post("/api/open-logs")),
  quit: () => openDialog("Quit TailConnect?", "Connections, tunnels and running custom apps are closed. Other devices can't reach this one until it runs again.", "", "Quit",
    async () => { await api.post("/api/quit"); setTimeout(() => window.close(), 300); app.innerHTML = `<div class="center"><div class="hero"><h1>TailConnect has quit</h1><p>You can close this window.</p></div></div>`; }, { danger: true }),
  copy: el => copy(el.dataset.text),
};

document.addEventListener("click", e => {
  const el = e.target.closest("[data-act]");
  if (!el) return;
  const fn = actions[el.dataset.act];
  if (!fn) return;
  e.preventDefault();
  fn(el);
});

document.addEventListener("change", async e => {
  const el = e.target;
  let patch = null;
  if (el.dataset.set) patch = { [el.dataset.set]: el.type === "checkbox" ? el.checked : el.value };
  else if (el.dataset.setNum) patch = { [el.dataset.setNum]: parseInt(el.value, 10) || 0 };
  else if (el.dataset.appToggle) {
    const on = new Set(S.builtins.filter(b => b.enabled).map(b => b.id));
    el.checked ? on.add(el.dataset.appToggle) : on.delete(el.dataset.appToggle);
    patch = { enabledApps: S.builtins.map(b => b.id).filter(id => on.has(id)) };
  }
  if (!patch) return;
  try {
    await api.put("/api/settings", patch);
  } catch (ex) {
    toast(ex.message, true);
  }
  el.blur();
  await refresh();
});

// ---------- live updates ----------
let refreshing = null;
async function refresh() {
  if (refreshing) return refreshing;
  refreshing = (async () => {
    try {
      S = await api.get("/api/state");
      render();
    } catch (e) {
      if (/unauthorized/i.test(e.message)) location.reload();
    } finally {
      refreshing = null;
    }
  })();
  return refreshing;
}

function connectEvents() {
  const es = new EventSource("/api/events");
  es.addEventListener("change", () => refresh());
  es.onerror = () => {
    es.close();
    setTimeout(connectEvents, 2000);
  };
}

refresh().then(connectEvents);
setInterval(refresh, 15000); // relative times, safety net
