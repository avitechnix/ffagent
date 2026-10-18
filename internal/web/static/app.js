"use strict";

const $ = (id) => document.getElementById(id);
let data = null;
let busy = false;
// Follow-up question state per move id, kept across re-renders.
const asks = {}; // id -> { open, draft, pending }

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

// Player photo (team logo for a D/ST), served by the agent from ESPN's CDN.
// Initials show until it loads, and stay if ESPN has no photo.
function avatar(p, opts = {}) {
  const initials = (p.name || "?").replace(/ D\/ST$/, "").split(/\s+/).map((w) => w[0]).slice(0, 2).join("").toUpperCase();
  const dst = p.position === "D/ST" || p.id < 0;
  const src = dst ? (p.team ? "img/t/" + p.team.toLowerCase() + ".png" : null) : p.id > 0 ? "img/p/" + p.id + ".png" : null;
  const wrap = el("span", { class: "av " + posClass(opts.slot || p.position, p.position) + (dst ? " logo-av" : "") + (opts.small ? " sm" : "") },
    el("span", { class: "initials", text: initials }));
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
  if (!data.status.ask_enabled && !thread.length) return null;
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
    el("span", { class: "sub" }, p.team || "", status),
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
    const side = (name, score, proj, cls) => el("div", { class: "side " + cls },
      el("div", { class: "tname", text: name }),
      el("div", { class: "score", text: fmt1(started ? score : proj) }),
      el("div", { class: "proj", text: started ? "Proj " + fmt1(proj) : "Projected" }));
    box.append(el("div", { class: "card hero" },
      el("div", { class: "hero-teams" },
        side(s.team, s.my_actual, s.my_projected, "left"),
        ring(s.win_prob),
        side(s.opponent, s.opp_actual, s.opp_projected, "right")),
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
          el("div", { class: "sub", text: `${p.position} ${p.team} · avg ${fmt1(p.avg)} · last 3 ${fmt1(p.last3)}${acc}` }),
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
        el("div", { class: "sub", text: [when(it.created), it.result && it.result !== "done" && it.result !== "declined by you" ? it.result : "", it.via ? "decided in " + (it.via === "app" ? "the app" : "email") : ""].filter(Boolean).join(" · ") })),
      el("span", { class: "status " + it.status, text: labels[it.status] || it.status })));
  }
  box.append(title("Last 7 days", items.length + ""), el("div", { class: "card flush" }, ul));
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

function skeletons() {
  for (const id of ["moves", "matchup", "roster", "history"]) {
    $(id).replaceChildren(el("div", { class: "skeleton" }), el("div", { class: "skeleton short" }), el("div", { class: "skeleton short" }));
  }
}

document.addEventListener("DOMContentLoaded", () => {
  for (const b of document.querySelectorAll(".tabbar button")) b.addEventListener("click", () => show(b.dataset.tab));
  $("refresh").addEventListener("click", () => load(true));
  let saved = "moves";
  try { saved = localStorage.getItem("tab") || "moves"; } catch (_) {}
  show(saved);
  skeletons();
  load(true);
  setInterval(() => { if (document.visibilityState === "visible") load(false); }, 30000);
  document.addEventListener("visibilitychange", () => { if (document.visibilityState === "visible") load(false); });
});
