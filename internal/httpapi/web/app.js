"use strict";

const $ = (sel) => document.querySelector(sel);

// Build DOM with textContent only, so nothing from a response is ever parsed as HTML.
function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

const rupees = (n) => "₹" + Number(n).toLocaleString("en-IN");

// Times arrive as "2026-09-26T19:30:00+05:30"; the clock part is already local to the venue.
const clock = (iso) => iso.slice(11, 16);

const recent = [];

function today() {
  const d = new Date();
  const pad = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

async function planEvening(event) {
  event.preventDefault();
  const button = $("#go");
  button.disabled = true;

  const body = {
    date: $("#date").value,
    area: $("#area").value,
    party_size: Number($("#party").value),
    budget_per_person: Number($("#budget").value),
  };

  const started = performance.now();
  try {
    const res = await fetch("/v1/plan", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    const roundTrip = performance.now() - started;
    const data = await res.json();

    if (!res.ok) {
      showError(res.status, data.error || "request failed", roundTrip);
    } else {
      showResult(data, roundTrip);
    }
  } catch (err) {
    showError(0, "could not reach the server", performance.now() - started);
  } finally {
    button.disabled = false;
  }
}

function clearResults() {
  $("#plans").replaceChildren();
  $("#empty").hidden = true;
  const status = $("#status");
  status.replaceChildren();
  status.hidden = false;
  return status;
}

function showError(code, message, ms) {
  const status = clearResults();
  status.append(el("span", "badge bad", code ? `Error ${code}` : "Error"), el("span", "meta", message));
  record(ms, "error");
}

function showResult(data, ms) {
  const status = clearResults();

  status.append(
    el("span", data.degraded ? "badge warn" : "badge ok", data.degraded ? "Degraded" : "All sources answered"),
    el("span", "meta", `${Math.round(ms)} ms round trip`),
    el("span", "meta", `${data.elapsed_ms.toFixed(1)} ms in the service`),
    el("span", "meta", `${data.considered.toLocaleString()} feasible plans considered`),
  );

  if (data.degraded) {
    const counts = new Map();
    for (const f of data.failures) {
      const key = `${f.source}: ${f.reason}`;
      counts.set(key, (counts.get(key) || 0) + 1);
    }
    const why = [...counts].map(([k, n]) => (n > 1 ? `${k} ×${n}` : k)).join(", ");
    status.append(el("span", "why", `Answered with partial data (${why}).`));
  }

  const plans = $("#plans");
  if (data.plans.length === 0) {
    plans.append(el("p", "empty", data.degraded
      ? "No plans could be built from the data that arrived in time."
      : "No plan fits that party, budget and time. Try a higher budget or a smaller party."));
  }
  data.plans.forEach((plan, i) => plans.append(planCard(plan, i + 1)));

  record(ms, data.degraded ? "degraded" : "ok");
}

function planCard(plan, n) {
  const card = el("article", "card");

  const head = el("header");
  head.append(
    el("strong", "", `Plan ${n} · ${clock(plan.start)}–${clock(plan.end)}`),
    el("span", "", `${rupees(plan.cost_per_person)} per person · ${Math.round(plan.score * 100)}% match`),
  );
  card.append(head);

  for (const leg of plan.legs) {
    const row = el("div", "leg");
    row.append(el("div", "when", `${clock(leg.start)}–${clock(leg.end)}`));

    const detail = el("div");
    detail.append(
      el("div", `kind ${leg.kind}`, leg.kind === "restaurant" ? "Dinner" : "Film"),
      el("div", "venue", leg.kind === "restaurant" ? leg.venue : `${leg.title}`),
    );

    const sub = [];
    if (leg.kind === "cinema") sub.push(leg.venue);
    sub.push(`${rupees(leg.cost_per_person)} pp`);
    detail.append(el("div", "sub", sub.join(" · ")));

    if (leg.travel_minutes > 0) {
      const travel = el("div", "sub", `${leg.travel_minutes} min travel from the previous stop`);
      if (leg.travel_estimated) {
        travel.append(el("span", "est", " (estimated: the travel service did not answer in time)"));
      }
      detail.append(travel);
    }
    row.append(detail);
    card.append(row);
  }
  return card;
}

function record(ms, outcome) {
  recent.unshift({ ms, outcome });
  recent.length = Math.min(recent.length, 8);

  const list = $("#log");
  list.replaceChildren();
  for (const r of recent) {
    const li = el("li", r.outcome === "ok" ? "" : "slow");
    li.append(el("span", "", r.outcome), el("span", "", `${Math.round(r.ms)} ms`));
    list.append(li);
  }
  $("#log-wrap").hidden = false;
}

// ---- fault controls (only shown when the server has ENABLE_ADMIN set) ----

const PROVIDERS = [
  ["showtimes", "Showtimes service"],
  ["tables", "Restaurant tables service"],
  ["travel", "Travel time service"],
];
const STATES = [
  ["Healthy", {}],
  ["Slow (2 s)", { latency_ms: 2000 }],
  ["Down", { down: true }],
];

function stateOf(cfg) {
  if (cfg.down) return "Down";
  if (cfg.latency_ms >= 1000) return "Slow (2 s)";
  return "Healthy";
}

async function initFaults() {
  let features;
  try {
    features = await (await fetch("/v1/features")).json();
  } catch {
    return;
  }
  if (!features.admin) return; // leave the panel hidden

  try {
    const res = await fetch("/admin/faults");
    if (!res.ok) return;
    renderFaults(await res.json());
    $("#faults").hidden = false;
  } catch { /* leave hidden */ }
}

function renderFaults(configs) {
  const rows = $("#fault-rows");
  rows.replaceChildren();

  for (const [key, label] of PROVIDERS) {
    const current = stateOf(configs[key] || {});
    const row = el("div", "fault-row");
    row.append(el("div", "name", label));

    const seg = el("div", "seg");
    seg.setAttribute("role", "group");
    seg.setAttribute("aria-label", label);
    for (const [name, cfg] of STATES) {
      const b = el("button", "", name);
      b.type = "button";
      b.setAttribute("aria-pressed", String(name === current));
      b.addEventListener("click", () => setFault(key, cfg));
      seg.append(b);
    }
    row.append(seg);
    rows.append(row);
  }
}

async function setFault(provider, cfg) {
  await fetch(`/admin/faults/${provider}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(cfg),
  });
  const res = await fetch("/admin/faults");
  if (res.ok) renderFaults(await res.json());
}

async function resetCache() {
  const button = $("#reset-cache");
  button.disabled = true;
  let message = "Could not clear the cache";
  try {
    const res = await fetch("/admin/cache/reset", { method: "POST" });
    message = res.ok ? "Cache cleared" : res.status === 404 ? "The cache is turned off" : message;
  } catch { /* keep the default message */ }

  button.textContent = message;
  setTimeout(() => { button.textContent = "Clear the cache"; button.disabled = false; }, 1500);
}

$("#date").value = today();
$("#plan-form").addEventListener("submit", planEvening);
$("#reset-cache").addEventListener("click", resetCache);
initFaults();
