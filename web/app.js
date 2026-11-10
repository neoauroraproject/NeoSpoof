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
  setTimeout(() => { el.classList.add("is-out"); setTimeout(() => el.remove(), 250); }, 3600);
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
  el.classList.toggle("c-bad", n > 1000);
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
    ta.className = "clip-temp";
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
  openModal("tn-create-modal");
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
  dd.className = me.raw_ready ? "c-ok" : "c-bad";
  dl.append(dt, dd);
  add("مدت گوش دادن", fa(me.listen_seconds) + " ثانیه");
  add("نرخ ارسال", fa(me.packets_per_sec) + " بسته/ثانیه");
  const lic = me.license || {};
  const ldt = document.createElement("dt"); ldt.textContent = "لایسنس";
  const ldd = document.createElement("dd");
  ldd.textContent = lic.activated ? (lic.active ? "فعال" : "منقضی") : (lic.trial ? "نسخه آزمایشی" : "غیرفعال");
  ldd.className = (lic.activated && lic.active) ? "c-ok" : (lic.trial ? "c-warn" : "c-bad");
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

/* ===== tunnels (per-tunnel workspace) ===== */
const tn = { listTimer: 0, statsTimer: 0, logsTimer: 0, logsOpen: false, sel: 0, list: [], checks: {}, speeds: {}, stats: {}, spark: {}, tuneCache: {} };
const TN_STATUS = { running: "فعال", stopped: "متوقف", error: "خطا" };
const esc = (s) => String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const round1 = (x) => Math.round((x || 0) * 10) / 10;

/* sub-switch: workspace vs port-forward */
document.querySelectorAll('#view-tunnels > .subswitch [data-tnsub]').forEach((b) => {
  b.addEventListener("click", () => {
    const sub = b.dataset.tnsub;
    document.querySelectorAll('#view-tunnels > .subswitch [data-tnsub]').forEach((x) => x.setAttribute("aria-selected", String(x === b)));
    $("tn-work").hidden = sub !== "work";
    $("tn-fwd").hidden = sub !== "fwd";
  });
});

/* modals */
function openModal(id) { const m = $(id); if (m) m.hidden = false; }
function closeModal(id) { const m = $(id); if (m) m.hidden = true; }
document.querySelectorAll("[data-close]").forEach((el) => el.addEventListener("click", () => closeModal(el.dataset.close)));
["tn-new-btn", "tn-empty-new"].forEach((id) => { const e = $(id); if (e) e.addEventListener("click", () => openModal("tn-create-modal")); });
["tn-import-btn", "tn-empty-import"].forEach((id) => { const e = $(id); if (e) e.addEventListener("click", () => openModal("tn-import-modal")); });

function startTunnels() {
  loadTunnels();
  if (!tn.listTimer) tn.listTimer = setInterval(loadTunnels, 4000);
  if (!tn.statsTimer) tn.statsTimer = setInterval(pollTnStats, 1500);
}
function stopTunnels() {
  clearInterval(tn.listTimer); tn.listTimer = 0;
  clearInterval(tn.statsTimer); tn.statsTimer = 0;
  clearInterval(tn.logsTimer); tn.logsTimer = 0; tn.logsOpen = false;
}
async function loadTunnels() {
  if (document.hidden) return;
  try {
    const d = await api("/api/tunnels");
    tn.list = d.tunnels || [];
    if (!tn.sel || !tn.list.some((t) => t.id === tn.sel)) tn.sel = tn.list.length ? tn.list[0].id : 0;
    renderSwitcher();
    renderWorkspace();
    fillFwdTunnelSelect(tn.list);
    loadForwards();
  } catch (e) { /* 401/403 handled */ }
}
async function pollTnStats() {
  if (document.hidden || !tn.list.length) return;
  try { const d = await api("/api/tunnels/stats"); tn.stats = d.stats || {}; updateLive(); } catch (e) { /* ignore */ }
}

const tnById = (id) => tn.list.find((t) => t.id === id);
function selectTunnel(id) { tn.sel = id; renderSwitcher(); renderWorkspace(); }

function renderSwitcher() {
  const bar = $("tn-switcher"), empty = $("tn-empty"), ws = $("tn-workspace");
  if (!bar) return;
  if (!tn.list.length) {
    bar.replaceChildren();
    if (empty) empty.hidden = false;
    if (ws) ws.hidden = true;
    return;
  }
  if (empty) empty.hidden = true;
  if (ws) ws.hidden = false;
  bar.innerHTML = tn.list.map((t) => {
    const on = t.id === tn.sel;
    const role = t.role === "server" ? "اکسیت" : "ایران";
    return `<button type="button" class="tn-pill${on ? " on" : ""} st-${t.status}" data-tnsel="${t.id}" role="tab" aria-selected="${on}">
      <span class="tn-pill-dot"></span>
      <span class="tn-pill-name">${esc(t.name || ("#" + t.id))}</span>
      <span class="tn-pill-role">${role}</span>
    </button>`;
  }).join("");
  bar.querySelectorAll("[data-tnsel]").forEach((b) => b.addEventListener("click", () => selectTunnel(parseInt(b.dataset.tnsel, 10))));
}

function tile(label, key, unit, arrow) {
  return `<div class="tn-tile tn-tile-${key}">
    <div class="tn-tile-top"><span class="tn-tile-label">${label}</span><span class="tn-tile-arrow">${arrow}</span></div>
    <div class="tn-tile-val"><span id="tn-t-${key}">—</span><span class="tn-tile-unit">${unit}</span></div>
  </div>`;
}
function op(name, label, danger) {
  return `<button type="button" class="tn-op${danger ? " danger" : ""}" data-op="${name}">${label}</button>`;
}

function renderWorkspace() {
  const ws = $("tn-workspace");
  if (!ws) return;
  const t = tnById(tn.sel);
  if (!t) { ws.innerHTML = ""; return; }
  const server = t.role === "server";
  const running = t.status === "running";
  const statusFa = TN_STATUS[t.status] || t.status;

  ws.innerHTML = `
  <section class="card tn-head">
    <div class="tn-head-main">
      <span class="tn-status-dot st-${t.status}"></span>
      <div class="tn-head-text">
        <h2>${esc(t.name || ("#" + t.id))}</h2>
        <div class="tn-sub muted">${server ? "تانل سرور (اکسیت)" : "SOCKS 127.0.0.1:" + t.socks_port} · ${esc(t.peer_real_ip || "—")}</div>
      </div>
    </div>
    <span class="tn-badge st-${t.status}">${statusFa}</span>
  </section>

  ${server ? `
  <section class="card tn-serverbox">
    <p class="muted lead">این یک تانل <b>سرور (اکسیت)</b> است؛ پروکسی و تست سرعت ندارد. فقط اجرا، ری‌استارت و لاگ دارد.</p>
  </section>` : `
  <section class="card tn-live">
    <div class="tn-tiles">
      ${tile("دانلود", "down", "Mbps", "↓")}
      ${tile("آپلود", "up", "Mbps", "↑")}
      ${tile("پینگ", "ping", "ms", "")}
      ${tile("جیتر", "jitter", "ms", "")}
    </div>
    <svg class="tn-spark" viewBox="0 0 100 30" preserveAspectRatio="none" aria-hidden="true"><polyline id="tn-spark-line" points=""></polyline></svg>
    <div class="tn-chips" id="tn-chips"></div>
    <div class="tn-exit muted" id="tn-exit"></div>
  </section>`}

  <section class="card tn-ops">
    <h3>عملیات</h3>
    <div class="tn-ops-grid">
      ${running ? op("stop", "توقف") : op("start", "فعال‌سازی")}
      ${op("restart", "ری‌استارت")}
      ${server ? "" : op("check", "بررسی عبور")}
      ${server ? "" : op("speed", "تست سرعت")}
      ${server ? "" : op("optimize", "بهینه‌سازی خودکار")}
      ${op("logs", "لاگ")}
      ${server ? "" : op("server", "دستور سرور")}
      ${op("delete", "حذف", true)}
    </div>
    <div id="tn-op-result" class="tn-op-result" hidden></div>
  </section>

  ${server ? "" : `
  <section class="card tn-auto">
    <div class="row spread">
      <h3>تنظیمات خودکار این تانل</h3>
      <label class="tn-toggle"><input type="checkbox" id="tn-auto-on"><span class="tn-switch"></span><span>بهینه‌سازی خودکار</span></label>
    </div>
    <p class="muted">فقط روی همین تانل اعمال می‌شود. «بهینه‌سازی خودکار» را روشن کنی، پنل خودش MTU/پنجره/تعداد تونل را با سرعت مسیر تنظیم می‌کند.</p>
    <div class="grid2 tn-tune-grid">
      <label>MTU<input id="tn-tune-mtu" type="number" class="ltr" min="576" max="1500" step="10"></label>
      <label>تعداد تونل موازی<input id="tn-tune-tc" type="number" class="ltr" min="1" max="16" step="1"></label>
      <label>پنجرهٔ ازدحام اولیه<input id="tn-tune-cwnd" type="number" class="ltr" min="1" max="512" step="1"></label>
    </div>
    <div class="row">
      <button class="cta" id="tn-tune-save" type="button">اعمال</button>
      <button class="ghost" id="tn-tune-auto" type="button">بهینه‌سازی خودکار الان</button>
      <span class="tn-tune-info muted" id="tn-tune-info"></span>
    </div>
  </section>`}

  <section class="card tn-logs-card" id="tn-logs-wrap" hidden>
    <div class="row spread"><h3>لاگ لوکیشن</h3><button class="icon-only" id="tn-logs-hide" type="button" aria-label="بستن">✕</button></div>
    <pre class="logbox ltr" id="tn-logs">— بدون لاگ —</pre>
  </section>

  <section class="card tn-server-card" id="tn-server-wrap" hidden>
    <div class="row spread"><h3>دستور سرور خارج</h3><button class="icon-only" id="tn-server-hide" type="button" aria-label="بستن">✕</button></div>
    <p class="muted lead">این متن را روی سرور مقصد در بخش «افزودن سرور از کانفیگ» بگذار، یا این دستور را آنجا در ترمینال اجرا کن. پورت و کلید حفظ می‌شوند.</p>
    <label>دستور آماده<textarea id="tn-server-cmd" class="ltr" readonly rows="8"></textarea></label>
    <textarea id="tn-server-toml" hidden></textarea>
    <div class="actions">
      <button class="primary" id="tn-server-copy" type="button">کپی دستور</button>
      <button class="ghost" id="tn-server-dl" type="button">دانلود server.toml</button>
    </div>
  </section>`;

  ws.querySelectorAll("[data-op]").forEach((b) => b.addEventListener("click", () => tnOp(b.dataset.op, t.id, b)));
  const lh = $("tn-logs-hide"); if (lh) lh.onclick = () => { $("tn-logs-wrap").hidden = true; tn.logsOpen = false; clearInterval(tn.logsTimer); tn.logsTimer = 0; };
  if (!server) {
    wireTune(t);
    const sh = $("tn-server-hide"); if (sh) sh.onclick = () => { $("tn-server-wrap").hidden = true; };
    const sc = $("tn-server-copy"); if (sc) sc.onclick = async () => { const ok = await copyText($("tn-server-cmd").value); toast(ok ? "دستور کپی شد" : "کپی نشد", ok ? "ok" : "err"); };
    const sd = $("tn-server-dl"); if (sd) sd.onclick = () => downloadText($("tn-server-toml").value, "server.toml");
  }
  updateLive();
}

function updateLive() {
  const t = tnById(tn.sel);
  if (!t || t.role === "server") return;
  const s = tn.stats[String(tn.sel)] || {};
  const sp = tn.speeds[tn.sel] || {};
  const setT = (k, v) => { const el = $("tn-t-" + k); if (el) el.textContent = v; };
  const down = s.down_mbps != null ? s.down_mbps : (sp.down_mbps != null ? sp.down_mbps : (sp.mbps || 0));
  const up = s.up_mbps != null ? s.up_mbps : (sp.up_mbps || 0);
  const ping = s.ping_ms || sp.ping_ms || 0;
  const jit = s.jitter_ms || sp.jitter_ms || 0;
  setT("down", down ? fa(round1(down)) : "—");
  setT("up", up ? fa(round1(up)) : "—");
  setT("ping", ping ? fa(Math.round(ping)) : "—");
  setT("jitter", jit ? fa(Math.round(jit)) : "—");
  const chips = $("tn-chips");
  if (chips) {
    const parts = [];
    if (s.loss_pct != null) parts.push(["loss", fa(round1(s.loss_pct)) + "٪"]);
    if (s.cwnd != null) parts.push(["cwnd", fa(Math.round(s.cwnd))]);
    if (s.srtt_ms != null) parts.push(["srtt", fa(Math.round(s.srtt_ms)) + "ms"]);
    if (s.retx != null) parts.push(["retx", fa(s.retx)]);
    if (s.resets) parts.push(["reset", fa(s.resets)]);
    chips.innerHTML = parts.map(([k, v]) => `<span class="chip"><span class="muted">${k}</span> <b>${v}</b></span>`).join("");
  }
  const ex = $("tn-exit");
  if (ex) { const c = tn.checks[tn.sel]; ex.textContent = (c && c.exit_ip) ? ("آی‌پی خروجی: " + c.exit_ip) : ""; }
  const a = tn.spark[tn.sel] || (tn.spark[tn.sel] = []);
  a.push(down || 0); if (a.length > 40) a.shift();
  const line = $("tn-spark-line");
  if (line && a.length > 1) {
    const max = Math.max(1, ...a);
    line.setAttribute("points", a.map((v, i) => (i / (a.length - 1) * 100).toFixed(1) + "," + (30 - (v / max) * 27).toFixed(1)).join(" "));
  }
}

async function tnOp(opName, id, btn) {
  if (opName === "delete") { if (!confirm("این تانل حذف شود؟")) return; return tnAction("delete", id); }
  if (opName === "start" || opName === "stop" || opName === "restart") return tnAction(opName, id);
  if (opName === "check") return checkTunnel(id, btn);
  if (opName === "speed") return speedTunnel(id, btn);
  if (opName === "optimize") return optimizeTunnel(id, btn);
  if (opName === "logs") return toggleLogs(id);
  if (opName === "server") return openServerCmd(id);
}
async function tnAction(path, id) {
  try {
    const d = await api("/api/tunnels/" + path, { method: "POST", body: { id } });
    tn.list = d.tunnels || tn.list;
    if (!tn.list.some((t) => t.id === tn.sel)) tn.sel = tn.list.length ? tn.list[0].id : 0;
    renderSwitcher(); renderWorkspace();
  } catch (e) { toast(e.message, "err"); }
}
function withBtn(btn, busy, fn) {
  const old = btn ? btn.textContent : "";
  if (btn) { btn.disabled = true; btn.textContent = busy; }
  return Promise.resolve().then(fn).catch((e) => opResult(e.message, false)).finally(() => { if (btn) { btn.disabled = false; btn.textContent = old; } });
}
function opResult(msg, ok) {
  const el = $("tn-op-result"); if (!el) return;
  el.hidden = false; el.textContent = msg; el.className = "tn-op-result " + (ok ? "ok" : "bad");
}
function checkTunnel(id, btn) {
  return withBtn(btn, "در حال بررسی…", async () => {
    const r = await api("/api/tunnels/check", { method: "POST", body: { id } });
    tn.checks[id] = r;
    opResult(r.ok ? ("عبور ✓ " + fa(r.latency_ms) + "ms" + (r.exit_ip ? " · خروجی " + r.exit_ip : "")) : ("عبور ✗ " + (r.detail || "")), r.ok);
    updateLive();
  });
}
function speedTunnel(id, btn) {
  return withBtn(btn, "در حال تست…", async () => {
    const r = await api("/api/tunnels/speed", { method: "POST", body: { id } });
    tn.speeds[id] = r;
    const dn = round1(r.down_mbps != null ? r.down_mbps : (r.mbps || 0)), up = round1(r.up_mbps || 0);
    opResult("سرعت: ↓" + fa(dn) + " ↑" + fa(up) + " Mbps · پینگ " + fa(Math.round(r.ping_ms || 0)) + "ms · جیتر " + fa(Math.round(r.jitter_ms || 0)) + "ms", r.ok !== false);
    updateLive();
  });
}
function optimizeTunnel(id, btn) {
  return withBtn(btn, "در حال بهینه‌سازی…", async () => {
    const r = await api("/api/tunnels/optimize", { method: "POST", body: { id } });
    if (r && r.ok && r.after) {
      if (r.speed) tn.speeds[id] = r.speed;
      opResult("بهینه شد: MTU " + fa(r.after.mtu) + " · پنجره " + fa(Math.round(r.after.initial_cwnd)) + " · " + fa(r.after.tunnel_count) + " تونل", true);
      loadTune(id);
    } else opResult((r && r.detail) ? r.detail : "بهینه‌سازی انجام نشد", false);
    updateLive();
  });
}
function toggleLogs(id) {
  const wrap = $("tn-logs-wrap");
  if (!wrap) return;
  if (tn.logsOpen) { wrap.hidden = true; tn.logsOpen = false; clearInterval(tn.logsTimer); tn.logsTimer = 0; return; }
  wrap.hidden = false; tn.logsOpen = true;
  pollLogs(id);
  tn.logsTimer = setInterval(() => pollLogs(id), 1500);
  wrap.scrollIntoView({ behavior: "smooth", block: "nearest" });
}
async function pollLogs(id) {
  if (document.hidden) return;
  try { const d = await api("/api/tunnels/logs?id=" + id); const el = $("tn-logs"); if (el) el.textContent = (d.logs || []).join("\n") || "— بدون لاگ —"; } catch (e) { /* ignore */ }
}
async function openServerCmd(id) {
  try {
    const d = await api("/api/tunnels/server?id=" + id);
    const w = $("tn-server-wrap"); if (!w) return;
    $("tn-server-cmd").value = d.command || "";
    $("tn-server-toml").value = d.server_toml || "";
    w.hidden = false; w.scrollIntoView({ behavior: "smooth", block: "nearest" });
  } catch (e) { toast(e.message, "err"); }
}

/* per-tunnel auto settings */
async function loadTune(id) {
  try {
    const d = await api("/api/tunnels/tune?id=" + id);
    tn.tuneCache[id] = d;
    const t = d.tune || {};
    if ($("tn-tune-mtu")) $("tn-tune-mtu").value = t.mtu || "";
    if ($("tn-tune-tc")) $("tn-tune-tc").value = t.tunnel_count || "";
    if ($("tn-tune-cwnd")) $("tn-tune-cwnd").value = Math.round(t.initial_cwnd || 0) || "";
    if ($("tn-auto-on")) $("tn-auto-on").checked = !!t.auto;
    const info = $("tn-tune-info"); if (info) info.textContent = t.bdp_kb ? ("BDP ~" + fa(t.bdp_kb) + "KB") : "";
  } catch (e) { /* ignore */ }
}
function wireTune(t) {
  loadTune(t.id);
  const save = $("tn-tune-save"); if (save) save.onclick = () => saveTune(t.id, false);
  const autoNow = $("tn-tune-auto"); if (autoNow) autoNow.onclick = (e) => optimizeTunnel(t.id, e.currentTarget);
  const auto = $("tn-auto-on"); if (auto) auto.onchange = () => saveTune(t.id, true);
}
async function saveTune(id, silent) {
  try {
    const body = {
      id,
      mtu: parseInt($("tn-tune-mtu").value, 10) || 0,
      tunnel_count: parseInt($("tn-tune-tc").value, 10) || 0,
      initial_cwnd: parseFloat($("tn-tune-cwnd").value) || 0,
      auto: $("tn-auto-on").checked
    };
    const d = await api("/api/tunnels/tune", { method: "POST", body });
    if (!silent) toast("تنظیمات تانل ذخیره شد" + (d.restarted ? " و ری‌استارت شد" : ""), "ok");
    loadTune(id);
  } catch (e) { toast(e.message, "err"); }
}

/* create / import (modals) */
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
    closeModal("tn-create-modal");
    toast("تانل ساخته شد — دستور سرور خارج آماده است", "ok");
    if (res && res.tunnel) tn.sel = res.tunnel.id;
    await loadTunnels();
    if (res && res.command) {
      const w = $("tn-server-wrap");
      if (w) { $("tn-server-cmd").value = res.command; $("tn-server-toml").value = res.server_toml || ""; w.hidden = false; }
    }
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
    await api("/api/tunnels/import", { method: "POST", body: { name, config } });
    $("imp-name").value = ""; $("imp-config").value = "";
    closeModal("tn-import-modal");
    toast("سرور ذخیره و اجرا شد", "ok");
    loadTunnels();
  } catch (e) { setError("imp-error", e.message); }
  finally { btn.disabled = false; }
});

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
    if (f.error) { const e = document.createElement("span"); e.className = "c-bad"; e.textContent = f.error; meta.append(e); }
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
/* (legacy flat tunnel list + server/logs handlers removed — superseded by the
   per-tunnel workspace defined above) */

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
      if (c) { passTd.textContent = c.ok ? "✓" : "✗"; passTd.className = "ltr " + (c.ok ? "c-ok" : "c-bad"); passTd.title = c.detail || ""; }
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

/* ===== demo mode (design preview, no backend) ===== */
const DEMO = /(^|[?&])demo(=|&|$)/.test(location.search) || /(^|#)demo\b/.test(location.hash);
if (DEMO) {
  const demoTunnels = [
    { id: 1, name: "آلمان ۱", role: "", status: "running", socks_port: 1080, peer_real_ip: "45.147.231.10", primary: "5.160.12.4", pool_size: 6, data_port: 41234, uptime: 5230, auto: true },
    { id: 2, name: "فنلاند", role: "", status: "running", socks_port: 1081, peer_real_ip: "95.217.44.9", primary: "2.144.18.9", pool_size: 4, data_port: 52110, uptime: 1980, auto: false },
    { id: 3, name: "هلند", role: "", status: "stopped", socks_port: 1082, peer_real_ip: "51.210.7.3", primary: "5.112.9.2", pool_size: 5, data_port: 33001, auto: false },
    { id: 4, name: "سرور آلمان", role: "server", status: "running", peer_real_ip: "2.189.44.7", uptime: 8800 }
  ];
  const demoStats = {
    "1": { down_mbps: 612.4, up_mbps: 358.1, loss_pct: 0.3, cwnd: 184, srtt_ms: 128, in_flight: 164, retx: 21, resets: 0, ping_ms: 128, jitter_ms: 7, running: true },
    "2": { down_mbps: 443.0, up_mbps: 201.5, loss_pct: 0.8, cwnd: 132, srtt_ms: 152, in_flight: 120, retx: 48, resets: 0, ping_ms: 151, jitter_ms: 12, running: true }
  };
  const demoTune = {
    "1": { ok: true, tune: { mtu: 1380, tunnel_count: 4, initial_cwnd: 48, auto: true, bdp_kb: 920 }, defaults: { mtu: 1380, tunnel_count: 4, initial_cwnd: 30 } },
    "2": { ok: true, tune: { mtu: 1280, tunnel_count: 6, initial_cwnd: 64, auto: false, bdp_kb: 640 }, defaults: { mtu: 1380, tunnel_count: 4, initial_cwnd: 30 } }
  };
  const demoMe = { version: "2.1.0", platform: "linux", raw_ready: true, listen_seconds: 180, packets_per_sec: 50, tunnel_count: 4, mtu: 1380, initial_cwnd: 30, site_role: "source", site_name: "ایران-۱", license: { active: true, activated: true, plan: "pro", bound_ip: "2.189.44.7" } };
  const demoPeers = [{ id: 1, name: "آلمان-۱", ip: "45.147.231.10", role: "exit" }, { id: 2, name: "فنلاند", ip: "95.217.44.9", role: "exit" }];
  const demoFwd = [{ id: 1, name: "shadowsocks", status: "running", local_port: 8388, target_ip: "127.0.0.1", target_port: 8388, tunnel_id: 1 }];
  const jit = (v, p) => v * (1 + (Math.random() - 0.5) * p);
  api = function (path) {
    path = String(path);
    const resp = (o) => Promise.resolve(JSON.parse(JSON.stringify(o)));
    if (path.indexOf("/api/me") === 0) return resp(demoMe);
    if (path === "/api/tunnels") return resp({ tunnels: demoTunnels });
    if (path === "/api/tunnels/stats") {
      const s = {};
      for (const k in demoStats) s[k] = Object.assign({}, demoStats[k], { down_mbps: jit(demoStats[k].down_mbps, 0.15), up_mbps: jit(demoStats[k].up_mbps, 0.2), cwnd: Math.round(jit(demoStats[k].cwnd, 0.1)) });
      return resp({ stats: s });
    }
    if (path.indexOf("/api/tunnels/tune") === 0) { const id = (path.match(/id=(\d+)/) || [])[1] || "1"; return resp(demoTune[id] || demoTune["1"]); }
    if (path.indexOf("/api/tunnels/logs") === 0) return resp({ logs: ["[info] tunnel 1 established", "[info] tunnel 1 RTT 128.0 ms", "[info] CC: slow-start → congestion-avoidance cwnd=128", "[info] send task running"] });
    if (path.indexOf("/api/tunnels/server") === 0) return resp({ name: "demo", command: "curl -Ls https://.../install.sh | bash   # سپس «افزودن سرور از کانفیگ»", server_toml: "# server.toml (نمونه)" });
    if (path === "/api/forwards") return resp({ forwards: demoFwd });
    if (path === "/api/peers") return resp({ peers: demoPeers });
    if (path === "/api/settings") return resp(demoMe);
    if (path === "/api/test") return resp({ phase: "idle" });
    return resp({ ok: true });
  };
}

/* ===== boot ===== */
applyRole("simple");
applyRole("list");
selectView("dash");
api("/api/me")
  .then((me) => { applyMe(me); gate(me); if (DEMO) selectView("tunnels"); })
  .catch(() => showLogin());
