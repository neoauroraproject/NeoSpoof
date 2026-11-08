"use strict";

/* ===== constants ===== */
const VERDICT = {
  passed: "اسپوف باز است",
  rewritten: "سورس عوض می‌شود",
  blocked: "اسپوف بسته است",
  none: "بسته‌ای نرسید",
  listening: "در حال گوش دادن",
  sending: "در حال ارسال",
  sent: "ارسال شد",
  report: "نتیجه لیست",
  error: "اجرا نشد",
  "": "آماده"
};
const ROW = { passed: "باز", rewritten: "عوض شد", missing: "نرسید", pending: "منتظر" };
const FA = "۰۱۲۳۴۵۶۷۸۹";

const state = { view: "dash", simpleRole: "sender", listRole: "sender", timer: 0, last: null, rawReady: true };
const result = {
  simple: { box: null, built: false, role: "", rowEls: {}, refs: null },
  list: { box: null, built: false, role: "", rowEls: {}, refs: null }
};

/* ===== helpers ===== */
const $ = (id) => document.getElementById(id);
const fa = (n) => String(n).replace(/[0-9]/g, (d) => FA[+d]);

function store(key, val) {
  try {
    if (val === null) localStorage.removeItem(key);
    else localStorage.setItem(key, val);
  } catch (e) { /* storage unavailable */ }
}
function load(key) {
  try { return localStorage.getItem(key); } catch (e) { return null; }
}

function api(path, opts) {
  opts = opts || {};
  return fetch(path, {
    method: opts.method || "GET",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", "X-Requested-With": "spoofd" },
    body: opts.body ? JSON.stringify(opts.body) : undefined
  }).then(async (res) => {
    const data = await res.json().catch(() => ({}));
    if (res.status === 401) { showLogin(); throw new Error(data.error || "وارد شوید"); }
    if (res.status === 403) { showLicense(null); throw new Error(data.error || "لایسنس فعال نیست"); }
    if (!res.ok) throw new Error(data.error || "درخواست ناموفق بود");
    return data;
  });
}

function setError(id, msg) {
  const el = $(id);
  el.hidden = !msg;
  el.textContent = msg || "";
}

/* ===== toasts ===== */
const ICONS = {
  ok: '<svg class="t-ic" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M20 6 9 17l-5-5"/></svg>',
  err: '<svg class="t-ic" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="9"/><path d="m15 9-6 6m0-6 6 6"/></svg>',
  info: '<svg class="t-ic" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="9"/><path d="M12 8h.01M11 12h1v4h1"/></svg>'
};
function toast(msg, kind) {
  kind = kind || "info";
  const box = $("toasts");
  const el = document.createElement("div");
  el.className = "toast " + kind;
  el.setAttribute("role", kind === "err" ? "alert" : "status");
  const ic = document.createElement("span");
  ic.innerHTML = ICONS[kind] || ICONS.info;
  const txt = document.createElement("span");
  txt.textContent = msg;
  el.append(ic.firstChild, txt);
  box.appendChild(el);
  setTimeout(() => { el.style.opacity = "0"; setTimeout(() => el.remove(), 250); }, 3600);
}

/* ===== screens ===== */
function showLogin() {
  $("app").hidden = true;
  $("license-screen").hidden = true;
  $("login-screen").hidden = false;
  setError("pw-error", "");
  setError("login-error", "");
  stopPoll();
  stopDash();
  stopTunnels();
}
function showApp() {
  $("login-screen").hidden = true;
  $("license-screen").hidden = true;
  $("app").hidden = false;
}
function showLicense(lic) {
  $("login-screen").hidden = true;
  $("app").hidden = true;
  $("license-screen").hidden = false;
  stopPoll();
  stopDash();
  stopTunnels();
  const st = $("license-status");
  if (lic && lic.reason) { st.hidden = false; st.textContent = lic.reason; }
  else { st.hidden = true; }
  // During the trial the operator can keep using it without activating yet.
  $("license-later").hidden = !(lic && lic.trial);
}
// gate decides which screen to show once we know the license state.
function gate(me) {
  if (me && me.license && me.license.active) { showApp(); selectView(state.view); loadPeers(); return refresh(); }
  showLicense(me ? me.license : null);
}

/* ===== password visibility ===== */
document.querySelectorAll(".pw-toggle").forEach((btn) => {
  btn.addEventListener("click", () => {
    const input = $(btn.dataset.pw);
    const show = input.type === "password";
    input.type = show ? "text" : "password";
    btn.setAttribute("aria-pressed", String(show));
    btn.setAttribute("aria-label", show ? "پنهان کردن رمز" : "نمایش رمز");
  });
});

/* ===== theme ===== */
const SUN = '<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="4"/><path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.5 1.5M17.5 17.5 19 19M19 5l-1.5 1.5M6.5 17.5 5 19"/></svg>';
const MOON = '<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8Z"/></svg>';
function effectiveTheme() {
  const forced = document.documentElement.getAttribute("data-theme");
  if (forced) return forced;
  return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
}
function applyTheme(mode) {
  if (mode) document.documentElement.setAttribute("data-theme", mode);
  else document.documentElement.removeAttribute("data-theme");
  const eff = effectiveTheme();
  $("theme").innerHTML = eff === "dark" ? SUN : MOON;
  $("theme").setAttribute("aria-label", eff === "dark" ? "تم روشن" : "تم تاریک");
}
applyTheme(load("spoofd-theme") || "light");
$("theme").addEventListener("click", () => {
  const next = effectiveTheme() === "dark" ? "light" : "dark";
  store("spoofd-theme", next);
  applyTheme(next);
});

/* ===== login / logout ===== */
$("login-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const btn = ev.target.querySelector("button[type=submit]");
  btn.disabled = true;
  setError("login-error", "");
  try {
    await api("/api/login", { method: "POST", body: {
      username: $("username").value.trim(),
      password: $("password").value
    }});
    $("password").value = "";
    const me = await api("/api/me");
    applyMe(me);
    gate(me);
  } catch (err) {
    setError("login-error", err.message);
  } finally {
    btn.disabled = false;
  }
});

$("logout").addEventListener("click", async () => {
  await api("/api/logout", { method: "POST", body: {} }).catch(() => {});
  showLogin();
  toast("خارج شدید", "info");
});

/* ===== license activation ===== */
$("license-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const btn = ev.target.querySelector("button[type=submit]");
  btn.disabled = true;
  setError("license-error", "");
  try {
    await api("/api/license/activate", { method: "POST", body: { key: $("license-key").value.trim() } });
    toast("لایسنس فعال شد", "ok");
    const me = await api("/api/me");
    applyMe(me);
    gate(me);
  } catch (err) {
    setError("license-error", err.message);
  } finally {
    btn.disabled = false;
  }
});
$("license-logout").addEventListener("click", async () => {
  await api("/api/logout", { method: "POST", body: {} }).catch(() => {});
  showLogin();
});
$("license-later").addEventListener("click", () => { showApp(); refresh(); });
$("trial-pill").addEventListener("click", () => showLicense(lastLicense));

/* ===== tabs ===== */
const tabs = Array.from(document.querySelectorAll('.nav [role="tab"]'));
function selectView(view) {
  state.view = view;
  tabs.forEach((t) => {
    const on = t.dataset.view === view;
    t.setAttribute("aria-selected", String(on));
    t.tabIndex = on ? 0 : -1;
  });
  $("view-dash").hidden = view !== "dash";
  $("view-test").hidden = view !== "test";
  $("view-tunnels").hidden = view !== "tunnels";
  $("view-settings").hidden = view !== "settings";
  if (view === "tunnels") startTunnels();
  else stopTunnels();
  if (view === "dash") startDash();
  else stopDash();
}
tabs.forEach((btn, i) => {
  btn.addEventListener("click", () => selectView(btn.dataset.view));
  btn.addEventListener("keydown", (e) => {
    let j = -1;
    if (e.key === "ArrowLeft") j = (i + 1) % tabs.length;        // RTL: left = next
    else if (e.key === "ArrowRight") j = (i - 1 + tabs.length) % tabs.length;
    else if (e.key === "Home") j = 0;
    else if (e.key === "End") j = tabs.length - 1;
    if (j >= 0) { e.preventDefault(); tabs[j].focus(); selectView(tabs[j].dataset.view); }
  });
});

/* ===== roles ===== */
// Segmented switch inside the merged "تست اسپوف" tab (simple vs list).
document.querySelectorAll(".subswitch [data-sub]").forEach((b) => {
  b.addEventListener("click", () => {
    const sub = b.dataset.sub;
    document.querySelectorAll(".subswitch [data-sub]").forEach((x) => x.setAttribute("aria-selected", String(x === b)));
    $("view-simple").hidden = sub !== "simple";
    $("view-list").hidden = sub !== "list";
  });
});

function bindRoles(group, key) {
  document.querySelectorAll('[data-group="' + group + '"]').forEach((btn) => {
    btn.addEventListener("click", () => {
      state[key] = btn.dataset.role;
      document.querySelectorAll('[data-group="' + group + '"]').forEach((b) => b.setAttribute("aria-pressed", String(b === btn)));
      applyRole(group);
    });
  });
}
bindRoles("simple", "simpleRole");
bindRoles("list", "listRole");

function applyRole(group) {
  const sender = state[group + "Role"] === "sender";
  $(group + "-dst-label").hidden = !sender;
  const stop = $(group + "-stop");
  stop.textContent = sender ? "توقف" : "پایان و جمع‌بندی";
}

/* ===== list ip counter (matches the server: comments + dedup aware) ===== */
function countIPs(text) {
  const seen = new Set();
  text.split("\n").forEach((line) => {
    line = line.trim();
    if (!line || line.startsWith("#")) return;
    line.split(/[\s,;]+/).forEach((tok) => { if (tok) seen.add(tok); });
  });
  return seen.size;
}
$("list-ips").addEventListener("input", () => {
  const n = countIPs($("list-ips").value);
  const el = $("list-count");
  el.textContent = fa(n) + " آی‌پی";
  el.style.color = n > 1000 ? "var(--bad)" : "var(--muted)";
});

/* ===== start / stop ===== */
async function begin(mode) {
  const errId = mode + "-error";
  setError(errId, "");
  const role = state[mode + "Role"];
  if (role === "sender" && !$(mode + "-dst").value.trim()) {
    setError(errId, "آی‌پی سرور مقصد را وارد کنید");
    return;
  }
  if (mode === "list" && role === "sender" && countIPs($("list-ips").value) === 0) {
    setError(errId, "حداقل یک آی‌پی در لیست لازم است");
    return;
  }
  const body = { mode: mode, role: role, dst: "", ips: "" };
  if (role === "sender") body.dst = $(mode + "-dst").value.trim();
  if (mode === "list") body.ips = $("list-ips").value;
  const btn = $(mode + "-start");
  btn.disabled = true;
  try {
    state.last = await api("/api/test/start", { method: "POST", body: body });
    render(state.last);
    startPoll();
    toast(role === "sender" ? "ارسال شروع شد" : "گوش دادن شروع شد", "info");
  } catch (err) {
    setError(errId, err.message);
  } finally {
    btn.disabled = false;
  }
}
$("simple-start").addEventListener("click", () => begin("simple"));
$("list-start").addEventListener("click", () => begin("list"));

async function halt(mode) {
  const btn = $(mode + "-stop");
  btn.disabled = true;
  try {
    state.last = await api("/api/test/stop", { method: "POST", body: {} });
    render(state.last);
  } catch (err) {
    setError(mode + "-error", err.message);
  } finally {
    btn.disabled = false;
  }
}
$("simple-stop").addEventListener("click", () => halt("simple"));
$("list-stop").addEventListener("click", () => halt("list"));

/* ===== clipboard with fallback (plain http has no navigator.clipboard) ===== */
async function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    try { await navigator.clipboard.writeText(text); return true; } catch (e) { /* fall through */ }
  }
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.focus();
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    return ok;
  } catch (e) { return false; }
}

$("copy-open").addEventListener("click", async () => {
  const ips = ((state.last && state.last.rows) || []).filter((r) => r.status === "passed").map((r) => r.ip).join("\n");
  const ok = await copyText(ips);
  if (ok) {
    const btn = $("copy-open");
    btn.textContent = "کپی شد";
    setTimeout(() => { btn.textContent = "کپی آی‌پی‌های باز"; }, 1500);
  } else {
    toast("کپی ممکن نشد. دستی انتخاب کنید", "err");
  }
});

$("export-csv").addEventListener("click", () => {
  const rows = (state.last && state.last.rows) || [];
  if (!rows.length) return;
  const lines = ["ip,status,wire,count"];
  rows.forEach((r) => lines.push([r.ip, r.status, r.wire || "", r.count].join(",")));
  const blob = new Blob([lines.join("\n")], { type: "text/csv;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = "spoof-result.csv";
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
});

/* ===== suggested IPs (automatic candidate list) ===== */
const SUGGESTED = ["8.8.8.8", "8.8.4.4", "1.1.1.1", "1.0.0.1", "9.9.9.9", "149.112.112.112", "208.67.222.222", "208.67.220.220", "94.140.14.14", "94.140.15.15", "4.2.2.4", "64.6.64.6"];
$("suggest-ips").addEventListener("click", () => {
  $("list-ips").value = SUGGESTED.join("\n");
  $("list-ips").dispatchEvent(new Event("input"));
  toast("آی‌پی‌های پیشنهادی گذاشته شد", "info");
});
// Iran "white" source list — ranges and CIDR are expanded by the test (capped
// at 1000). Served as a static asset so the big list stays out of the bundle.
$("suggest-white").addEventListener("click", async () => {
  const btn = $("suggest-white"); btn.disabled = true;
  try {
    const res = await fetch("iran-white.txt", { credentials: "same-origin" });
    if (!res.ok) throw new Error("not ok");
    $("list-ips").value = await res.text();
    $("list-ips").dispatchEvent(new Event("input"));
    toast("لیست وایت ایران گذاشته شد — رنج/CIDR پشتیبانی می‌شود (سقف ۱۰۰۰)", "info");
  } catch (e) { toast("بارگیری لیست وایت ناموفق بود", "err"); }
  finally { btn.disabled = false; }
});

/* ===== build tunnel from passing IPs ===== */
function downloadText(text, name) {
  const blob = new Blob([text], { type: "text/plain;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
// Carry the passing IPs into the Tunnels tab, where one click builds a managed
// tunnel (client runs here) and hands back a single server command — no loose
// client.toml/server.toml files.
$("build-tunnel").addEventListener("click", () => {
  const ips = ((state.last && state.last.rows) || []).filter((r) => r.status === "passed").map((r) => r.ip);
  if (!ips.length) { toast("آی‌پی بازی برای تانل نیست", "err"); return; }
  $("tn-ips").value = ips.join("\n");
  selectView("tunnels");
  toast("آی‌پی‌های باز منتقل شد — نام و آی‌پی مقصد را بزن و «افزودن تانل»", "info");
  $("tn-name").focus();
});

/* ===== settings: probe params ===== */
$("probe-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const btn = ev.target.querySelector("button[type=submit]");
  btn.disabled = true;
  setError("probe-error", "");
  try {
    const cur = await api("/api/settings");
    const saved = await api("/api/settings", { method: "POST", body: Object.assign({}, cur, {
      listen_seconds: parseInt($("set-listen").value, 10) || 0,
      packets_per_sec: parseInt($("set-pps").value, 10) || 0,
      tunnel_count: parseInt($("set-tcount").value, 10) || 0,
      mtu: parseInt($("set-mtu").value, 10) || 0,
      initial_cwnd: parseFloat($("set-cwnd").value) || 0
    })});
    $("set-listen").value = saved.listen_seconds;
    $("set-pps").value = saved.packets_per_sec;
    $("set-tcount").value = saved.tunnel_count;
    $("set-mtu").value = saved.mtu;
    $("set-cwnd").value = saved.initial_cwnd;
    toast("تنظیمات ذخیره شد", "ok");
  } catch (err) {
    setError("probe-error", err.message);
  } finally {
    btn.disabled = false;
  }
});

/* ===== settings: password ===== */
$("password-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const btn = ev.target.querySelector("button[type=submit]");
  btn.disabled = true;
  setError("pw-error", "");
  try {
    await api("/api/password", { method: "POST", body: {
      current: $("pw-current").value,
      next: $("pw-next").value
    }});
    ev.target.reset();
    toast("رمز عوض شد", "ok");
  } catch (err) {
    setError("pw-error", err.message);
  } finally {
    btn.disabled = false;
  }
});

/* ===== me / capabilities ===== */
let lastLicense = null;
// applyMe renders everything derived from /api/me (no fetch of its own).
function applyMe(me) {
  state.rawReady = !!me.raw_ready;
  if (me.version) { $("ver").textContent = "v" + me.version; $("ver").hidden = false; }
  $("set-listen").value = me.listen_seconds;
  $("set-pps").value = me.packets_per_sec;
  if ("tunnel_count" in me) $("set-tcount").value = me.tunnel_count;
  if ("mtu" in me) $("set-mtu").value = me.mtu;
  if ("initial_cwnd" in me) $("set-cwnd").value = me.initial_cwnd;
  lastLicense = me.license || null;
  const tp = $("trial-pill");
  if (lastLicense && lastLicense.trial) {
    const hrs = Math.max(0, Math.round((lastLicense.trial_expires - Date.now() / 1000) / 3600));
    tp.hidden = false;
    tp.textContent = "آزمایشی · " + fa(hrs) + " ساعت";
  } else {
    tp.hidden = true;
  }
  renderServerInfo(me);
  renderCapability(me);
  renderSite(me);
}

/* ===== server identity (this panel: ایران/مبدأ vs خارج/اکسیت) ===== */
const ROLE_FA = { source: "ایران (مبدأ)", exit: "خارج (اکسیت)" };
function renderSite(me) {
  const role = me.site_role || "";
  const name = me.site_name || "";
  const label = (ROLE_FA[role] || "") + (role && name ? " · " : "") + (name || "");
  const pill = $("site-pill");
  if (role || name) { pill.hidden = false; pill.textContent = label; }
  else { pill.hidden = true; }
  const rs = $("site-role"); if (rs) rs.value = role;
  const nm = $("site-name"); if (nm) nm.value = name;
  const badge = $("site-badge");
  if (badge) {
    if (role || name) {
      badge.hidden = false;
      badge.textContent = label;
      badge.className = "badge " + (role === "source" ? "v-passed" : "v-listening");
    } else { badge.hidden = true; }
  }
}
$("site-save").addEventListener("click", async () => {
  const btn = $("site-save"); btn.disabled = true;
  try {
    const cur = await api("/api/settings");
    const saved = await api("/api/settings", { method: "POST", body: {
      listen_seconds: cur.listen_seconds,
      packets_per_sec: cur.packets_per_sec,
      site_role: $("site-role").value,
      site_name: $("site-name").value.trim()
    }});
    renderSite({ site_role: saved.site_role, site_name: saved.site_name });
    toast("هویت سرور ذخیره شد", "ok");
  } catch (e) { toast(e.message, "err"); }
  finally { btn.disabled = false; }
});
function renderCapability(me) {
  const banner = $("cap-banner");
  if (me.raw_ready) { banner.hidden = true; }
  else {
    banner.hidden = false;
    $("cap-text").textContent = "این سیستم (" + (me.platform || "?") +
      ") بدون دسترسی root نمی‌تواند بسته خام بفرستد یا بگیرد. برای تست واقعی، پنل را روی سرور لینوکسی با sudo اجرا کنید.";
  }
  $("simple-start").disabled = !me.raw_ready;
  $("list-start").disabled = !me.raw_ready;
  const t = me.raw_ready ? "" : "روی این سیستم در دسترس نیست";
  $("simple-start").title = t;
  $("list-start").title = t;
}
function renderServerInfo(me) {
  const dl = $("server-info");
  dl.replaceChildren();
  const add = (k, v) => {
    const dt = document.createElement("dt"); dt.textContent = k;
    const dd = document.createElement("dd"); dd.textContent = v; dd.className = "ltr";
    dl.append(dt, dd);
  };
  add("نسخه", me.version ? "v" + me.version : "—");
  add("سیستم‌عامل", me.platform || "—");
  const dt = document.createElement("dt"); dt.textContent = "آماده برای بسته خام";
  const dd = document.createElement("dd"); dd.textContent = me.raw_ready ? "بله" : "خیر";
  dd.style.color = me.raw_ready ? "var(--ok)" : "var(--bad)";
  dl.append(dt, dd);
  add("مدت گوش دادن", fa(me.listen_seconds) + " ثانیه");
  add("نرخ ارسال", fa(me.packets_per_sec) + " بسته/ثانیه");
  const lic = me.license || {};
  const ldt = document.createElement("dt"); ldt.textContent = "لایسنس";
  const ldd = document.createElement("dd");
  ldd.textContent = lic.activated ? (lic.active ? "فعال" : "منقضی") : (lic.trial ? "نسخه آزمایشی" : "غیرفعال");
  ldd.style.color = (lic.activated && lic.active) ? "var(--ok)" : (lic.trial ? "var(--warn)" : "var(--bad)");
  dl.append(ldt, ldd);
  if (lic.plan) add("پلن", lic.plan);
  if (lic.bound_ip) add("قفل‌شده روی", lic.bound_ip);
  if (lic.expires_at) add("اعتبار تا", new Date(lic.expires_at * 1000).toLocaleString("fa-IR"));
  if (lic.trial && lic.trial_expires) add("پایان آزمایشی", new Date(lic.trial_expires * 1000).toLocaleString("fa-IR"));
}

/* ===== polling ===== */
function setLive(on, text) {
  const el = $("live");
  el.classList.toggle("on", on);
  $("live-text").textContent = text;
}
function startPoll() {
  if (state.timer) return;
  state.timer = setInterval(refresh, 1000);
  setLive(true, "در حال دریافت");
}
function stopPoll() {
  clearInterval(state.timer);
  state.timer = 0;
  setLive(false, "آماده");
}
async function refresh() {
  if (document.hidden) return; // don't poll a hidden tab
  try {
    state.last = await api("/api/test");
    render(state.last);
    if (state.last.phase !== "running") stopPoll();
  } catch (err) {
    stopPoll();
  }
}
document.addEventListener("visibilitychange", () => {
  if (!document.hidden && state.last && state.last.phase === "running") { refresh(); startPoll(); }
});

/* ===== rendering ===== */
function render(s) {
  if (!s) return;
  draw("simple", s);
  draw("list", s);
}

function buildSkeleton(view, mode) {
  const box = view.box;
  box.replaceChildren();
  const head = document.createElement("div");
  head.className = "verdict";
  const count = document.createElement("div"); count.className = "count";
  const cap = document.createElement("p"); cap.className = "muted cap";
  const badge = document.createElement("div"); badge.className = "badge";
  const detail = document.createElement("p"); detail.className = "detail muted";
  head.append(count, cap, badge, detail);

  const bar = document.createElement("div"); bar.className = "bar"; bar.hidden = true;
  bar.setAttribute("role", "progressbar");
  bar.setAttribute("aria-valuemin", "0");
  const barSpan = document.createElement("span"); bar.appendChild(barSpan);

  const chips = document.createElement("div"); chips.className = "chips"; chips.hidden = true;

  const tools = document.createElement("div"); tools.className = "result-tools"; tools.hidden = true;
  const filter = document.createElement("input");
  filter.className = "ltr search"; filter.type = "text";
  filter.placeholder = "جستجوی آی‌پی"; filter.setAttribute("aria-label", "جستجو در نتیجه");
  filter.addEventListener("input", () => applyFilter(view));
  tools.appendChild(filter);

  const wrap = document.createElement("div"); wrap.className = "table-wrap"; wrap.hidden = true;
  const table = document.createElement("table");
  const thead = document.createElement("thead");
  const hr = document.createElement("tr");
  ["آی‌پی", "وضعیت", "سورس رسیده", "تعداد"].forEach((name) => {
    const th = document.createElement("th"); th.textContent = name; th.scope = "col"; hr.appendChild(th);
  });
  thead.appendChild(hr);
  const tbody = document.createElement("tbody");
  table.append(thead, tbody);
  wrap.appendChild(table);

  box.append(head, bar, chips, tools, wrap);
  view.refs = { count, cap, badge, detail, bar, barSpan, chips, tools, filter, wrap, tbody };
  view.rowEls = {};
}

function draw(mode, s) {
  const view = result[mode];
  if (!view.box) view.box = $(mode + "-result");
  const mine = s.mode === mode && s.phase !== "idle";
  const stop = $(mode + "-stop");
  stop.hidden = !(mine && s.phase === "running");

  if (!mine) {
    view.box.replaceChildren();
    view.built = false;
    const p = document.createElement("p");
    p.className = "muted";
    p.textContent = s.phase === "running" ? "یک تست دیگر در حال اجراست." : "هنوز تستی در این بخش اجرا نشده است.";
    view.box.appendChild(p);
    if (mode === "list") {
      $("copy-open").hidden = true;
      $("export-csv").hidden = true;
      $("build-tunnel").hidden = true;
    }
    return;
  }

  if (!view.built || view.role !== s.role) {
    buildSkeleton(view, mode);
    view.built = true;
    view.role = s.role;
  }
  const r = view.refs;
  const sender = s.role === "sender";

  r.count.textContent = sender ? (fa(s.sent) + " / " + fa(s.total)) : fa(s.packet_count);
  r.cap.textContent = sender ? "بسته ارسال‌شده" : "بسته رسیده‌شده";
  r.badge.className = "badge v-" + (s.verdict || "");
  r.badge.textContent = VERDICT[s.verdict] || VERDICT[""];
  r.detail.textContent = s.detail || s.error || "";

  // progress bar (sender)
  if (sender && s.total > 0) {
    r.bar.hidden = false;
    r.bar.setAttribute("aria-valuemax", String(s.total));
    r.bar.setAttribute("aria-valuenow", String(s.sent));
    r.barSpan.style.width = Math.min(100, Math.round((s.sent / s.total) * 100)) + "%";
  } else {
    r.bar.hidden = true;
  }

  // chips + tools (list receiver)
  const listReceiver = mode === "list" && !sender;
  if (listReceiver) {
    r.chips.hidden = false;
    r.chips.replaceChildren();
    [["باز", s.passed_count], ["عوض‌شده", s.rewritten_count], ["نرسیده", s.missing_count], ["منتظر", s.pending_count]].forEach((pair) => {
      const c = document.createElement("span");
      c.className = "chip";
      const b = document.createElement("b"); b.textContent = fa(pair[1]);
      c.append(pair[0] + " ", b);
      r.chips.appendChild(c);
    });
    $("copy-open").hidden = !(s.passed_count > 0);
    $("export-csv").hidden = !((s.rows || []).length > 0);
    $("build-tunnel").hidden = !(s.passed_count > 0);
  } else {
    r.chips.hidden = true;
    if (mode === "list") {
      $("copy-open").hidden = true;
      $("export-csv").hidden = true;
      $("build-tunnel").hidden = true;
    }
  }

  // table
  const rows = s.rows || [];
  if (!rows.length) { r.wrap.hidden = true; r.tools.hidden = true; return; }
  r.wrap.hidden = false;
  r.tools.hidden = !listReceiver;

  const present = new Set();
  rows.forEach((row) => {
    present.add(row.ip);
    let tr = view.rowEls[row.ip];
    if (!tr) {
      tr = document.createElement("tr");
      const ipTd = document.createElement("td"); ipTd.className = "ltr";
      const stTd = document.createElement("td");
      const stSpan = document.createElement("span"); stSpan.className = "dot"; stTd.appendChild(stSpan);
      const wTd = document.createElement("td"); wTd.className = "ltr";
      const cTd = document.createElement("td"); cTd.className = "ltr";
      tr.append(ipTd, stTd, wTd, cTd);
      tr._cells = { ip: ipTd, st: stSpan, w: wTd, c: cTd };
      view.rowEls[row.ip] = tr;
      r.tbody.appendChild(tr);
    }
    const c = tr._cells;
    c.ip.textContent = row.ip;
    c.st.textContent = ROW[row.status] || row.status;
    c.st.className = "dot rs-" + row.status;
    c.w.textContent = row.wire || "—";
    c.c.textContent = fa(row.count);
  });
  Object.keys(view.rowEls).forEach((ip) => {
    if (!present.has(ip)) { view.rowEls[ip].remove(); delete view.rowEls[ip]; }
  });
  applyFilter(view);
}

function applyFilter(view) {
  if (!view.refs) return;
  const q = view.refs.filter.value.trim();
  Object.keys(view.rowEls).forEach((ip) => {
    view.rowEls[ip].hidden = q && ip.indexOf(q) === -1;
  });
}

/* ===== tunnels (activation) ===== */
const tn = { listTimer: 0, logsTimer: 0, logsId: 0, checks: {}, speeds: {} };
const TN_STATUS = { running: "فعال", stopped: "متوقف", error: "خطا" };

function startTunnels() {
  loadTunnels();
  if (!tn.listTimer) tn.listTimer = setInterval(loadTunnels, 3000);
}
function stopTunnels() {
  clearInterval(tn.listTimer); tn.listTimer = 0;
  clearInterval(tn.logsTimer); tn.logsTimer = 0; tn.logsId = 0;
  $("tn-logs-card").hidden = true;
  $("tn-server-card").hidden = true;
}
async function loadTunnels() {
  if (document.hidden) return;
  try {
    const d = await api("/api/tunnels");
    renderTunnels(d.tunnels || []);
    fillFwdTunnelSelect(d.tunnels || []);
    loadForwards();
  } catch (e) { /* 401/403 handled */ }
}

/* ===== port forwarding ===== */
const FWD_STATUS = { running: "فعال", stopped: "متوقف", error: "خطا" };
function fillFwdTunnelSelect(list) {
  const sel = $("fwd-tunnel"); if (!sel) return;
  const prev = sel.value;
  const clients = list.filter((t) => t.role !== "server");
  sel.replaceChildren();
  if (!clients.length) {
    const o = document.createElement("option"); o.value = ""; o.textContent = "— تانل کلاینتی نیست —"; sel.appendChild(o); return;
  }
  clients.forEach((t) => {
    const o = document.createElement("option"); o.value = String(t.id);
    o.textContent = (t.name || ("#" + t.id)) + " · SOCKS " + t.socks_port + (t.status === "running" ? "" : " (متوقف)");
    sel.appendChild(o);
  });
  if (prev) sel.value = prev;
}
async function loadForwards() {
  try { const d = await api("/api/forwards"); renderForwards(d.forwards || []); } catch (e) { /* ignore */ }
}
function renderForwards(list) {
  const box = $("fwd-list"); if (!box) return;
  box.replaceChildren();
  if (!list.length) return;
  list.forEach((f) => {
    const row = document.createElement("div"); row.className = "tn-row";
    const head = document.createElement("div"); head.className = "row spread";
    const title = document.createElement("h3"); title.textContent = f.name || ("#" + f.id);
    const badge = document.createElement("span"); badge.className = "badge st-" + f.status; badge.textContent = FWD_STATUS[f.status] || f.status;
    head.append(title, badge);
    const meta = document.createElement("div"); meta.className = "meta";
    const m = (k, v) => { const s = document.createElement("span"); const b = document.createElement("b"); b.className = "ltr"; b.textContent = v; s.append(k + " ", b); return s; };
    meta.append(m("محلی", "127.0.0.1:" + f.local_port), m("← سرویس", f.target_ip + ":" + f.target_port), m("از تانل", "#" + f.tunnel_id));
    if (f.error) { const e = document.createElement("span"); e.style.color = "var(--bad)"; e.textContent = f.error; meta.append(e); }
    const acts = document.createElement("div"); acts.className = "actions";
    const toggle = document.createElement("button");
    if (f.status === "running") { toggle.className = "ghost"; toggle.textContent = "توقف"; toggle.onclick = () => fwdAction("stop", f.id); }
    else { toggle.className = "cta"; toggle.textContent = "فعال‌سازی"; toggle.onclick = () => fwdAction("start", f.id); }
    const del = document.createElement("button"); del.className = "ghost"; del.textContent = "حذف";
    del.onclick = () => { if (confirm("این فوروارد حذف شود؟")) fwdAction("delete", f.id); };
    acts.append(toggle, del);
    row.append(head, meta, acts);
    box.appendChild(row);
  });
}
async function fwdAction(path, id) {
  try { const d = await api("/api/forwards/" + path, { method: "POST", body: { id: id } }); renderForwards(d.forwards || []); }
  catch (e) { toast(e.message, "err"); }
}
$("fwd-add").addEventListener("click", async () => {
  setError("fwd-error", "");
  const btn = $("fwd-add"); btn.disabled = true;
  try {
    const d = await api("/api/forwards", { method: "POST", body: {
      name: $("fwd-name").value.trim(),
      local_port: parseInt($("fwd-local").value, 10) || 0,
      target_ip: $("fwd-tip").value.trim() || "127.0.0.1",
      target_port: parseInt($("fwd-tport").value, 10) || 0,
      tunnel_id: parseInt($("fwd-tunnel").value, 10) || 0
    }});
    $("fwd-name").value = ""; $("fwd-local").value = ""; $("fwd-tip").value = ""; $("fwd-tport").value = "";
    renderForwards(d.forwards || []);
    toast("فوروارد اضافه و فعال شد", "ok");
  } catch (e) { setError("fwd-error", e.message); }
  finally { btn.disabled = false; }
});
function renderTunnels(list) {
  const box = $("tn-list");
  box.replaceChildren();
  if (!list.length) {
    const p = document.createElement("p"); p.className = "muted";
    p.textContent = "هنوز تانلی ساخته نشده. از بالا یک لوکیشن اضافه کن.";
    box.appendChild(p);
    return;
  }
  list.forEach((t) => {
    const row = document.createElement("div"); row.className = "tn-row";
    const head = document.createElement("div"); head.className = "row spread";
    const title = document.createElement("h3"); title.textContent = t.name || ("#" + t.id);
    const badge = document.createElement("span"); badge.className = "badge st-" + t.status;
    badge.textContent = TN_STATUS[t.status] || t.status;
    head.append(title, badge);

    const isServer = t.role === "server";

    const meta = document.createElement("div"); meta.className = "meta";
    const m = (k, v) => { const s = document.createElement("span"); const b = document.createElement("b"); b.className = "ltr"; b.textContent = v; s.append(k + " ", b); return s; };
    if (isServer) meta.append(m("نقش", "سرور (اکسیت)"), m("کلاینت", t.peer_real_ip || "—"));
    else meta.append(m("SOCKS", "127.0.0.1:" + t.socks_port), m("مقصد", t.peer_real_ip || "—"));
    if (t.primary) meta.append(m("آی‌پی اسپوف", t.primary));
    if (t.pool_size) meta.append(m("تعداد اسپوف", fa(t.pool_size)));
    if (t.data_port) meta.append(m("پورت داده", fa(t.data_port)));
    if (t.status === "running" && t.uptime) meta.append(m("آپ‌تایم", fa(t.uptime) + "s"));
    if (t.error) { const e = document.createElement("span"); e.style.color = "var(--bad)"; e.textContent = t.error; meta.append(e); }
    if (!isServer) {
      const chk = tn.checks[t.id];
      if (chk) {
        const e = document.createElement("span");
        e.style.color = chk.ok ? "var(--ok)" : "var(--bad)";
        e.textContent = chk.ok
          ? ("عبور ✓ " + fa(chk.latency_ms) + "ms" + (chk.exit_ip ? " · خروجی " + chk.exit_ip : ""))
          : ("عبور ✗ " + (chk.detail || ""));
        meta.append(e);
      }
    }

    const acts = document.createElement("div"); acts.className = "actions";
    const toggle = document.createElement("button");
    if (t.status === "running") { toggle.className = "ghost"; toggle.textContent = "توقف"; toggle.onclick = () => tnAction("stop", t.id); }
    else { toggle.className = "cta"; toggle.textContent = "فعال‌سازی"; toggle.onclick = () => tnAction("start", t.id); }
    const restart = document.createElement("button"); restart.className = "ghost"; restart.textContent = "ری‌استارت"; restart.onclick = () => tnAction("restart", t.id);
    const logsBtn = document.createElement("button"); logsBtn.className = "ghost"; logsBtn.textContent = "لاگ"; logsBtn.onclick = () => openTnLogs(t.id);
    const del = document.createElement("button"); del.className = "ghost"; del.textContent = "حذف";
    del.onclick = () => { if (confirm("این تانل حذف شود؟")) tnAction("delete", t.id); };
    if (isServer) {
      acts.append(toggle, restart, logsBtn, del);
    } else {
      const srvBtn = document.createElement("button"); srvBtn.className = "ghost"; srvBtn.textContent = "دستور سرور"; srvBtn.onclick = () => openTnServer(t.id);
      const chkBtn = document.createElement("button"); chkBtn.className = "ghost"; chkBtn.textContent = "تست عبور"; chkBtn.onclick = () => checkTunnel(t.id, chkBtn);
      acts.append(toggle, restart, srvBtn, chkBtn, logsBtn, del);
    }

    row.append(head, meta, acts);
    box.appendChild(row);
  });
}
async function tnAction(path, id) {
  try { const d = await api("/api/tunnels/" + path, { method: "POST", body: { id: id } }); renderTunnels(d.tunnels || []); }
  catch (e) { toast(e.message, "err"); }
}
async function checkTunnel(id, btn) {
  if (btn) { btn.disabled = true; btn.textContent = "در حال تست…"; }
  try {
    const r = await api("/api/tunnels/check", { method: "POST", body: { id: id } });
    tn.checks[id] = r;
    if (r.ok) toast("ترافیک عبور کرد — " + fa(r.latency_ms) + "ms" + (r.exit_ip ? " · خروجی " + r.exit_ip : ""), "ok");
    else toast(r.detail || "ترافیک عبور نکرد", "err");
    loadTunnels();
  } catch (e) { toast(e.message, "err"); }
  finally { if (btn) { btn.disabled = false; btn.textContent = "تست عبور"; } }
}
$("tn-create").addEventListener("click", async () => {
  setError("tn-error", "");
  const ips = $("tn-ips").value.split(/[\s,;]+/).filter(Boolean);
  const btn = $("tn-create"); btn.disabled = true;
  try {
    const res = await api("/api/tunnels", { method: "POST", body: {
      name: $("tn-name").value.trim(),
      peer_real_ip: $("tn-peer").value.trim(),
      socks_port: parseInt($("tn-socks").value, 10) || 0,
      spoof_ips: ips
    }});
    $("tn-name").value = ""; $("tn-ips").value = "";
    toast("تانل ساخته شد — دستور سرور خارج آماده است", "ok");
    if (res && res.command) {
      showTnServer({ name: (res.tunnel && res.tunnel.name) || "", command: res.command, server_toml: res.server_toml });
    }
    loadTunnels();
  } catch (e) { setError("tn-error", e.message); }
  finally { btn.disabled = false; }
});
$("imp-save").addEventListener("click", async () => {
  setError("imp-error", "");
  const name = $("imp-name").value.trim();
  const config = $("imp-config").value.trim();
  if (!config) { setError("imp-error", "کانفیگ سرور یا دستور کامل را بگذار"); return; }
  const btn = $("imp-save"); btn.disabled = true;
  try {
    await api("/api/tunnels/import", { method: "POST", body: { name: name, config: config } });
    $("imp-name").value = ""; $("imp-config").value = "";
    toast("سرور ذخیره و اجرا شد", "ok");
    loadTunnels();
  } catch (e) { setError("imp-error", e.message); }
  finally { btn.disabled = false; }
});
async function openTnServer(id) {
  try { showTnServer(await api("/api/tunnels/server?id=" + id)); }
  catch (e) { toast(e.message, "err"); }
}
function showTnServer(d) {
  $("tn-server-name").textContent = d.name || "";
  $("tn-server-cmd").value = d.command || "";
  $("tn-server-toml").value = d.server_toml || "";
  $("tn-server-card").hidden = false;
  $("tn-server-card").scrollIntoView({ behavior: "smooth", block: "nearest" });
}
$("tn-server-close").addEventListener("click", () => { $("tn-server-card").hidden = true; });
$("tn-server-cmd-copy").addEventListener("click", async () => {
  const ok = await copyText($("tn-server-cmd").value);
  toast(ok ? "دستور کپی شد" : "کپی نشد", ok ? "ok" : "err");
});
$("tn-server-toml-dl").addEventListener("click", () => { downloadText($("tn-server-toml").value, "server.toml"); });
function openTnLogs(id) {
  tn.logsId = id;
  $("tn-logs-card").hidden = false;
  pollTnLogs();
  if (!tn.logsTimer) tn.logsTimer = setInterval(pollTnLogs, 1500);
  $("tn-logs-card").scrollIntoView({ behavior: "smooth", block: "nearest" });
}
async function pollTnLogs() {
  if (!tn.logsId || document.hidden) return;
  try {
    const d = await api("/api/tunnels/logs?id=" + tn.logsId);
    $("tn-logs").textContent = (d.logs || []).join("\n") || "— بدون لاگ —";
  } catch (e) { /* ignore */ }
}
$("tn-logs-close").addEventListener("click", () => {
  $("tn-logs-card").hidden = true; tn.logsId = 0;
  clearInterval(tn.logsTimer); tn.logsTimer = 0;
});

/* ===== dashboard (home) ===== */
const dash = { listTimer: 0, checkTimer: 0 };
function startDash() {
  loadDash();
  if (!dash.listTimer) dash.listTimer = setInterval(loadDash, 4000);
  runDashChecks();
  if (!dash.checkTimer) dash.checkTimer = setInterval(runDashChecks, 20000);
}
function stopDash() {
  clearInterval(dash.listTimer); dash.listTimer = 0;
  clearInterval(dash.checkTimer); dash.checkTimer = 0;
}
async function loadDash() {
  if (document.hidden) return;
  try { const d = await api("/api/tunnels"); renderDash(d.tunnels || []); } catch (e) { /* 401/403 handled */ }
}
const isClient = (t) => t.role !== "server";
function renderDash(list) {
  const running = list.filter((t) => t.status === "running");
  const proxies = running.filter(isClient);
  $("st-total").textContent = fa(list.length);
  $("st-active").textContent = fa(running.length);
  $("st-proxies").textContent = fa(proxies.length);
  const lats = [];
  list.forEach((t) => { const c = tn.checks[t.id]; if (c && c.ok && c.latency_ms) lats.push(c.latency_ms); });
  $("st-lat").textContent = lats.length ? fa(Math.round(lats.reduce((a, b) => a + b, 0) / lats.length)) + "ms" : "—";

  const box = $("dash-list");
  box.replaceChildren();
  if (!list.length) {
    const p = document.createElement("p"); p.className = "muted";
    p.textContent = "هنوز تانلی ساخته نشده. از تب «تانل‌ها» یک لوکیشن اضافه کن.";
    box.appendChild(p);
    return;
  }
  const wrap = document.createElement("div"); wrap.className = "table-wrap";
  const table = document.createElement("table");
  const thead = document.createElement("thead");
  const hr = document.createElement("tr");
  ["لوکیشن", "وضعیت", "SOCKS", "مقصد", "عبور", "تأخیر", "سرعت", "آی‌پی خروجی"].forEach((h) => {
    const th = document.createElement("th"); th.textContent = h; th.scope = "col"; hr.appendChild(th);
  });
  thead.appendChild(hr);
  const tbody = document.createElement("tbody");
  list.forEach((t) => {
    const tr = document.createElement("tr");
    const td = (txt, cls) => { const d = document.createElement("td"); if (cls) d.className = cls; if (txt !== undefined) d.textContent = txt; return d; };
    const server = !isClient(t);
    // name
    tr.appendChild(td(t.name || ("#" + t.id)));
    // status
    const stTd = document.createElement("td");
    const b = document.createElement("span"); b.className = "badge st-" + t.status; b.textContent = TN_STATUS[t.status] || t.status;
    stTd.appendChild(b); tr.appendChild(stTd);
    // socks / dest
    tr.appendChild(td(server ? "— (سرور)" : "127.0.0.1:" + t.socks_port, "ltr"));
    tr.appendChild(td(t.peer_real_ip || "—", "ltr"));
    // pass / latency / speed / exit
    const c = tn.checks[t.id];
    const sp = tn.speeds[t.id];
    if (server) {
      tr.appendChild(td("—")); tr.appendChild(td("—")); tr.appendChild(td("—")); tr.appendChild(td("—"));
    } else {
      const passTd = document.createElement("td");
      if (c) { passTd.textContent = c.ok ? "✓" : "✗"; passTd.style.color = c.ok ? "var(--ok)" : "var(--bad)"; passTd.title = c.detail || ""; }
      else if (t.status === "running") { passTd.textContent = "…"; passTd.className = "muted"; }
      else { passTd.textContent = "—"; }
      tr.appendChild(passTd);
      tr.appendChild(td(c && c.ok && c.latency_ms ? fa(c.latency_ms) + "ms" : "—", "ltr"));
      const spTd = document.createElement("td"); spTd.className = "ltr";
      if (sp && sp.ok) {
        const dn = Math.round((sp.down_mbps != null ? sp.down_mbps : sp.mbps) * 10) / 10;
        const up = Math.round((sp.up_mbps || 0) * 10) / 10;
        spTd.textContent = "↓" + fa(dn) + (up ? " ↑" + fa(up) : "") + " Mbps";
        const ping = sp.ping_ms ? Math.round(sp.ping_ms) : 0;
        const jit = sp.jitter_ms ? Math.round(sp.jitter_ms) : 0;
        spTd.title = [ping ? "پینگ " + fa(ping) + "ms" : "", jit ? "جیتر " + fa(jit) + "ms" : ""].filter(Boolean).join(" · ");
      } else { spTd.textContent = "—"; }
      tr.appendChild(spTd);
      tr.appendChild(td(c && c.exit_ip ? c.exit_ip : "—", "ltr"));
    }
    tbody.appendChild(tr);
  });
  table.append(thead, tbody);
  wrap.appendChild(table);
  box.appendChild(wrap);
}
async function runDashChecks() {
  if (document.hidden) return;
  let list = [];
  try { const d = await api("/api/tunnels"); list = d.tunnels || []; } catch (e) { return; }
  for (const t of list.filter((x) => x.status === "running" && isClient(x))) {
    try { tn.checks[t.id] = await api("/api/tunnels/check", { method: "POST", body: { id: t.id } }); }
    catch (e) { /* ignore a single failed check */ }
  }
  renderDash(list);
}
async function runDashSpeeds() {
  let list = [];
  try { const d = await api("/api/tunnels"); list = d.tunnels || []; } catch (e) { return; }
  const targets = list.filter((x) => x.status === "running" && isClient(x));
  if (!targets.length) { toast("تانل کلاینت فعالی برای تست سرعت نیست", "err"); return; }
  toast("تست سرعت شروع شد (دانلود، آپلود، پینگ و جیتر)…", "info");
  for (const t of targets) {
    try { tn.speeds[t.id] = await api("/api/tunnels/speed", { method: "POST", body: { id: t.id } }); renderDash(list); }
    catch (e) { /* ignore a single failed speed test */ }
  }
}
async function runDashOptimize() {
  let list = [];
  try { const d = await api("/api/tunnels"); list = d.tunnels || []; } catch (e) { return; }
  const targets = list.filter((x) => x.status === "running" && isClient(x));
  if (!targets.length) { toast("تانل کلاینت فعالی برای بهینه‌سازی نیست", "err"); return; }
  toast("بهینه‌سازی خودکار شروع شد (تست سرعت، تنظیم و ری‌استارت)…", "info");
  for (const t of targets) {
    try {
      const r = await api("/api/tunnels/optimize", { method: "POST", body: { id: t.id } });
      if (r && r.ok && r.applied && r.after) {
        tn.speeds[t.id] = r.speed;
        toast(t.name + ": MTU " + fa(r.after.mtu) + "، پنجره " + fa(Math.round(r.after.initial_cwnd)) + "، " + fa(r.after.tunnel_count) + " تونل", "ok");
      } else {
        toast(t.name + ": " + (r && r.detail ? r.detail : "بهینه‌سازی انجام نشد"), "err");
      }
    } catch (e) { toast(t.name + ": خطا در بهینه‌سازی", "err"); }
  }
  // Configs and processes changed — reload the table from the server.
  try { const d = await api("/api/tunnels"); renderDash(d.tunnels || []); } catch (e) { /* ignore */ }
}
$("dash-refresh").addEventListener("click", () => { runDashChecks(); toast("بررسی عبور شروع شد", "info"); });
$("dash-speed").addEventListener("click", () => { runDashSpeeds(); });
$("dash-optimize").addEventListener("click", () => { runDashOptimize(); });

/* ===== server registry (peers) ===== */
let peersCache = [];
const PEER_ROLE_FA = { exit: "خارج", source: "ایران" };
async function loadPeers() {
  try { const d = await api("/api/peers"); peersCache = d.peers || []; renderPeers(); } catch (e) { /* 401/403 handled */ }
}
function renderPeers() {
  const dl = $("peers-opts");
  if (dl) {
    dl.replaceChildren();
    peersCache.forEach((p) => {
      const o = document.createElement("option");
      o.value = p.ip;
      o.label = p.name + (p.role ? " · " + (PEER_ROLE_FA[p.role] || "") : "");
      dl.appendChild(o);
    });
  }
  const box = $("peer-list");
  if (!box) return;
  box.replaceChildren();
  if (!peersCache.length) {
    const p = document.createElement("p"); p.className = "muted"; p.textContent = "هنوز سروری ثبت نشده.";
    box.appendChild(p); return;
  }
  peersCache.forEach((pr) => {
    const row = document.createElement("div"); row.className = "dash-row";
    const r = document.createElement("div"); r.className = "row spread";
    const left = document.createElement("div");
    const nm = document.createElement("b"); nm.textContent = pr.name;
    const ip = document.createElement("span"); ip.className = "ltr muted"; ip.textContent = "  " + pr.ip + (pr.role ? "  · " + (PEER_ROLE_FA[pr.role] || "") : "");
    left.append(nm, ip);
    const del = document.createElement("button"); del.className = "ghost"; del.textContent = "حذف";
    del.onclick = async () => {
      try { const d = await api("/api/peers/delete", { method: "POST", body: { id: pr.id } }); peersCache = d.peers || []; renderPeers(); }
      catch (e) { toast(e.message, "err"); }
    };
    r.append(left, del);
    row.appendChild(r);
    box.appendChild(row);
  });
}
$("peer-add").addEventListener("click", async () => {
  setError("peer-error", "");
  const btn = $("peer-add"); btn.disabled = true;
  try {
    const d = await api("/api/peers", { method: "POST", body: { name: $("peer-name").value.trim(), ip: $("peer-ip").value.trim(), role: $("peer-role").value } });
    peersCache = d.peers || [];
    $("peer-name").value = ""; $("peer-ip").value = "";
    renderPeers();
    toast("سرور ثبت شد", "ok");
  } catch (e) { setError("peer-error", e.message); }
  finally { btn.disabled = false; }
});

/* ===== boot ===== */
applyRole("simple");
applyRole("list");
selectView("dash");
api("/api/me")
  .then((me) => { applyMe(me); gate(me); })
  .catch(() => showLogin());
