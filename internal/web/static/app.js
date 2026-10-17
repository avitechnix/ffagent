"use strict";

const $ = (id) => document.getElementById(id);
let data = null;
let busy = false;

// --- rendering helpers (all text goes through textContent; no innerHTML) ---

function el(tag, attrs = {}, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") n.className = v;
    else if (k === "style") Object.assign(n.style, v); // CSSOM, allowed by the CSP
    else if (k === "text") n.textContent = v;
    else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
    else n.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) {
    if (c === null || c === undefined || c === false) continue;
    n.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return n;
}

const fmt1 = (x) => (Math.round(x * 10) / 10).toFixed(1);
const pct = (x) => Math.round(x * 100) + "%";

function when(iso) {
  if (!iso || iso.startsWith("0001")) return "";
  const d = new Date(iso);
  const now = new Date();
  const mins = Math.round((d - now) / 60000);
  const rel = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  if (Math.abs(mins) < 60) return rel.format(mins, "minute");
  if (Math.abs(mins) < 48 * 60) return rel.format(Math.round(mins / 60), "hour");
  return d.toLocaleString(undefined, { weekday: "short", month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
}

function toast(msg, isError) {
  const t = $("toast");
  t.textContent = msg;
  t.className = "toast" + (isError ? " error" : "");
  t.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => (t.hidden = true), isError ? 6000 : 3000);
}

function empty(title, text) {
  return el("div", { class: "card empty" }, el("h2", { text: title }), el("p", { class: "muted", text }));
}

// --- data ---

async function load(showSpinner) {
  if (busy) return;
  busy = true;
  if (showSpinner) $("refresh").classList.add("spin");
  try {
    const r = await fetch("api/snapshot", { cache: "no-store" });
    if (!r.ok) throw new Error((await r.text()) || r.statusText);
    data = await r.json();
    render();
  } catch (e) {
    toast("Couldn't reach the agent: " + e.message, true);
  } finally {
    busy = false;
    $("refresh").classList.remove("spin");
  }
}

async function decide(item, approve, buttons) {
  buttons.forEach((b) => (b.disabled = true));
  try {
    const r = await fetch(`api/moves/${item.id}/${approve ? "approve" : "decline"}`, {
      method: "POST",
      headers: { "X-FFAgent-Request": "1" },
    });
    const body = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(body.error || r.statusText);
    toast(approve ? "Approved. It runs after a fresh check; you'll get an email with the result." : "Declined.");
    await load(false);
  } catch (e) {
    buttons.forEach((b) => (b.disabled = false));
    toast(e.message, true);
  }
}

// --- views ---

function render() {
  const s = data.status;
  $("team").textContent = s.team || "ffagent";
  const parts = [];
  if (s.week) parts.push("Week " + s.week);
  if (s.record) parts.push(s.record);
  if (s.opponent) parts.push("vs " + s.opponent);
  $("subtitle").textContent = s.ready ? parts.join(" · ") : "Waiting for the agent's first check…";

  const mode = $("mode");
  mode.hidden = false;
  mode.className = "pill " + (s.mode === "approve" ? "approve" : s.mode === "dry-run" ? "dry" : "");
  mode.textContent =
    s.mode === "approve" ? "Approve mode · nothing runs without your OK"
    : s.mode === "dry-run" ? "Dry run · logging only"
    : "Auto mode · moves run without asking";

  const waiting = data.pending ? data.pending.filter((i) => i.status === "pending").length : 0;
  const badge = $("badge");
  badge.hidden = waiting === 0;
  badge.textContent = waiting;
  if (navigator.setAppBadge) (waiting ? navigator.setAppBadge(waiting) : navigator.clearAppBadge()).catch(() => {});

  renderMoves();
  renderMatchup();
  renderRoster();
  renderHistory();
}

function renderMoves() {
  const box = $("moves");
  box.replaceChildren();
  const items = data.pending || [];
  if (!items.length) {
    box.append(empty("No moves waiting", data.status.ready
      ? "When injury news or a better player shows up, proposed moves appear here with the reasoning. Last check " + (when(data.status.last_check) || "pending") + "."
      : "The agent hasn't finished its first check yet."));
    return;
  }
  box.append(el("h2", { class: "section-title", text: "Waiting for you" }));
  for (const it of items) {
    const approveBtn = el("button", { class: "btn-approve", text: "Approve" });
    const declineBtn = el("button", { class: "btn-decline", text: "Decline" });
    const both = [approveBtn, declineBtn];
    approveBtn.addEventListener("click", () => decide(it, true, both));
    declineBtn.addEventListener("click", () => decide(it, false, both));
    box.append(el("article", { class: "card move" },
      el("span", { class: "kind", text: it.label }),
      el("h3", { text: it.summary }),
      it.reason ? el("p", { class: "why" }, el("b", { text: "Why: " }), it.reason) : null,
      it.needs && it.needs.length ? el("p", { class: "needs", text: "Only runs if this runs too: " + it.needs.join("; ") }) : null,
      el("div", { class: "meta" },
        it.kind === "ADD" ? el("span", { text: "Free agent: first come, first served" }) : null,
        it.expires ? el("span", { text: "Expires " + when(it.expires) }) : null),
      it.status === "approved"
        ? el("p", { class: "waiting", text: "Approved: waiting on the move it depends on" })
        : el("div", { class: "actions" }, declineBtn, approveBtn)));
  }
}

function renderMatchup() {
  const box = $("matchup");
  box.replaceChildren();
  const s = data.status;
  if (!s.ready) return box.append(empty("No matchup yet", "Waiting for the agent's first check."));
  if (s.opponent) {
    box.append(el("div", { class: "card" },
      el("div", { class: "score" },
        el("div", {}, el("div", { class: "name", text: s.team }), el("div", { class: "pts", text: fmt1(s.my_projected) })),
        el("div", { class: "vs", text: "vs" }),
        el("div", {}, el("div", { class: "name", text: s.opponent }), el("div", { class: "pts", text: fmt1(s.opp_projected) }))),
      el("div", { class: "meter", role: "img", "aria-label": "Win probability " + pct(s.win_prob) },
        el("div", { style: { width: Math.round(s.win_prob * 100) + "%" } })),
      el("div", { class: "winline" }, el("span", { class: "muted", text: "Win probability" }), el("b", { text: pct(s.win_prob) })),
      s.strategy ? el("p", { class: "muted small", style: { marginTop: "8px" }, text: "Strategy: " + s.strategy }) : null));
  }
  if (data.lineup && data.lineup.length) {
    box.append(el("h2", { class: "section-title", text: "Recommended lineup" }));
    const ul = el("ul", { class: "list" });
    for (const p of data.lineup) {
      ul.append(el("li", {},
        el("span", { class: "slot", text: p.slot }),
        el("div", { class: "grow" }, el("div", { class: "nm" }, p.name, p.locked ? el("span", { class: "tag lock", text: "LOCKED" }) : null), el("div", { class: "sub", text: p.team })),
        el("div", { class: "num", text: fmt1(p.proj) })));
    }
    box.append(el("div", { class: "card" }, ul));
  }
  box.append(el("p", { class: "muted small center", text: "Projections as of " + when(s.planned_at) }));
}

function renderRoster() {
  const box = $("roster");
  box.replaceChildren();
  if (!data.roster || !data.roster.length) return box.append(empty("No roster yet", "Waiting for the agent's first check."));
  const ul = el("ul", { class: "list" });
  for (const p of data.roster) {
    const acc = p.accuracy ? " · " + pct(p.accuracy) + " of ESPN proj" : "";
    const notes = (p.notes || []).filter((n) => n !== p.injury);
    ul.append(el("li", {},
      el("span", { class: "slot", text: p.slot }),
      el("div", { class: "grow" },
        el("div", { class: "nm" }, p.name, p.injury ? el("span", { class: "tag inj", text: p.injury.replace("INJURY_RESERVE", "IR").replace("QUESTIONABLE", "Q").replace("DOUBTFUL", "D") }) : null),
        el("div", { class: "sub", text: `${p.position} ${p.team} · avg ${fmt1(p.avg)} · L3 ${fmt1(p.last3)}${acc}` }),
        notes.length ? el("div", { class: "note", text: notes.join(", ") }) : null),
      el("div", { class: "num" }, fmt1(p.proj), el("small", { text: Math.round(p.ros) + " ROS" }))));
  }
  box.append(el("h2", { class: "section-title", text: "This week · rest of season" }), el("div", { class: "card" }, ul));
  box.append(el("p", { class: "muted small center", text: "Projections adjusted for each player's record against ESPN's, injuries and byes." }));
}

function renderHistory() {
  const box = $("history");
  box.replaceChildren();
  const items = data.history || [];
  if (!items.length) return box.append(empty("Nothing yet", "Moves you approve or decline, and what happened to them, show up here."));
  const labels = { executed: "Done", declined: "Declined", skipped: "Not run", failed: "Failed", expired: "Expired", superseded: "Replaced" };
  const ul = el("ul", { class: "list" });
  for (const it of items) {
    ul.append(el("li", {},
      el("div", { class: "grow" },
        el("div", { class: "nm", text: it.summary }),
        el("div", { class: "sub", text: [when(it.created), it.result && it.result !== "done" && it.result !== "declined by you" ? it.result : "", it.via ? "decided in " + (it.via === "app" ? "the app" : "email") : ""].filter(Boolean).join(" · ") })),
      el("span", { class: "status " + it.status, text: labels[it.status] || it.status })));
  }
  box.append(el("h2", { class: "section-title", text: "Last 7 days" }), el("div", { class: "card" }, ul));
}

// --- tabs and lifecycle ---

function show(tab) {
  for (const b of document.querySelectorAll(".tabbar button")) {
    const on = b.dataset.tab === tab;
    b.setAttribute("aria-selected", on);
    $("tab-" + b.dataset.tab).hidden = !on;
  }
  try { localStorage.setItem("tab", tab); } catch (_) {}
  window.scrollTo(0, 0);
}

document.addEventListener("DOMContentLoaded", () => {
  for (const b of document.querySelectorAll(".tabbar button")) b.addEventListener("click", () => show(b.dataset.tab));
  $("refresh").addEventListener("click", () => load(true));
  let saved = "moves";
  try { saved = localStorage.getItem("tab") || "moves"; } catch (_) {}
  show(saved);
  load(true);
  setInterval(() => { if (document.visibilityState === "visible") load(false); }, 30000);
  document.addEventListener("visibilitychange", () => { if (document.visibilityState === "visible") load(false); });
});
