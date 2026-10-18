"use strict";

const $ = (id) => document.getElementById(id);
let data = null;
let busy = false;
// Follow-up question state per move id, kept across re-renders.
const asks = {}; // id -> { open, draft, pending }
const chat = { draft: "", pending: null }; // the Ask tab

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

const SVG = "http://www.w3.org/2000/svg";
function svg(tag, attrs = {}, ...children) {
  const n = document.createElementNS(SVG, tag);
  for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v);
  children.forEach((c) => n.append(c));
  return n;
}

const ICONS = {
  add: "M19 13h-6v6h-2v-6H5v-2h6V5h2v6h6z",
  swap: "M7 7h11l-3-3 1.4-1.4L21.8 8l-5.4 5.4L15 12l3-3H7zm10 10H6l3 3-1.4 1.4L2.2 16l5.4-5.4L9 12l-3 3h11z",
  ir: "M19 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V5a2 2 0 0 0-2-2zm-2 10h-4v4h-2v-4H7v-2h4V7h2v4h4z",
  lineup: "M3 5h18v2H3zm0 6h18v2H3zm0 6h12v2H3z",
  trade: "M6.99 11 3 15l3.99 4v-3H14v-2H6.99zM21 9l-3.99-4v3H10v2h7.01v3z",
  drop: "M19 13H5v-2h14z",
  check: "M9 16.2 4.8 12l-1.4 1.4L9 19 21 7l-1.4-1.4z",
  chat: "M4 4h16a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H8l-4 4V6a2 2 0 0 1 2-2zm2 5v2h12V9zm0 4v2h8v-2z",
  trophy: "M19 4h-2V3H7v1H5a2 2 0 0 0-2 2v1a4 4 0 0 0 3.8 4A5 5 0 0 0 11 14.9V18H8v2h8v-2h-3v-3.1A5 5 0 0 0 17.2 11 4 4 0 0 0 21 7V6a2 2 0 0 0-2-2zM5 7V6h2v3a2 2 0 0 1-2-2zm14 0a2 2 0 0 1-2 2V6h2z",
  people: "M16 11a3 3 0 1 0-3-3 3 3 0 0 0 3 3zm-8 0a3 3 0 1 0-3-3 3 3 0 0 0 3 3zm0 2c-2.3 0-7 1.2-7 3.5V19h14v-2.5C15 14.2 10.3 13 8 13zm8 0c-.3 0-.6 0-1 .1a4.2 4.2 0 0 1 2 3.4V19h6v-2.5c0-2.3-4.7-3.5-7-3.5z",
  clock: "M13 3a9 9 0 0 0-9 9H1l4 4 4-4H6a7 7 0 1 1 2.1 5L6.7 18.4A9 9 0 1 0 13 3zm-1 5v5l4.3 2.5.7-1.2-3.5-2.1V8z",
};
const icon = (name) => svg("svg", { viewBox: "0 0 24 24", "aria-hidden": "true" }, svg("path", { fill: "currentColor", d: ICONS[name] }));

const fmt1 = (x) => (Math.round((x || 0) * 10) / 10).toFixed(1);
const pct = (x) => Math.round((x || 0) * 100) + "%";

function when(iso) {
  if (!iso || iso.startsWith("0001")) return "";
  const d = new Date(iso);
  const mins = Math.round((d - new Date()) / 60000);
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

function empty(glyph, title, text) {
  return el("div", { class: "card empty" },
    el("div", { class: "glyph" }, icon(glyph)),
    el("h2", { text: title }), el("p", { text }));
}

function title(text, count) {
  return el("h2", { class: "section-title" }, el("span", { text }), count ? el("span", { class: "count", text: count }) : null);
}

// Position color class: starting slots by slot, bench and IR by position.
const STARTING = ["QB", "RB", "WR", "TE", "FLEX", "K", "D/ST", "OP", "RB/WR", "WR/TE"];
function posClass(slot, position) {
  if (slot === "IR") return "p-IR";
  const s = STARTING.includes(slot) ? slot : position;
  if (s === "D/ST") return "p-DST";
  if (s === "OP" || s === "RB/WR" || s === "WR/TE") return "p-FLEX";
  return "p-" + (s || "BE");
}
const slotChip = (slot, position) => el("span", { class: "slot " + posClass(slot, position), text: slot });

// NFL team primary colors, for the backdrop behind player photos.
const TEAM_COLORS = {
  ARI: "#97233F", ATL: "#A71930", BAL: "#241773", BUF: "#00338D", CAR: "#0085CA", CHI: "#0B162A",
  CIN: "#FB4F14", CLE: "#311D00", DAL: "#003594", DEN: "#FB4F14", DET: "#0076B6", GB: "#203731",
  HOU: "#03202F", IND: "#002C5F", JAX: "#006778", KC: "#E31837", LV: "#1a1a1a", LAC: "#0080C6",
  LAR: "#003594", MIA: "#008E97", MIN: "#4F2683", NE: "#002244", NO: "#9F8958", NYG: "#0B2265",
  NYJ: "#125740", PHI: "#004C54", PIT: "#c99400", SF: "#AA0000", SEA: "#002244", TB: "#D50A0A",
  TEN: "#0C2340", WSH: "#5A1414",
};

// Small NFL team logo, from the agent's image proxy.
function teamLogo(abbr) {
  if (!abbr || !TEAM_COLORS[abbr]) return null;
  const img = el("img", { class: "tlogo", src: "img/t/" + abbr.toLowerCase() + ".png", alt: "", loading: "lazy", decoding: "async" });
  img.addEventListener("error", () => img.remove());
  return img;
}

function kickoff(iso) {
  if (!iso || iso.startsWith("0001")) return "";
  return new Date(iso).toLocaleString(undefined, { weekday: "short", hour: "numeric", minute: "2-digit" });
}

// "vs NYJ · Sun 12:00 PM", "@ GB · Sun 7:20 PM", "Bye week".
function gameLine(p, withTime = true) {
  if (!p.opp) return "";
  if (p.opp === "BYE") return "Bye week";
  const vs = p.opp.startsWith("@") ? "@ " + p.opp.slice(1) : "vs " + p.opp;
  const t = withTime && !p.locked ? kickoff(p.kickoff) : "";
  return t ? vs + " · " + t : vs;
}

// A fantasy team's logo (the one its manager picked on ESPN), with
// initials until it loads or if there's none.
function fantasyLogo(id, name) {
  const initials = (name || "?").split(/\s+/).filter(Boolean).map((w) => w[0]).slice(0, 2).join("").toUpperCase();
  const wrap = el("span", { class: "flogo" }, el("span", { class: "initials", text: initials }));
  if (id) {
    const img = el("img", { src: "img/f/" + id + ".png", alt: "", decoding: "async" });
    img.addEventListener("load", () => wrap.classList.add("has-img"));
    img.addEventListener("error", () => img.remove());
    wrap.append(img);
  }
  return wrap;
}

// Counts a number up (or down) to its new value, like a scoreboard.
const shown = {};
function countTo(node, key, to) {
  const from = shown[key] ?? to;
  shown[key] = to;
  node.textContent = fmt1(from);
  if (from === to || matchMedia("(prefers-reduced-motion: reduce)").matches) { node.textContent = fmt1(to); return; }
  const t0 = performance.now(), dur = 700;
  const step = (t) => {
    const k = Math.min(1, (t - t0) / dur), e = 1 - Math.pow(1 - k, 3);
    node.textContent = fmt1(from + (to - from) * e);
    if (k < 1) requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
}

// Player photo (team logo for a D/ST), served by the agent from ESPN's CDN.
// Initials show until it loads, and stay if ESPN has no photo.
function avatar(p, opts = {}) {
  const initials = (p.name || "?").replace(/ D\/ST$/, "").split(/\s+/).map((w) => w[0]).slice(0, 2).join("").toUpperCase();
  const dst = p.position === "D/ST" || p.id < 0;
  const src = dst ? (p.team ? "img/t/" + p.team.toLowerCase() + ".png" : null) : p.id > 0 ? "img/p/" + p.id + ".png" : null;
  const wrap = el("span", { class: "av " + posClass(opts.slot || p.position, p.position) + (dst ? " logo-av" : "") + (opts.small ? " sm" : "") },
    el("span", { class: "initials", text: initials }));
  const tc = TEAM_COLORS[p.team];
  if (tc && !dst) wrap.style.background = `radial-gradient(120% 90% at 50% 0%, color-mix(in srgb, ${tc} 55%, #fff) 0%, ${tc} 55%, color-mix(in srgb, ${tc} 70%, #000) 100%)`;
  if (src) {
    const img = el("img", { src, alt: "", loading: "lazy", decoding: "async" });
    img.addEventListener("load", () => wrap.classList.add("has-img"));
    img.addEventListener("error", () => img.remove());
    wrap.append(img);
  }
  if (opts.badge) wrap.append(el("span", { class: "av-badge " + opts.badge.cls, text: opts.badge.text }));
  if (opts.slot) wrap.append(el("span", { class: "av-slot", text: opts.slot }));
  return wrap;
}

function shortInjury(s) {
  return { INJURY_RESERVE: "IR", QUESTIONABLE: "Q", DOUBTFUL: "D", OUT: "OUT", SUSPENSION: "SUSP", DAY_TO_DAY: "DTD" }[s] || s;
}

// --- data ---

// typing reports whether the user is writing a question; background
// refreshes wait so the keyboard and cursor don't jump.
const typing = () => !!(document.activeElement && document.activeElement.closest && document.activeElement.closest(".ask"));

async function load(showSpinner) {
  if (busy || (!showSpinner && typing())) return;
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

async function ask(item, question) {
  const st = asks[item.id];
  st.pending = question;
  st.draft = "";
  renderMoves();
  try {
    const r = await fetch(`api/moves/${item.id}/ask`, {
      method: "POST",
      headers: { "X-FFAgent-Request": "1", "Content-Type": "application/json" },
      body: JSON.stringify({ question }),
    });
    const body = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(body.error || r.statusText);
    const cur = (data.pending || []).find((i) => i.id === item.id) || item; // a refresh may have replaced it
    cur.thread = (cur.thread || []).concat([body]);
  } catch (e) {
    st.draft = question; // give the question back so it can be re-sent
    toast(e.message, true);
  } finally {
    st.pending = null;
    renderMoves();
  }
}

// --- views ---

function render() {
  const s = data.status;
  $("team").textContent = s.team || "ffagent";
  const chips = [];
  if (s.ready) {
    if (s.week) chips.push("Week " + s.week);
    if (s.record) chips.push(s.record);
    if (s.opponent) chips.push("vs " + s.opponent);
  } else {
    chips.push("Waiting for the first check…");
  }
  $("subtitle").replaceChildren(...chips.map((c) => el("span", { class: "chip", text: c })));

  const mode = $("mode");
  mode.hidden = false;
  mode.className = "pill " + (s.mode === "approve" ? "approve" : s.mode === "dry-run" ? "dry" : "auto");
  mode.textContent =
    s.mode === "approve" ? "Approve mode · nothing runs without your OK"
    : s.mode === "dry-run" ? "Dry run · logging only"
    : "Auto mode · moves run without asking";

  const health = $("health");
  const probs = s.health || [];
  health.hidden = probs.length === 0;
  health.replaceChildren(...(probs.length ? [
    el("b", { text: probs.length === 1 ? "Heads up: the agent found a data problem" : `Heads up: the agent found ${probs.length} data problems` }),
    el("ul", {}, probs.map((p) => el("li", { text: p }))),
  ] : []));

  const waiting = data.pending ? data.pending.filter((i) => i.status === "pending").length : 0;
  const badge = $("badge");
  badge.hidden = waiting === 0;
  badge.textContent = waiting;
  if (navigator.setAppBadge) (waiting ? navigator.setAppBadge(waiting) : navigator.clearAppBadge()).catch(() => {});

  renderMoves();
  renderMatchup();
  renderChat();
  renderRoster();
  renderHistory();
}

const SETUP_ASK = "cd ~/ffagent && sudo ./deploy/setup-ask.sh";

// askOff explains how to turn follow-up questions on.
function askOff() {
  return el("div", { class: "card empty ask-off" },
    el("div", { class: "glyph" }, icon("chat")),
    el("h2", { text: "Ask Claude about your team" }),
    el("p", { text: "Questions are turned off on your server. To turn them on, run this on the server (it asks for an Anthropic API key):" }),
    el("code", { class: "cmd", text: SETUP_ASK }),
    el("button", { class: "copy-btn", onclick: (e) => {
      navigator.clipboard?.writeText(SETUP_ASK).then(() => toast("Copied"), () => {});
      e.currentTarget.blur();
    } }, "Copy command"));
}

const SUGGESTIONS = [
  "Am I likely to win this week?",
  "Who should I start at FLEX?",
  "Which free agent is worth picking up?",
  "Why is the agent suggesting these moves?",
  "Who on my bench could I drop?",
];

async function askChat(question) {
  chat.pending = question;
  chat.draft = "";
  renderChat(true);
  try {
    const r = await fetch("api/ask", {
      method: "POST",
      headers: { "X-FFAgent-Request": "1", "Content-Type": "application/json" },
      body: JSON.stringify({ question }),
    });
    const body = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(body.error || r.statusText);
    data.chat = (data.chat || []).concat([body]);
  } catch (e) {
    chat.draft = question;
    toast(e.message, true);
  } finally {
    chat.pending = null;
    renderChat(true);
  }
}

function renderChat(scroll) {
  const box = $("ask");
  box.replaceChildren();
  if (!data.status.ask_enabled) return box.append(askOff());
  const thread = el("div", { class: "chat" },
    el("div", { class: "bubble bot" },
      el("b", { text: "Ask me anything about your team." }),
      "I answer from the agent's numbers: your lineup and matchup, your roster, moves waiting for you and the best players on the wire. I can't make moves, but I can explain them."));
  for (const qa of data.chat || []) {
    thread.append(el("div", { class: "bubble me", text: qa.question }));
    thread.append(el("div", { class: "bubble bot", text: qa.answer }), el("div", { class: "stamp", text: when(qa.at) }));
  }
  if (chat.pending) {
    thread.append(el("div", { class: "bubble me", text: chat.pending }));
    thread.append(el("div", { class: "bubble bot typing" }, el("span"), el("span"), el("span")));
  }
  box.append(thread);
  if (!(data.chat || []).length && !chat.pending) {
    box.append(el("div", { class: "suggest" }, SUGGESTIONS.map((q) =>
      el("button", { text: q, onclick: () => askChat(q) }))));
  }
  const input = el("textarea", { rows: "1", maxlength: "1000", "aria-label": "Your question", placeholder: "Ask about your team…", disabled: !!chat.pending });
  input.value = chat.draft;
  const send = el("button", { class: "send", "aria-label": "Send", disabled: !chat.draft.trim() || !!chat.pending },
    svg("svg", { viewBox: "0 0 24 24", "aria-hidden": "true" }, svg("path", { fill: "currentColor", d: "M3.4 20.4 21 12 3.4 3.6 3.4 10l12.6 2-12.6 2z" })));
  const grow = () => { input.style.height = "auto"; input.style.height = Math.min(140, input.scrollHeight) + "px"; };
  input.addEventListener("input", () => { chat.draft = input.value; send.disabled = !input.value.trim(); grow(); });
  input.addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.shiftKey && input.value.trim()) { e.preventDefault(); askChat(input.value.trim()); } });
  send.addEventListener("click", () => { const q = input.value.trim(); if (q) askChat(q); });
  box.append(el("div", { class: "ask composer" }, input, send));
  requestAnimationFrame(grow);
  if (scroll) requestAnimationFrame(() => window.scrollTo({ top: document.body.scrollHeight, behavior: "smooth" }));
}

// "Add Name (RB, MIA), drop Other (WR) — bid $5" -> parts, when it has that shape.
function parseSwap(summary) {
  const m = /^Add (.+?)(?:, drop (.+?))?(?: — bid \$(\d+))?$/.exec(summary || "");
  if (!m) return null;
  const split = (s) => {
    const p = /^(.*?) \((.*)\)$/.exec(s || "");
    return p ? { name: p[1], meta: p[2] } : { name: s, meta: "" };
  };
  return { add: split(m[1]), drop: m[2] ? split(m[2]) : null, bid: m[3] };
}

const KIND = {
  "ADD": ["add", "Free agent", "k-add"],
  "WAIVER-CLAIM": ["add", "Waiver claim", "k-add"],
  "IR-STASH": ["ir", "Injured reserve", "k-ir"],
  "IR-ACTIVATE": ["ir", "Back from IR", "k-ir"],
  "DROP": ["drop", "Drop", ""],
  "LINEUP": ["lineup", "Lineup", ""],
  "TRADE-OFFER": ["trade", "Trade offer", ""],
};

function renderMoves() {
  const box = $("moves");
  box.replaceChildren();
  const items = data.pending || [];
  if (!items.length) {
    box.append(empty("check", "You're all set", data.status.ready
      ? "When injury news or a better player shows up, proposed moves appear here with the reasoning. Last check " + (when(data.status.last_check) || "pending") + "."
      : "The agent hasn't finished its first check yet."));
    return;
  }
  box.append(title("Waiting for you", items.length === 1 ? "1 move" : items.length + " moves"));
  for (const it of items) {
    const [glyph, label, cls] = KIND[it.kind] || ["swap", it.label, ""];
    const approveBtn = el("button", { class: "btn-approve", text: "Approve" });
    const declineBtn = el("button", { class: "btn-decline", text: "Decline" });
    const both = [approveBtn, declineBtn];
    approveBtn.addEventListener("click", () => decide(it, true, both));
    declineBtn.addEventListener("click", () => decide(it, false, both));

    const swap = parseSwap(it.summary);
    const ids = it.players || [];
    const who = (part, id, sign) => {
      const [pos, team] = (part.meta || "").split(", ");
      return avatar({ id, name: part.name, position: pos, team }, { small: true, badge: { cls: sign === "+" ? "in" : "out", text: sign } });
    };
    const body = swap
      ? el("div", { class: "swap" },
          el("div", { class: "swap-row in" }, who(swap.add, ids[0], "+"),
            el("span", { class: "who", text: swap.add.name }), el("span", { class: "meta", text: swap.add.meta })),
          swap.drop ? el("div", { class: "swap-row out" }, who(swap.drop, ids[1], "−"),
            el("span", { class: "who", text: swap.drop.name }), el("span", { class: "meta", text: swap.drop.meta })) : null,
          swap.bid ? el("span", { class: "bid", text: "FAAB bid $" + swap.bid }) : null)
      : el("h3", { text: it.summary });

    const notes = [];
    if (it.kind === "ADD") notes.push(el("span", { text: "First come, first served" }));
    if (it.needs && it.needs.length) notes.push(el("span", { class: "needs", text: "Needs: " + it.needs.join("; ") }));

    box.append(el("article", { class: "card move " + cls },
      el("div", { class: "move-head" },
        el("span", { class: "kind" }, icon(glyph), label || it.label),
        when(it.expires) ? el("span", { class: "expires", text: "Expires " + when(it.expires) }) : null),
      body,
      it.reason ? el("p", { class: "why" }, el("b", { text: "Why: " }), it.reason) : null,
      notes.length ? el("div", { class: "note-row" }, notes) : null,
      it.status === "approved"
        ? el("p", { class: "waiting", text: "Approved: waiting on the move it depends on" })
        : el("div", { class: "actions" }, declineBtn, approveBtn),
      renderAsk(it)));
  }
}

function renderAsk(it) {
  const thread = it.thread || [];
  if (!data.status.ask_enabled && !thread.length) {
    return el("div", { class: "ask" }, el("button", { class: "ask-open", onclick: () => show("ask") },
      icon("chat"), "Questions about this move? Turn them on"));
  }
  const st = (asks[it.id] ||= { open: false, draft: "", pending: null });
  const box = el("div", { class: "ask" });
  for (const qa of thread) {
    box.append(el("div", { class: "qa" },
      el("p", { class: "q", text: qa.question }),
      el("p", { class: "a", text: qa.answer }),
      el("p", { class: "when", text: [when(qa.at), qa.via === "email" ? "asked by email" : ""].filter(Boolean).join(" · ") })));
  }
  if (st.pending) {
    box.append(el("div", { class: "qa" },
      el("p", { class: "q", text: st.pending }),
      el("p", { class: "a thinking", text: "Thinking…" })));
  }
  if (!data.status.ask_enabled) return box;
  if (!st.open && !st.pending) {
    box.append(el("button", { class: "ask-open",
      onclick: () => { st.open = true; renderMoves(); const t = document.querySelector(`[data-ask="${it.id}"]`); if (t) t.focus(); } },
      icon("chat"), thread.length ? "Ask another question" : "Ask about this move"));
    return box;
  }
  if (st.pending) return box;
  const input = el("textarea", { rows: "2", maxlength: "1000", "data-ask": it.id, "aria-label": "Your question",
    placeholder: "e.g. Why drop him instead of my other WR?" });
  input.value = st.draft;
  const send = el("button", { class: "ask-send", text: "Ask", disabled: !st.draft.trim() });
  input.addEventListener("input", () => { st.draft = input.value; send.disabled = !input.value.trim(); });
  send.addEventListener("click", () => { const q = input.value.trim(); if (q) ask(it, q); });
  const cancel = el("button", { class: "ask-cancel", text: "Cancel", onclick: () => { st.open = false; st.draft = ""; renderMoves(); } });
  box.append(el("div", { class: "ask-form" }, input, el("div", { class: "ask-row" },
    el("span", { class: "muted", text: "Answered by Claude from the agent's data" }), cancel, send)));
  return box;
}

function ring(p) {
  const r = 34, c = 2 * Math.PI * r;
  const fill = svg("circle", { class: "fill", cx: "42", cy: "42", r, fill: "none", "stroke-width": "8",
    "stroke-dasharray": c.toFixed(2), "stroke-dashoffset": (c * (1 - Math.max(0, Math.min(1, p)))).toFixed(2) });
  return el("div", { class: "ring", role: "img", "aria-label": "Win probability " + pct(p) },
    svg("svg", { viewBox: "0 0 84 84" },
      svg("circle", { class: "track", cx: "42", cy: "42", r, fill: "none", "stroke-width": "8" }), fill),
    el("div", { class: "label" }, el("div", {}, el("b", { text: pct(p) }), el("span", { text: "to win" }))));
}

// Pairs each of my starters with the opponent's starter in the same slot.
function pairLineups(mine, theirs) {
  const pool = {};
  for (const p of theirs) (pool[p.slot] ||= []).push(p);
  const rows = mine.map((p) => [p, (pool[p.slot] || []).shift() || null]);
  for (const rest of Object.values(pool)) for (const p of rest) rows.push([null, p]);
  return rows;
}

// "B. Allen", the way ESPN fits names next to photos; "Jaguars" for a D/ST.
function shortName(p) {
  if (p.position === "D/ST") return (p.name || "").replace(/ D\/ST$/, "");
  const parts = (p.name || "").split(" ");
  if (parts.length < 2) return p.name || "";
  return parts[0][0] + ". " + parts.slice(1).join(" ");
}

function playerCell(p, other, side) {
  if (!p) return el("div", { class: "pl " + side }, el("span", { class: "empty-slot", text: "Empty" }));
  const final = p.locked && p.left <= 0;
  const live = p.locked && !final;
  const status = final ? el("span", { class: "tag final", text: "FINAL" }) : live ? el("span", { class: "tag live", text: "LIVE" }) : null;
  // Highlight whoever is projected to win the slot: on the big number before
  // kickoff, on the projection once points are coming in.
  const winning = other && p.proj > other.proj + 0.05;
  const info = el("div", { class: "pinfo" },
    el("span", { class: "pname", text: shortName(p) }),
    el("span", { class: "sub" }, teamLogo(p.team), p.team || "", status || (p.opp ? el("span", { class: "game", text: gameLine(p) }) : null)),
    el("span", { class: "pts" },
      el("b", { text: fmt1(p.locked ? p.actual : p.proj) }),
      el("small", { text: p.locked ? (final ? "" : "proj " + fmt1(p.proj)) : "proj" })),
    live ? el("div", { class: "progress" }, el("div", { style: { width: Math.round((1 - p.left) * 100) + "%" } })) : null);
  return el("div", { class: "pl " + side + (winning ? (p.locked ? " win-live" : " win-proj") : "") },
    avatar(p, { small: true }), info);
}

function renderMatchup() {
  const box = $("matchup");
  box.replaceChildren();
  const s = data.status;
  if (!s.ready) return box.append(empty("trophy", "No matchup yet", "Waiting for the agent's first check."));
  const mine = data.lineup || [], theirs = data.opp_lineup || [];
  const started = mine.some((p) => p.locked) || theirs.some((p) => p.locked);

  if (s.opponent) {
    const side = (key, id, name, record, score, proj, cls) => {
      const num = el("div", { class: "score" });
      countTo(num, key, started ? score : proj);
      return el("div", { class: "side " + cls },
        fantasyLogo(id, name),
        el("div", { class: "tname", text: name }),
        record ? el("div", { class: "trec", text: record }) : null,
        num,
        el("div", { class: "proj", text: started ? "Proj " + fmt1(proj) : "Projected" }));
    };
    const total = Math.max(1, (s.my_projected || 0) + (s.opp_projected || 0));
    const margin = (s.my_projected || 0) - (s.opp_projected || 0);
    box.append(el("div", { class: "card hero" },
      el("div", { class: "hero-teams" },
        side("me", s.my_team_id, s.team, s.record, s.my_actual, s.my_projected, "left"),
        ring(s.win_prob),
        side("opp", s.opp_team_id, s.opponent, s.opp_record, s.opp_actual, s.opp_projected, "right")),
      el("div", { class: "margin" },
        el("div", { class: "margin-bar" }, el("div", { style: { width: (100 * (s.my_projected || 0) / total).toFixed(1) + "%" } })),
        el("div", { class: "margin-text" },
          el("span", { text: margin >= 0 ? "Projected to win by " + fmt1(margin) : "Projected to lose by " + fmt1(-margin) }),
          started ? el("span", { class: "live-dot", text: "Live" }) : null)),
      s.strategy ? el("p", { class: "hero-strategy", text: "Strategy: " + s.strategy }) : null));
  }

  if (mine.length) {
    box.append(title(s.opponent ? "Head to head" : "Recommended lineup", s.opponent ? "you · " + s.opponent : ""));
    const ul = el("ul", { class: "h2h" });
    const rows = s.opponent ? pairLineups(mine, theirs) : mine.map((p) => [p, null]);
    for (const [a, b] of rows) {
      const slot = (a || b).slot, pos = (a || b).position;
      ul.append(el("li", {},
        playerCell(a, b, "me"),
        slotChip(slot, pos),
        s.opponent ? playerCell(b, a, "opp") : el("span")));
    }
    box.append(el("div", { class: "card flush" }, ul));
  }
  box.append(el("p", { class: "footnote", text: "Your side is the agent's recommended lineup; your opponent's is as set on ESPN. Updated " + (when(s.planned_at) || "—") + "." }));
}

function renderRoster() {
  const box = $("roster");
  box.replaceChildren();
  const rows = data.roster || [];
  if (!rows.length) return box.append(empty("people", "No roster yet", "Waiting for the agent's first check."));
  const max = Math.max(1, ...rows.map((p) => p.proj || 0));
  const groups = [
    ["Starters", rows.filter((p) => p.slot !== "BE" && p.slot !== "IR")],
    ["Bench", rows.filter((p) => p.slot === "BE")],
    ["Injured reserve", rows.filter((p) => p.slot === "IR")],
  ];
  for (const [name, ps] of groups) {
    if (!ps.length) continue;
    const ul = el("ul", { class: "list" });
    for (const p of ps) {
      const acc = p.accuracy ? ` · ${pct(p.accuracy)} of ESPN proj (${p.acc_weeks} wk${p.acc_weeks === 1 ? "" : "s"})` : "";
      const notes = (p.notes || []).filter((n) => n !== p.injury);
      const differs = Math.abs((p.espn || 0) - (p.proj || 0)) > 0.05;
      ul.append(el("li", {},
        avatar(p, { slot: p.slot }),
        el("div", { class: "grow" },
          el("div", { class: "nm" }, p.name, p.injury ? el("span", { class: "tag inj", text: shortInjury(p.injury) }) : null),
          el("div", { class: "sub game-line" }, teamLogo(p.team), el("span", { class: "team-txt", text: `${p.position} · ${p.team}` }), p.opp ? el("span", { class: "game", text: gameLine(p) }) : null),
          el("div", { class: "sub", text: `avg ${fmt1(p.avg)} · last 3 ${fmt1(p.last3)}${acc}` }),
          notes.length ? el("div", { class: "note", text: notes.join(" · ") }) : null,
          el("div", { class: "bar " + posClass(p.slot, p.position) }, el("div", { style: { width: Math.round(100 * (p.proj || 0) / max) + "%" } }))),
        el("div", { class: "val" },
          el("b", { text: fmt1(p.proj) }),
          differs ? el("small", { text: "ESPN " + fmt1(p.espn) }) : null,
          el("small", { text: Math.round(p.ros) + " ROS" }))));
    }
    box.append(title(name, ps.length + ""), el("div", { class: "card flush" }, ul));
  }
  box.append(el("p", { class: "footnote", text: "This week's projections are ESPN's, updated for injury news ESPN hasn't caught up on. ROS = rest of season." }));
}

function renderHistory() {
  const box = $("history");
  box.replaceChildren();
  const items = data.history || [];
  if (!items.length) return box.append(empty("clock", "Nothing yet", "Moves you approve or decline, and what happened to them, show up here."));
  const labels = { executed: "Done", declined: "Declined", skipped: "Not run", failed: "Failed", expired: "Expired", superseded: "Replaced" };
  const ul = el("ul", { class: "list timeline" });
  for (const it of items) {
    ul.append(el("li", {},
      el("span", { class: "dot " + it.status }),
      el("div", { class: "grow" },
        el("div", { class: "nm", text: it.summary }),
        el("div", { class: "sub", text: [when(it.created), it.result && it.result !== "done" && it.result !== "declined by you" ? it.result : "", it.via === "auto" ? "approved automatically (ruled-out starter)" : it.via ? "decided in " + (it.via === "app" ? "the app" : "email") : ""].filter(Boolean).join(" · ") })),
      el("span", { class: "status " + it.status, text: labels[it.status] || it.status })));
  }
  box.append(title("Last 7 days", items.length + ""), el("div", { class: "card flush" }, ul));
}

// --- notifications ---

// Web Push needs a service worker, and on iPhone the app must be opened
// from the Home Screen (iOS 16.4+).
const push = { key: "", sub: null, reg: null, granted: false };
const pushOn = () => !!push.sub && pushSupported() && (push.granted || Notification.permission === "granted");
const pushSupported = () => "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;

function b64url(s) {
  const raw = atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4));
  return Uint8Array.from(raw, (c) => c.charCodeAt(0));
}

async function pushPost(action, sub) {
  const r = await fetch("api/push/" + action, {
    method: "POST",
    headers: { "X-FFAgent-Request": "1", "Content-Type": "application/json" },
    body: JSON.stringify(sub),
  });
  const body = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(body.error || r.statusText);
}

function renderBell() {
  const bell = $("bell");
  const on = pushOn();
  bell.setAttribute("aria-pressed", on);
  bell.setAttribute("aria-label", on ? "Notifications are on" : "Turn on notifications");
}

async function initPush() {
  const bell = $("bell");
  try {
    const r = await fetch("api/push", { cache: "no-store" });
    const info = r.ok ? await r.json() : {};
    if (!info.enabled) return;
    push.key = info.key;
  } catch (_) { return; }
  bell.hidden = false;
  bell.addEventListener("click", togglePush);
  if (!pushSupported()) return renderBell();
  push.reg = await navigator.serviceWorker.register("sw.js").catch(() => null);
  if (!push.reg) return;
  push.sub = await push.reg.pushManager.getSubscription().catch(() => null);
  // A key change (or a lost server store) means subscribing afresh.
  if (push.sub && push.sub.options && push.sub.options.applicationServerKey) {
    const have = new Uint8Array(push.sub.options.applicationServerKey).join();
    if (have !== b64url(push.key).join()) { await push.sub.unsubscribe().catch(() => {}); push.sub = null; }
  }
  if (push.sub && Notification.permission === "granted") pushPost("subscribe", push.sub).catch(() => {});
  renderBell();
}

async function togglePush() {
  if (!pushSupported()) {
    return toast(/iPhone|iPad/.test(navigator.userAgent) && !navigator.standalone
      ? "To get notifications, open ffagent from your Home Screen (Share › Add to Home Screen)."
      : "This browser can't receive notifications.", true);
  }
  const bell = $("bell");
  const reg = push.reg;
  if (!reg) return toast("Notifications aren't ready yet; try again in a moment.", true);
  bell.disabled = true;
  try {
    if (pushOn()) {
      if (!confirm("Turn off notifications on this device?")) return;
      await pushPost("unsubscribe", push.sub).catch(() => {});
      await push.sub.unsubscribe().catch(() => {});
      push.sub = null;
      toast("Notifications off.");
      return;
    }
    push.granted = await Notification.requestPermission() === "granted";
    if (!push.granted) {
      return toast("Notifications are blocked. Allow them for ffagent in Settings › Notifications.", true);
    }
    push.sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64url(push.key) });
    await pushPost("subscribe", push.sub);
    toast("Notifications on. You'll get one when a move needs your OK.");
  } catch (e) {
    toast("Couldn't turn on notifications: " + e.message, true);
  } finally {
    bell.disabled = false;
    renderBell();
  }
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

// tabFromHash opens a tab linked from a notification, like "#moves".
function tabFromHash() {
  const t = location.hash.slice(1);
  return document.querySelector(`.tabbar button[data-tab="${CSS.escape(t)}"]`) ? t : "";
}

function skeletons() {
  for (const id of ["moves", "matchup", "ask", "roster", "history"]) {
    $(id).replaceChildren(el("div", { class: "skeleton" }), el("div", { class: "skeleton short" }), el("div", { class: "skeleton short" }));
  }
}

// Pull down at the top of the page to refresh, as in native apps.
function pullToRefresh() {
  const ptr = $("ptr");
  let y0 = null, dy = 0;
  window.addEventListener("touchstart", (e) => { y0 = window.scrollY <= 0 && !typing() ? e.touches[0].clientY : null; dy = 0; }, { passive: true });
  window.addEventListener("touchmove", (e) => {
    if (y0 === null) return;
    dy = Math.max(0, e.touches[0].clientY - y0);
    const pull = Math.min(90, dy * 0.5);
    ptr.style.transform = `translate(-50%, ${pull - 50}px) rotate(${dy * 2}deg)`;
    ptr.style.opacity = String(Math.min(1, dy / 100));
    ptr.classList.toggle("ready", dy > 140);
  }, { passive: true });
  window.addEventListener("touchend", () => {
    if (y0 !== null && dy > 140) load(true);
    y0 = null;
    ptr.style.transform = "";
    ptr.style.opacity = "";
    ptr.classList.remove("ready");
  });
}

document.addEventListener("DOMContentLoaded", () => {
  pullToRefresh();
  for (const b of document.querySelectorAll(".tabbar button")) b.addEventListener("click", () => show(b.dataset.tab));
  $("refresh").addEventListener("click", () => load(true));
  let saved = "moves";
  try { saved = localStorage.getItem("tab") || "moves"; } catch (_) {}
  show(tabFromHash() || saved);
  window.addEventListener("hashchange", () => { const t = tabFromHash(); if (t) { show(t); load(false); } });
  skeletons();
  load(true);
  initPush();
  setInterval(() => { if (document.visibilityState === "visible") load(false); }, 30000);
  document.addEventListener("visibilitychange", () => { if (document.visibilityState === "visible") load(false); });
});
