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
  screen: '<rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/>',
  pulse: '<path d="M3 12h4l3-8 4 16 3-8h4"/>',
  upload: '<path d="M12 16V4"/><path d="M7 9l5-5 5 5"/><path d="M5 20h14"/>',
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
  syncExtras();
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

const ACCESS = [["full", "Full access", "Terminal, editors, files, ports and monitor"], ["ports", "Ports & monitor", "Forwarded ports, screen sharing and the resource monitor — no shell"], ["stats", "Monitor only", "Only the resource monitor"]];
function accessLabel(a) { const x = ACCESS.find(v => v[0] === a); return x ? x[1] : "No access"; }
function accessSelect(attrs, cur) {
  return `<select ${attrs}>${ACCESS.map(([v, l]) => `<option value="${v}" ${v === cur ? "selected" : ""}>${l}</option>`).join("")}</select>`;
}

function banners() {
  let out = "";
  for (const r of S.pairRequests) {
    out += `<div class="banner">
      <div class="grow"><b>${h(r.name)}</b> wants to pair with this device
        <div class="small muted">${h(osLabel(r.os))} · ${h(r.loginName)}${r.sameUser ? " (your account)" : ""} · ${h(ago(r.created))}</div>
        <div class="small" style="margin-top:4px">Check that <b>${h(r.name)}</b> shows the code <span class="code">${h(r.code)}</span></div></div>
      ${accessSelect(`data-pair-access="${h(r.id)}" aria-label="Access to grant"`, S.settings.ownDeviceAccess && r.sameUser ? S.settings.ownDeviceAccess : "full")}
      <button class="btn sm" data-act="pair-decide" data-id="${h(r.id)}" data-v="deny">Decline</button>
      <button class="btn sm primary" data-act="pair-decide" data-id="${h(r.id)}" data-v="approve">Approve</button>
    </div>`;
  }
  const u = S.update || {};
  if (u.available && !u.installing) {
    out += `<div class="banner info"><div class="grow"><b>TailConnect ${h(u.latest)}</b> is available (you have ${h(u.current)}).${u.error ? ` <span class="muted">${h(u.error)}</span>` : ""}</div>
      <button class="btn sm" data-act="ext" data-url="${h(u.url)}">Release notes</button>
      ${u.canInstall ? `<button class="btn sm primary" data-act="update-install">Install &amp; restart</button>` : `<button class="btn sm primary" data-act="ext" data-url="${h(u.url)}">Download</button>`}</div>`;
  } else if (u.installing) {
    out += `<div class="banner info"><div class="grow"><span class="spinner sm"></span> Installing TailConnect ${h(u.latest)}… it will restart by itself.</div></div>`;
  }
  if (S.installWarning) {
    out += `<div class="banner"><div class="grow">${h(S.installWarning)}</div></div>`;
  }
  if (S.restartRequired) {
    out += `<div class="banner info"><div class="grow">Restart TailConnect to apply your connection change.</div><button class="btn sm primary" data-act="restart">Restart now</button></div>`;
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
  const peer = d.kind === "peer";
  const full = !peer || d.access === "full";
  const portsOK = !peer || d.access === "full" || d.access === "ports";
  let connBtn;
  if (!d.online && d.canWake) connBtn = `<button class="btn primary" data-act="wake" data-id="${h(d.id)}">${icon("power")}Wake</button>`;
  else if (connected || working) connBtn = `<button class="btn" data-act="disconnect" data-id="${h(d.id)}">${working ? "Cancel" : "Disconnect"}</button>`;
  else connBtn = `<button class="btn primary" data-act="connect" data-id="${h(d.id)}" ${!d.online ? "disabled" : ""}>Connect</button>`;
  const pills = [`<span class="pill ${st.cls}"><span class="dot ${st.dot}"></span>${h(st.text)}</span>`];
  if (d.link && d.online) pills.push(`<span class="pill">${icon("link")}${h(d.link)}</span>`);
  pills.push(`<span class="pill">${h(peer ? "TailConnect" + (d.version ? " " + d.version : "") : "SSH host")}</span>`);
  if (d.os) pills.push(`<span class="pill">${h(osLabel(d.os))}</span>`);
  if (peer && d.access) pills.push(`<span class="pill" title="What this computer may do on ${h(d.name)}">You: ${h(accessLabel(d.access))}</span>`);

  let alerts = "";
  if (d.state === "error" && d.error) alerts += `<div class="alert bad">${h(d.error)}</div>`;
  else if (d.state === "reconnecting" && d.error) alerts += `<div class="alert">${h(d.error)} — retrying…</div>`;
  if (!d.online && peer) alerts += `<div class="alert">${h(d.name)} is offline.${d.canWake ? " It can be woken over its local network." : ""}</div>`;
  if (peer && d.online && !d.trustsUs && d.pairing !== "waiting")
    alerts += `<div class="alert">This device doesn't trust this computer yet. <button class="btn sm" data-act="pair" data-id="${h(d.id)}">Pair again</button></div>`;
  if (d.pairing === "waiting" || d.pairing === "requesting") alerts += `<div class="alert info"><span class="spinner sm"></span> ${h(d.pairingMsg || "Pairing…")}${d.pairCode ? ` <span class="code">${h(d.pairCode)}</span>` : ""}</div>`;
  else if (d.pairing === "denied" || d.pairing === "error") alerts += `<div class="alert">${h(d.pairingMsg || "Pairing did not complete.")}</div>`;
  if (peer && d.online && d.trustsUs && full && !d.ssh)
    alerts += `<div class="alert">Terminal and editors need SSH on ${h(d.name)}: turn on <b>Built-in SSH</b> in TailConnect's settings there (one switch), or ${h(sshHint(d.os))}</div>`;

  const apps = S.builtins.filter(b => b.enabled);
  const canLaunch = d.online && full && (!peer || (d.ssh && d.trustsUs));
  const tiles = apps.map(b => {
    const dis = !b.available || !canLaunch;
    const sub = !b.available ? "Not installed" : !full ? "Needs full access" : b.id === "terminal" ? "SSH session" : "Remote window";
    return `<button class="app-tile" data-act="launch" data-id="${h(d.id)}" data-app="${h(b.id)}" ${dis ? "disabled" : ""} title="${h(!b.available ? b.name + " was not found on this computer" : "Open " + b.name + " on " + d.name)}">
      <span class="ico">${icon(APP_ICON[b.id] || "code")}</span><span class="t">${h(b.name)}</span><span class="s">${h(sub)}</span></button>`;
  }).join("");
  const extraTiles = portsOK ? `<button class="app-tile" data-act="remote-desktop" data-id="${h(d.id)}" ${!d.online ? "disabled" : ""} title="Screen Sharing / Remote Desktop">
      <span class="ico">${icon("screen")}</span><span class="t">Screen</span><span class="s">${d.os === "darwin" ? "Screen Sharing" : d.os === "windows" ? "Remote Desktop" : "RDP / VNC"}</span></button>` : "";

  return `
    <div class="dev-head">
      <span class="os-badge">${h(osShort(d.os))}</span>
      <div style="flex:1;min-width:0"><h1>${h(d.name)}</h1><div class="pills">${pills.join("")}</div></div>
      ${connBtn}
      <button class="btn icon" data-act="diagnose" data-id="${h(d.id)}" title="Test connection" aria-label="Test connection" ${!d.online ? "disabled" : ""}>${icon("pulse")}</button>
      <button class="btn icon" data-act="rename" data-id="${h(d.id)}" title="Rename" aria-label="Rename">${icon("edit")}</button>
      <button class="btn icon danger" data-act="remove" data-id="${h(d.id)}" title="Remove device" aria-label="Remove device">${icon("trash")}</button>
    </div>
    ${alerts}
    <h2 class="sec">Apps<span class="spacer"></span><a class="btn sm ghost" href="#/settings/apps">Choose apps</a></h2>
    <div class="apps">${tiles}${extraTiles}</div>
    ${portsOK ? `<h2 class="sec">Running on ${h(d.name)}<span class="spacer"></span><button class="btn sm ghost" data-act="ex-refresh" data-what="services">Refresh</button></h2>
    <div id="ex-services">${viewServices(d)}</div>` : ""}
    <h2 class="sec">Custom apps &amp; ports<span class="spacer"></span><button class="btn sm ghost" data-act="edit-app">${icon("plus")}New</button></h2>
    ${customApps(d)}
    ${full ? `<h2 class="sec">Files &amp; clipboard</h2>${viewTransfer(d)}` : ""}
    <h2 class="sec">Resource monitor</h2>
    <div id="monitor">${viewMonitor(d)}</div>
    <div id="ex-procs">${viewProcs(d)}</div>
    ${portsOK ? `<div id="ex-docker">${viewDocker(d)}</div>` : ""}
    <h2 class="sec">Details</h2>
    ${details(d)}`;
}

// ---------- device extras: services, processes, docker ----------
const ex = { id: null, services: null, procs: null, docker: null, err: {}, timer: null, loading: new Set() };

function syncExtras() {
  const d = route.name === "settings" ? null : (S && S.tailnet.state === "Running" ? selectedDevice() : null);
  const id = d && monitorEligible(d) ? d.id : null;
  if (id === ex.id) return;
  clearInterval(ex.timer);
  Object.assign(ex, { id, services: null, procs: null, docker: null, err: {}, timer: null });
  if (!id) return;
  const peer = d.kind === "peer";
  const portsOK = !peer || d.access === "full" || d.access === "ports";
  if (portsOK) { loadExtra("services"); loadExtra("docker"); }
  loadExtra("procs");
  ex.timer = setInterval(() => { if (!document.hidden && ex.open) loadExtra("procs"); }, 5000);
}

const EX_PATH = { services: "services", procs: "processes", docker: "containers" };
async function loadExtra(what) {
  const id = ex.id;
  if (!id || ex.loading.has(what)) return;
  ex.loading.add(what);
  rerenderExtra(what);
  try {
    const v = await api.get(`/api/devices/${enc(id)}/${EX_PATH[what]}`);
    if (id !== ex.id) return;
    ex[what] = v || [];
    delete ex.err[what];
  } catch (e) {
    if (id === ex.id) ex.err[what] = e.message;
  } finally {
    ex.loading.delete(what);
  }
  rerenderExtra(what);
}

function rerenderExtra(what) {
  const d = devById(ex.id);
  if (!d) return;
  const el = document.getElementById("ex-" + what);
  if (!el) return;
  el.innerHTML = what === "services" ? viewServices(d) : what === "procs" ? viewProcs(d) : viewDocker(d);
}

function viewServices(d) {
  if (!monitorEligible(d)) return `<div class="card empty">${d.online ? "Connect to see what's running." : "Device is offline."}</div>`;
  if (ex.err.services) return `<div class="card empty">Couldn't list services: ${h(ex.err.services)}</div>`;
  if (!ex.services) return `<div class="card empty"><span class="spinner sm"></span> Looking for running services…</div>`;
  const running = Object.fromEntries((d.running || []).map(r => [r.remotePort, r]));
  const saved = new Set(S.apps.filter(a => !a.devices || !a.devices.length || a.devices.includes(d.id)).map(a => a.port));
  const list = ex.services.filter(s => !saved.has(s.port));
  if (!list.length) return `<div class="card empty">No other services are listening on ${h(d.name)}.</div>`;
  return `<div class="card list">${list.map(s => {
    const r = running[s.port];
    const name = s.label || s.process || "Port " + s.port;
    const sub = `Port ${s.port}${s.process && s.label ? " · " + h(s.process) : ""}${r ? ` · forwarded to <span class="mono">localhost:${r.localPort}</span>` : ""}`;
    const btns = r
      ? `${s.web ? `<button class="btn sm" data-act="ext" data-url="${h(r.url)}">${icon("ext")}Open</button>` : ""}<button class="btn sm" data-act="app-stop" data-id="${h(d.id)}" data-app="${h(r.appID)}">${icon("stop")}Stop</button>`
      : `<button class="btn sm ${s.web ? "primary" : ""}" data-act="forward" data-id="${h(d.id)}" data-port="${s.port}" data-label="${h(name)}" data-open="${s.web ? 1 : 0}">${s.web ? icon("ext") + "Open" : icon("link") + "Forward"}</button>`;
    return `<div class="row"><div class="grow"><div class="title">${h(name)}</div><div class="sub">${sub}</div></div>${btns}
      <button class="btn sm ghost icon" data-act="save-service" data-port="${s.port}" data-label="${h(name)}" data-web="${s.web ? 1 : 0}" title="Save as custom app" aria-label="Save as custom app">${icon("plus")}</button></div>`;
  }).join("")}</div>`;
}

function viewProcs(d) {
  if (!monitorEligible(d)) return "";
  const full = d.kind !== "peer" || d.access === "full";
  const head = `<h2 class="sec">Processes<span class="spacer"></span>${ex.open ? `<button class="btn sm ghost" data-act="ex-refresh" data-what="procs">Refresh</button>` : ""}<button class="btn sm ghost" data-act="toggle-procs">${ex.open ? "Hide" : "Show"}</button></h2>`;
  if (!ex.open) return head;
  if (ex.err.procs) return head + `<div class="card empty">Couldn't list processes: ${h(ex.err.procs)}</div>`;
  if (!ex.procs) return head + `<div class="card empty"><span class="spinner sm"></span> Loading…</div>`;
  const rows = ex.procs.slice(0, 15).map(p => `<tr>
    <td class="mono">${p.pid}</td><td title="${h(p.cmd || p.name)}">${h(p.name || "?")}</td><td class="muted">${h(p.user || "")}</td>
    <td class="num">${p.cpu.toFixed(1)}%</td><td class="num">${bytes(p.rss)}</td><td class="num">${p.gpuMiB ? bytes(p.gpuMiB * 1048576) : ""}</td>
    <td>${full ? `<button class="btn sm ghost danger" data-act="kill" data-id="${h(d.id)}" data-pid="${p.pid}" data-name="${h(p.name)}">Stop</button>` : ""}</td></tr>`).join("");
  return head + `<div class="card table-wrap"><table class="tbl"><thead><tr><th>PID</th><th>Process</th><th>User</th><th class="num">CPU</th><th class="num">Memory</th><th class="num">GPU</th><th></th></tr></thead><tbody>${rows}</tbody></table></div>`;
}

function viewDocker(d) {
  if (!monitorEligible(d)) return "";
  const full = d.kind !== "peer" || d.access === "full";
  if (ex.err.docker) return /not installed|No such file|executable file not found|Cannot connect/i.test(ex.err.docker) ? "" :
    `<h2 class="sec">Docker</h2><div class="card empty">${h(ex.err.docker)}</div>`;
  if (!ex.docker || !ex.docker.length) return "";
  return `<h2 class="sec">Docker<span class="spacer"></span><button class="btn sm ghost" data-act="ex-refresh" data-what="docker">Refresh</button></h2>
    <div class="card list">${ex.docker.map(c => {
      const up = c.state === "running";
      const acts = !full ? "" : up
        ? `<button class="btn sm" data-act="docker" data-id="${h(d.id)}" data-cid="${h(c.id)}" data-v="restart">Restart</button><button class="btn sm" data-act="docker" data-id="${h(d.id)}" data-cid="${h(c.id)}" data-v="stop">${icon("stop")}Stop</button>`
        : `<button class="btn sm primary" data-act="docker" data-id="${h(d.id)}" data-cid="${h(c.id)}" data-v="start">${icon("play")}Start</button>`;
      return `<div class="row"><span class="dot ${up ? "on" : ""}"></span><div class="grow"><div class="title">${h(c.name)}</div>
        <div class="sub">${h(c.image)} · ${h(c.status)}${c.ports ? " · " + h(c.ports) : ""}</div></div>${acts}</div>`;
    }).join("")}</div>`;
}

function viewTransfer(d) {
  const peer = d.kind === "peer";
  const off = !d.online ? "disabled" : "";
  return `<div class="card transfer" data-drop="${h(d.id)}">
    <div class="grow"><div class="title">Send files to ${h(d.name)}</div>
      <div class="sub">Drop files here or choose them. They're saved to <span class="mono">Downloads/TailConnect</span> on ${h(d.name)}.</div>
      <div id="xfer-progress" class="small muted"></div></div>
    <label class="btn sm ${off ? "disabled" : ""}">${icon("upload")}Choose files<input type="file" multiple hidden data-send-files="${h(d.id)}" ${off}></label>
    ${peer ? `<button class="btn sm" data-act="clip-push" data-id="${h(d.id)}" ${off} title="Copy this computer's clipboard to ${h(d.name)}">${icon("copy")}Send clipboard</button>
    <button class="btn sm" data-act="clip-pull" data-id="${h(d.id)}" ${off} title="Copy ${h(d.name)}'s clipboard to this computer">Get its clipboard</button>` : ""}
  </div>`;
}

async function sendFiles(id, files) {
  const d = devById(id);
  const el = () => document.getElementById("xfer-progress");
  let ok = 0;
  for (const f of files) {
    if (el()) el().textContent = `Sending ${f.name} (${bytes(f.size)})…`;
    try {
      const r = await fetch(`/api/devices/${enc(id)}/files?name=${enc(f.name)}`, {
        method: "POST", body: f, credentials: "same-origin", headers: { "X-TailConnect": "1", "Content-Type": "application/octet-stream" },
      });
      if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || r.statusText);
      ok++;
    } catch (e) {
      toast(`${f.name}: ${e.message}`, true);
    }
  }
  if (el()) el().textContent = "";
  if (ok) toast(`Sent ${ok} file${ok > 1 ? "s" : ""} to ${d ? d.name : "device"}`);
}

function sshHint(os) {
  if (os === "darwin") return "Turn on System Settings → General → Sharing → Remote Login.";
  if (os === "windows") return "Enable Settings → System → Optional features → OpenSSH Server.";
  return "Install openssh-server (e.g. sudo apt install openssh-server).";
}

function customApps(d) {
  const appsHere = S.apps.filter(a => !a.devices || !a.devices.length || a.devices.includes(d.id));
  if (!appsHere.length) return `<div class="card empty">No custom apps yet. Forward a port (e.g. Jupyter on 8888) to this computer with one click. <button class="btn sm" data-act="edit-app">Add custom app</button></div>`;
  const runMap = Object.fromEntries((d.running || []).map(r => [r.appID, r]));
  const rows = appsHere.map(a => {
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
      btns = `<button class="btn sm" data-act="ext" data-url="${h(r.url)}">${icon("ext")}Open</button>
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
  if (tn.mode === "app") {
    return `<div class="center"><div class="hero">
      <img src="/icon.png" alt="">
      <h1>Waiting for the Tailscale app</h1>
      <p>TailConnect is set to use the Tailscale app on this computer. Open Tailscale and sign in — TailConnect connects automatically.</p>
      ${tn.error ? `<div class="alert" style="text-align:left">${h(tn.error)}</div>` : ""}
      <p class="small faint" style="margin-top:22px"><a href="#/settings/tailscale">Use TailConnect's own connection instead</a></p>
    </div></div>`;
  }
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
const TABS = [["general", "General"], ["apps", "Apps"], ["custom", "Custom apps"], ["access", "Devices & access"], ["activity", "Activity"], ["tailscale", "Tailscale"], ["about", "About"]];

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
    const al = st.alerts || {};
    body = `<div class="card">
      ${field("Pair automatically with my devices", "TailConnect devices signed in to the same Tailscale account are trusted without prompts.", toggle("autoTrustOwnDevices", st.autoTrustOwnDevices))}
      ${field("Access for my devices", "What auto-paired devices may do on this computer.", accessSelect('data-set="ownDeviceAccess"', st.ownDeviceAccess || "full"))}
      ${field("Built-in SSH", S.self.builtinSSHSupported
        ? "Lets paired devices with full access open terminals and editors here — no SSH server setup needed. Only reachable over Tailscale."
        : "Not available on this platform yet; paired devices use this computer's OpenSSH server instead.",
        toggle("builtinSSH", st.builtinSSH, S.self.builtinSSHSupported ? "" : "disabled"))}
      ${field("Notifications", "Pairing requests, received files, resource alerts and updates.", toggle("notifications", st.notifications))}
      ${field("Start at login", "Launch TailConnect in the menu bar when you log in.", toggle("startAtLogin", st.startAtLogin, S.headless ? "disabled" : ""))}
      ${field("Terminal", "Used for SSH sessions and custom apps that run in a terminal.",
        `<select data-set="terminal"><option value="">Automatic</option>${terms.map(t => `<option ${t === st.terminal ? "selected" : ""}>${h(t)}</option>`).join("")}</select>`)}
      ${field("Theme", "", `<select data-set="theme">${["system", "light", "dark"].map(t => `<option value="${t}" ${t === st.theme ? "selected" : ""}>${t[0].toUpperCase() + t.slice(1)}</option>`).join("")}</select>`)}
      ${field("Add devices to ~/.ssh/config", `Lets any tool use <code>ssh tc-&lt;device&gt;</code>. Hosts live in <code>${h(S.self.sshConfig)}</code>.`, toggle("manageSSHConfig", st.manageSSHConfig))}
      ${field("System SSH server port", "Where this computer's own SSH server listens, if it has one.", `<input type="number" min="1" max="65535" style="width:96px" data-set-num="localSSHPort" value="${h(st.localSSHPort)}">`)}
    </div>
    <h2 class="sec">Resource alerts</h2>
    <div class="card">
      ${field("Alert me", "Watches this computer and connected devices every minute.", `<label class="switch"><input type="checkbox" data-alert="enabled" ${al.enabled ? "checked" : ""}><span></span></label>`)}
      ${field("GPU temperature", "°C (0 = off)", `<input type="number" min="0" max="120" style="width:80px" data-alert-num="gpuTempC" value="${h(al.gpuTempC ?? 85)}">`)}
      ${field("Disk usage", "% full (0 = off)", `<input type="number" min="0" max="100" style="width:80px" data-alert-num="diskPct" value="${h(al.diskPct ?? 95)}">`)}
      ${field("Memory usage", "% used (0 = off)", `<input type="number" min="0" max="100" style="width:80px" data-alert-num="memPct" value="${h(al.memPct ?? 95)}">`)}
    </div>`;
  } else if (tab === "apps") {
    body = `<div class="card">${S.builtins.map(b => field(h(b.name),
      b.available ? "Installed" : `Not found on this computer${b.id === "terminal" ? "" : " — install it, or make sure its command-line launcher is on PATH"}`,
      `<label class="switch"><input type="checkbox" data-app-toggle="${h(b.id)}" ${b.enabled ? "checked" : ""}><span></span></label>`)).join("")}</div>
      <p class="small muted">Editors open a remote window over SSH (VS Code-family editors need the Remote - SSH extension).</p>`;
  } else if (tab === "custom") {
    const devName = id => (devById(id) || { name: "removed device" }).name;
    body = `<div class="card list">${S.apps.length ? S.apps.map(a => `<div class="row"><div class="grow"><div class="title">${h(a.name)}</div>
        <div class="sub">Port ${a.port}${a.script ? " · launch script" : ""}${a.openBrowser ? " · opens browser" : ""}${a.inTerminal ? " · runs in terminal" : ""} · ${a.devices && a.devices.length ? "on " + a.devices.map(x => h(devName(x))).join(", ") : "all devices"}</div></div>
        <button class="btn sm" data-act="edit-app" data-app="${h(a.id)}">${icon("edit")}Edit</button>
        <button class="btn sm ghost icon danger" data-act="delete-app" data-app="${h(a.id)}" title="Delete" aria-label="Delete">${icon("trash")}</button></div>`).join("")
      : `<div class="empty">No custom apps.</div>`}</div>
      <p><button class="btn primary" data-act="edit-app">${icon("plus")}Add custom app</button></p>
      <p class="small muted">A custom app forwards a port from a device to <code>localhost</code> on this computer. An optional bash launch script starts the service first (Linux/macOS). Scripts and logs live in <code>~/.config/tailconnect</code> on the device.</p>`;
  } else if (tab === "access") {
    body = `<p class="muted">Devices that can reach <b>${h(S.self.name)}</b>, and what they may do.</p>
      <div class="card list">${S.trusted.length ? S.trusted.map(t => `<div class="row"><div class="grow"><div class="title">${h(t.name)}</div>
        <div class="sub">${h(t.loginName || "")} · paired ${h(ago(t.trustedAt))}${t.ports && t.ports.length ? " · ports " + h(t.ports.join(", ")) : ""}</div></div>
        ${accessSelect(`data-access-node="${h(t.nodeID)}" aria-label="Access level"`, t.access || "full")}
        <button class="btn sm" data-act="ports" data-node="${h(t.nodeID)}" title="Limit which ports it can reach">Ports…</button>
        <button class="btn sm danger" data-act="revoke" data-node="${h(t.nodeID)}">Revoke</button></div>`).join("") : `<div class="empty">No devices can access this computer.</div>`}</div>
      <div class="small muted" style="margin-top:8px">${ACCESS.map(([, l, d]) => `<b>${l}:</b> ${d}.`).join(" ")}</div>
      <h2 class="sec">This computer</h2>
      <div class="card" style="padding:14px 16px"><dl class="kv">
        <dt>Built-in SSH</dt><dd>${S.self.builtinSSH ? "On — paired devices with full access can open terminals and editors" : S.self.builtinSSHSupported ? "Off (General settings)" : "Not available on this platform"}</dd>
        <dt>System SSH</dt><dd>${S.self.ssh ? `Running on port ${S.self.sshPort}` : `Not running on port ${S.self.sshPort}`}</dd>
        <dt>Received files</dt><dd class="mono">${h(S.inboxDir || "")}</dd>
        <dt>Public key</dt><dd><span class="mono copy" data-act="copy" data-text="${h(S.self.publicKey)}">${h(S.self.publicKey.slice(0, 48))}…</span></dd>
      </dl></div>`;
  } else if (tab === "activity") {
    body = `<p class="muted">Recent connections, sessions, pairings and changes, newest first. Stored only on this computer.</p>
      <div class="card activity" id="activity">${activityHTML()}</div>`;
    if (Date.now() - actAt > 3000) setTimeout(loadActivity, 0);
  } else if (tab === "tailscale") {
    const tn = S.tailnet;
    body = `<div class="card" style="padding:14px 16px"><dl class="kv">
        <dt>Status</dt><dd>${h(tn.state)}</dd>
        ${tn.loginName ? `<dt>Account</dt><dd>${h(tn.loginName)}</dd>` : ""}
        ${tn.tailnet ? `<dt>Tailnet</dt><dd>${h(tn.tailnet)}</dd>` : ""}
        ${tn.dnsName ? `<dt>This device</dt><dd class="mono">${h(tn.dnsName)}</dd>` : ""}
        ${tn.ipv4 ? `<dt>IP</dt><dd class="mono">${h(tn.ipv4)}</dd>` : ""}
      </dl>
      <div style="margin-top:14px;display:flex;gap:8px">${tn.mode === "app"
        ? `<span class="muted small">Signed in through the Tailscale app — sign out or switch accounts there.</span>`
        : tn.state === "Running"
        ? `<button class="btn danger" data-act="logout">Sign out</button>`
        : `<button class="btn primary" data-act="login">Sign in</button>`}</div></div>
      <h2 class="sec">Connection</h2>
      <div class="card">${field("Use the Tailscale app on this computer",
        "Share the Tailscale app's connection instead of TailConnect's own (faster, and this computer appears once in your admin console). Switching changes how other devices see this one, so they'll pair again. Takes effect after restart.",
        `<select data-set="tailscaleMode"><option value="embedded" ${st.tailscaleMode !== "app" ? "selected" : ""}>Built into TailConnect</option><option value="app" ${st.tailscaleMode === "app" ? "selected" : ""}>Tailscale app</option></select>`)}</div>
      <h2 class="sec">Advanced</h2>
      <div class="card">${field("Control server", "Leave empty for Tailscale. Set a Headscale URL to self-host. Takes effect after restart.",
        `<input type="url" placeholder="https://headscale.example.com" style="width:260px" id="controlURL" value="${h(st.controlURL)}"><button class="btn" data-act="save-control">Save</button>`)}</div>
      <p class="small muted">${tn.mode === "app" ? "TailConnect is using the Tailscale app's connection." : "TailConnect runs its own Tailscale node (it appears as <code>tc-…</code> in your admin console) — you don't need the Tailscale app installed."}</p>`;
  } else if (tab === "about") {
    const u = S.update || {};
    let upd;
    if (u.devBuild) upd = "Development build — updates are checked for release builds only.";
    else if (u.installing) upd = `<span class="spinner sm"></span> Installing ${h(u.latest)}…`;
    else if (u.available) upd = `Version ${h(u.latest)} is available. ${u.canInstall ? `<button class="btn sm primary" data-act="update-install">Install &amp; restart</button>` : `<button class="btn sm primary" data-act="ext" data-url="${h(u.url)}">Download</button>`}`;
    else if (u.checking) upd = `<span class="spinner sm"></span> Checking…`;
    else if (u.error) upd = `Couldn't check: ${h(u.error)}`;
    else if (u.checkedAt) upd = `Up to date (checked ${h(ago(u.checkedAt))}).`;
    else upd = "Not checked yet.";
    body = `<div class="card" style="padding:14px 16px"><dl class="kv">
        <dt>Version</dt><dd>${h(S.version)}</dd>
        <dt>Updates</dt><dd>${upd}</dd>
        <dt>Device</dt><dd>${h(S.self.name)} · ${h(osLabel(S.self.os))} ${h(S.self.arch)}</dd>
        ${S.logDir ? `<dt>Logs</dt><dd class="mono">${h(S.logDir)}</dd>` : ""}
      </dl>
      <div style="margin-top:14px;display:flex;gap:8px;flex-wrap:wrap">
        ${u.devBuild ? "" : `<button class="btn" data-act="update-check" ${u.checking ? "disabled" : ""}>Check for updates</button>`}
        <button class="btn" data-act="report">Report a problem…</button>
        <button class="btn" data-act="open-logs">Open logs folder</button>
        <button class="btn" data-act="ext" data-url="https://github.com/imbikramsaha/TailConnect">${icon("ext")}Project page</button>
        <button class="btn danger" data-act="quit">${icon("power")}Quit TailConnect</button>
      </div></div>
      <div class="card" style="margin-top:12px">${field("Check for updates automatically", "Once a day, from GitHub Releases.", toggle("checkUpdates", st.checkUpdates))}</div>`;
  }
  return `<div class="settings">
    <nav class="tabs">${TABS.map(([k, v]) => `<a class="tab ${k === tab ? "sel" : ""}" href="#/settings/${k}">${v}</a>`).join("")}</nav>
    <section><h1 style="margin:0 0 16px;font-size:22px">${h((TABS.find(t => t[0] === tab) || ["", "Settings"])[1])}</h1>${body}</section>
  </div>`;
}

let actLines = null, actErr = null, actAt = 0;
function activityHTML() {
  if (actErr) return `<div class="empty">${h(actErr)}</div>`;
  if (!actLines) return `<div class="empty"><span class="spinner sm"></span></div>`;
  if (!actLines.length) return `<div class="empty">No activity yet.</div>`;
  return actLines.map(l => { const m = l.match(/^(\S+ \S+) (.*)$/); return `<div class="act-row"><span class="mono faint">${h(m ? m[1] : "")}</span><span>${h(m ? m[2] : l)}</span></div>`; }).join("");
}
async function loadActivity() {
  actAt = Date.now();
  try { actLines = await api.get("/api/activity"); actErr = null; } catch (e) { actErr = e.message; }
  const el = document.getElementById("activity");
  if (el) el.innerHTML = activityHTML();
}

// ---------- dialogs ----------
const root = document.getElementById("dialog-root");
let dialogSubmit = null;

function openDialog(title, sub, bodyHTML, okLabel, onSubmit, opts = {}) {
  root.innerHTML = `<div class="overlay"><form class="dialog" novalidate>
    <header><h3>${h(title)}</h3>${sub ? `<p>${sub}</p>` : ""}</header>
    <div class="body">${bodyHTML}</div>
    <footer><span class="err"></span>${opts.noCancel ? "" : `<button type="button" class="btn" data-dlg="cancel">Cancel</button>`}
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
    const list = seg.closest(".f") && seg.closest(".f").querySelector(".scope-list");
    if (list) list.classList.toggle("hidden", seg.dataset.v !== "some");
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

const TEMPLATES = [
  { name: "JupyterLab", port: 8888, openBrowser: true, script: "jupyter lab --no-browser --port=8888 --ServerApp.token='' --ServerApp.password=''" },
  { name: "Ollama API", port: 11434, openBrowser: false, script: "pgrep -x ollama >/dev/null || ollama serve" },
  { name: "Open WebUI", port: 3000, openBrowser: true, script: "docker start open-webui 2>/dev/null || docker run -d --name open-webui -p 127.0.0.1:3000:8080 -v open-webui:/app/backend/data --add-host=host.docker.internal:host-gateway ghcr.io/open-webui/open-webui:main" },
  { name: "ComfyUI", port: 8188, openBrowser: true, script: "cd ~/ComfyUI && python main.py --listen 127.0.0.1 --port 8188" },
  { name: "Stable Diffusion WebUI", port: 7860, openBrowser: true, script: "cd ~/stable-diffusion-webui && ./webui.sh" },
  { name: "code-server (VS Code in browser)", port: 8080, openBrowser: true, script: "code-server --bind-addr 127.0.0.1:8080 --auth none" },
  { name: "TensorBoard", port: 6006, openBrowser: true, script: "tensorboard --logdir ~/runs --port 6006" },
  { name: "Streamlit app", port: 8501, openBrowser: true, script: "streamlit run app.py --server.port 8501 --server.headless true" },
  { name: "vLLM server", port: 8000, openBrowser: false, path: "docs", script: "vllm serve Qwen/Qwen2.5-7B-Instruct --port 8000" },
  { name: "Gradio app", port: 7860, openBrowser: true, script: "python app.py" },
];

function appDialog(appID, preset) {
  const a = (S.apps.find(x => x.id === appID)) || Object.assign({ name: "", port: "", script: "", openBrowser: true, path: "", inTerminal: false, devices: [] }, preset || {});
  const mine = S.devices.filter(d => d.section === "mine");
  const scoped = new Set(a.devices || []);
  openDialog(appID ? "Edit custom app" : "New custom app", "Forward a port from a device to localhost on this computer — optionally starting the service first.", `
    ${appID ? "" : `<label class="f"><span>Start from a template</span><select data-template><option value="">Blank</option>${TEMPLATES.map((t, i) => `<option value="${i}">${h(t.name)} (port ${t.port})</option>`).join("")}</select></label>`}
    <div class="grid2">
      <label class="f"><span>Name</span><input type="text" name="name" required placeholder="JupyterLab" value="${h(a.name)}"></label>
      <label class="f"><span>Port</span><input type="number" name="port" required min="1" max="65535" placeholder="8888" value="${h(a.port)}"></label>
    </div>
    <label class="f"><span>Launch script (bash, optional)</span><textarea name="script" rows="4" spellcheck="false" placeholder="jupyter lab --no-browser --port=8888">${h(a.script || "")}</textarea>
      <small>Runs from your home directory on the device. Leave empty to only forward a port that's already in use.</small></label>
    <label class="check"><input type="checkbox" name="openBrowser" ${a.openBrowser ? "checked" : ""}> Open in browser at <span class="mono">http://localhost:PORT/</span><input type="text" name="path" placeholder="path" style="height:28px;width:120px" value="${h(a.path || "")}"></label>
    <label class="check"><input type="checkbox" name="inTerminal" ${a.inTerminal ? "checked" : ""}> Run the script in a terminal window (stays open if it fails)</label>
    ${mine.length ? `<div class="f"><span>Show on</span><div class="seg" data-name="scope" data-value="${scoped.size ? "some" : "all"}">
      <button type="button" data-v="all" class="${scoped.size ? "" : "sel"}">All devices</button><button type="button" data-v="some" class="${scoped.size ? "sel" : ""}">Selected devices</button></div>
      <div class="scope-list ${scoped.size ? "" : "hidden"}">${mine.map(d => `<label class="check"><input type="checkbox" name="dev" value="${h(d.id)}" ${scoped.has(d.id) ? "checked" : ""}> ${h(d.name)}</label>`).join("")}</div></div>` : ""}`,
    appID ? "Save" : "Create", async form => {
      const f = new FormData(form);
      const seg = form.querySelector('.seg[data-name="scope"]');
      const devices = seg && seg.dataset.value === "some" ? f.getAll("dev") : [];
      await api.post("/api/apps", {
        id: appID || "", name: f.get("name"), port: parseInt(f.get("port"), 10) || 0, script: f.get("script"),
        openBrowser: f.get("openBrowser") === "on", path: f.get("path"), inTerminal: f.get("inTerminal") === "on", devices,
      });
      toast(appID ? "Saved" : "Custom app created");
    });
}
root.addEventListener("change", e => {
  const t = e.target.closest("[data-template]");
  if (!t || t.value === "") return;
  const tpl = TEMPLATES[+t.value];
  const form = t.closest("form");
  form.elements.name.value = tpl.name;
  form.elements.port.value = tpl.port;
  form.elements.script.value = tpl.script;
  form.elements.openBrowser.checked = !!tpl.openBrowser;
  form.elements.path.value = tpl.path || "";
});

function diagnoseDialog(d, res) {
  const lat = res.latencyMS.length ? (res.latencyMS.reduce((a, b) => a + b, 0) / res.latencyMS.length) : null;
  const pathText = { lan: "Direct · same local network", direct: "Direct · peer to peer", relay: "Relayed via Tailscale (" + (res.relay || "").toUpperCase() + ")", "peer-relay": "Via a peer relay", internet: "Direct over the internet (SSH)" }[res.path] || "Unknown";
  openDialog(`Connection to ${d.name}`, "", `<dl class="kv">
      <dt>Path</dt><dd>${h(pathText)}${res.endpoint ? ` <span class="mono faint">${h(res.endpoint)}</span>` : ""}</dd>
      <dt>Latency</dt><dd>${lat == null ? "No reply" : lat.toFixed(1) + " ms"}${res.latencyMS.length > 1 ? ` <span class="faint">(${res.latencyMS.map(x => x < 10 ? x.toFixed(1) : x.toFixed(0)).join(", ")})</span>` : ""}</dd>
      ${res.speedMbps ? `<dt>Throughput</dt><dd>${res.speedMbps.toFixed(0)} Mbit/s</dd>` : res.speedErr ? `<dt>Throughput</dt><dd class="muted">${h(res.speedErr)}</dd>` : ""}
    </dl>${res.advice.map(a => `<div class="alert info">${h(a)}</div>`).join("")}`, "Done", async () => {}, { noCancel: true });
}

function portsDialog(nodeID) {
  const t = S.trusted.find(x => x.nodeID === nodeID);
  openDialog(`Ports ${t.name} can reach`, "Limit forwarded ports and screen sharing to a list. Leave empty to allow any port.", `
    <label class="f"><span>Allowed ports</span><input type="text" name="ports" placeholder="e.g. 8888, 11434, 5900" value="${h((t.ports || []).join(", "))}"></label>`,
    "Save", async form => {
      const raw = new FormData(form).get("ports").trim();
      const ports = raw ? raw.split(/[\s,]+/).map(x => parseInt(x, 10)) : [];
      if (ports.some(p => !(p > 0 && p < 65536))) throw new Error("Ports must be numbers between 1 and 65535");
      await api.put(`/api/trusted/${enc(nodeID)}`, { access: t.access || "full", ports });
      toast("Saved");
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
  "pair-decide": el => {
    const sel = document.querySelector(`[data-pair-access="${CSS.escape(el.dataset.id)}"]`);
    const access = sel ? sel.value : "full";
    run("decide:" + el.dataset.id, () => api.post(`/api/pair-requests/${enc(el.dataset.id)}/${el.dataset.v}`, { access }),
      el.dataset.v === "approve" ? `Device approved (${accessLabel(access).toLowerCase()})` : "Request declined");
  },
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
  ext: el => openExternal(el.dataset.url),
  wake: el => run("wake:" + el.dataset.id, async () => {
    const r = await api.post(`/api/devices/${enc(el.dataset.id)}/wake`);
    toast(`Wake signal sent via ${r.via}. It can take a minute to come online.`);
  }),
  "remote-desktop": el => run("rd:" + el.dataset.id, async () => {
    const r = await api.post(`/api/devices/${enc(el.dataset.id)}/remote-desktop`);
    toast(`Opening ${r.kind}…`);
  }),
  diagnose: el => {
    const d = devById(el.dataset.id);
    openDialog(`Connection to ${d.name}`, "", `<div class="empty"><span class="spinner sm"></span> Testing latency, path and speed…</div>`, "Close", async () => {}, { noCancel: true });
    api.get(`/api/devices/${enc(d.id)}/diagnose`).then(res => { if (root.innerHTML) diagnoseDialog(d, res); })
      .catch(e => { if (root.innerHTML) { closeDialog(); toast(e.message, true); } });
  },
  forward: el => run(`fwd:${el.dataset.id}:${el.dataset.port}`, async () => {
    await api.post(`/api/devices/${enc(el.dataset.id)}/forward`, { port: +el.dataset.port, label: el.dataset.label, open: el.dataset.open === "1" });
  }),
  "save-service": el => appDialog("", { name: el.dataset.label, port: +el.dataset.port, openBrowser: el.dataset.web === "1", devices: ex.id ? [ex.id] : [] }),
  kill: el => {
    const d = devById(el.dataset.id);
    openDialog(`Stop ${el.dataset.name} (PID ${el.dataset.pid})?`, `The process on ${d.name} is asked to quit.`, "", "Stop process", async () => {
      await api.post(`/api/devices/${enc(d.id)}/processes/${el.dataset.pid}/kill`);
      toast("Process stopped");
      setTimeout(() => loadExtra("procs"), 500);
    }, { danger: true });
  },
  docker: el => run(`docker:${el.dataset.cid}`, async () => {
    await api.post(`/api/devices/${enc(el.dataset.id)}/containers/${enc(el.dataset.cid)}/${el.dataset.v}`);
    loadExtra("docker");
  }),
  "ex-refresh": el => loadExtra(el.dataset.what),
  "toggle-procs": () => { ex.open = !ex.open; rerenderExtra("procs"); if (ex.open) loadExtra("procs"); },
  "clip-push": el => run("clip:" + el.dataset.id, () => api.post(`/api/devices/${enc(el.dataset.id)}/clipboard/push`), "Clipboard sent"),
  "clip-pull": el => run("clip:" + el.dataset.id, async () => {
    const r = await api.post(`/api/devices/${enc(el.dataset.id)}/clipboard/pull`);
    toast(`Copied ${r.chars} characters to your clipboard`);
  }),
  ports: el => portsDialog(el.dataset.node),
  report: () => run("report", async () => {
    const r = await api.post("/api/report");
    toast(`Saved ${r.path.split(/[\\/]/).pop()} to Downloads — attach it to a GitHub issue. Nothing was uploaded.`);
  }),
  restart: () => run("restart", async () => {
    await api.post("/api/restart");
    app.innerHTML = `<div class="center"><div class="hero"><div class="spinner"></div><p>Restarting TailConnect…</p></div></div>`;
    setTimeout(() => window.close(), 1500);
  }),
  "update-check": () => run("update", () => api.post("/api/update/check")),
  "update-install": () => run("update", () => api.post("/api/update/install")),
};

// External links: the native window can't open new tabs, so hand them to
// the system browser through the window's bridge.
function openExternal(url) {
  if (!url) return;
  if (typeof window.tcOpenExternal === "function") window.tcOpenExternal(url);
  else window.open(url, "_blank", "noopener");
}
document.addEventListener("click", e => {
  const a = e.target.closest('a[target="_blank"]');
  if (a && typeof window.tcOpenExternal === "function") {
    e.preventDefault();
    window.tcOpenExternal(a.href);
  }
}, true);

// Drag & drop files onto a device's transfer card.
document.addEventListener("dragover", e => {
  const z = e.target.closest("[data-drop]");
  if (!z) return;
  e.preventDefault();
  z.classList.add("drop-hover");
});
document.addEventListener("dragleave", e => {
  const z = e.target.closest("[data-drop]");
  if (z && !z.contains(e.relatedTarget)) z.classList.remove("drop-hover");
});
document.addEventListener("drop", e => {
  const z = e.target.closest("[data-drop]");
  if (!z) return;
  e.preventDefault();
  z.classList.remove("drop-hover");
  if (e.dataTransfer.files.length) sendFiles(z.dataset.drop, [...e.dataTransfer.files]);
});

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
  if (el.dataset.sendFiles) {
    const files = [...el.files];
    el.value = "";
    if (files.length) sendFiles(el.dataset.sendFiles, files);
    return;
  }
  if (el.dataset.pairAccess) return; // read when approving
  if (el.dataset.accessNode) {
    const t = S.trusted.find(x => x.nodeID === el.dataset.accessNode);
    try {
      await api.put(`/api/trusted/${enc(el.dataset.accessNode)}`, { access: el.value, ports: (t && t.ports) || [] });
      toast(`${t ? t.name : "Device"} now has ${accessLabel(el.value).toLowerCase()}`);
    } catch (ex) { toast(ex.message, true); }
    el.blur();
    await refresh();
    return;
  }
  let patch = null;
  if (el.dataset.set) patch = { [el.dataset.set]: el.type === "checkbox" ? el.checked : el.value };
  else if (el.dataset.setNum) patch = { [el.dataset.setNum]: parseInt(el.value, 10) || 0 };
  else if (el.dataset.alert) patch = { alerts: Object.assign({}, S.settings.alerts, { enabled: el.checked }) };
  else if (el.dataset.alertNum) patch = { alerts: Object.assign({}, S.settings.alerts, { [el.dataset.alertNum]: parseFloat(el.value) || 0 }) };
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
