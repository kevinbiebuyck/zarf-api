/* zarf-api UI — dependency-free SPA talking to /api/v1. */
"use strict";

// Derive the API base from the page URL so the UI works under any
// ZARF_API_BASE_PATH: the UI is always served at <base>/ui/.
const BASE = location.pathname.replace(/\/ui\/?$/, "");
const API = BASE + "/api/v1";

// ---------- tiny helpers ----------

const $ = (sel) => document.querySelector(sel);
const el = (tag, cls, text) => {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
};
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) =>
  ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(API + path, opts);
  if (res.status === 204) return null;
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = { error: text }; }
  if (!res.ok) throw new Error((data && data.error) || `HTTP ${res.status}`);
  return data;
}

function toast(msg, kind = "") {
  const t = el("div", `toast ${kind}`);
  t.append(el("div", "msg", msg));
  $("#toasts").append(t);
  setTimeout(() => t.remove(), kind === "err" ? 8000 : 4000);
}

function fmtBytes(n) {
  if (n < 1024) return n + " B";
  const units = ["KiB", "MiB", "GiB"];
  let v = n;
  let u = -1;
  do { v /= 1024; u++; } while (v >= 1024 && u < units.length - 1);
  return v.toFixed(1) + " " + units[u];
}

function fmtTime(iso) {
  if (!iso) return "—";
  return new Date(iso).toLocaleString();
}

// Compare version strings: numeric-aware, semver-ish. Returns <0, 0, >0.
function cmpVersion(a, b) {
  const pa = String(a || "").split(/[.\-+]/).map((x) => (/^\d+$/.test(x) ? Number(x) : x));
  const pb = String(b || "").split(/[.\-+]/).map((x) => (/^\d+$/.test(x) ? Number(x) : x));
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const x = pa[i] ?? 0, y = pb[i] ?? 0;
    if (x === y) continue;
    if (typeof x === "number" && typeof y === "number") return x - y;
    return String(x).localeCompare(String(y));
  }
  return 0;
}

// ---------- modal ----------

function openModal(title, bodyNode, buttons) {
  $("#modal-title").textContent = title;
  const body = $("#modal-body");
  body.innerHTML = "";
  body.append(bodyNode);
  const foot = $("#modal-foot");
  foot.innerHTML = "";
  for (const b of buttons) foot.append(b);
  $("#modal-backdrop").classList.remove("hidden");
}
function closeModal() { $("#modal-backdrop").classList.add("hidden"); }

function confirmModal(title, message, confirmLabel, onConfirm) {
  const body = el("div");
  body.append(el("p", "", message));
  const ok = el("button", "btn danger", confirmLabel);
  ok.onclick = async () => { closeModal(); await onConfirm(); };
  const cancel = el("button", "btn", "Cancel");
  cancel.onclick = closeModal;
  openModal(title, body, [cancel, ok]);
}

// ---------- tabs (hash routing: #/packages, #/installed, #/jobs) ----------

const TABS = ["packages", "installed", "jobs"];
let activeTab = "packages";

function tabFromHash() {
  const name = location.hash.replace(/^#\/?/, "");
  return TABS.includes(name) ? name : "packages";
}

function applyRoute() {
  activeTab = tabFromHash();
  document.querySelectorAll(".tab").forEach((b) => b.classList.toggle("active", b.dataset.tab === activeTab));
  document.querySelectorAll(".tab-panel").forEach((p) => p.classList.add("hidden"));
  $("#tab-" + activeTab).classList.remove("hidden");
  refreshActive();
}

document.querySelectorAll(".tab").forEach((btn) => {
  btn.onclick = () => { location.hash = "/" + btn.dataset.tab; };
});
window.addEventListener("hashchange", applyRoute);

function refreshActive() {
  if (activeTab === "packages") loadPackages();
  if (activeTab === "installed") loadInstalled();
  if (activeTab === "jobs") loadJobs();
}

// ---------- packages ----------

function groupByName(packages) {
  const groups = new Map();
  for (const p of packages) {
    if (!groups.has(p.name)) groups.set(p.name, []);
    groups.get(p.name).push(p);
  }
  for (const list of groups.values()) list.sort((a, b) => -cmpVersion(a.version, b.version));
  return [...groups.entries()].sort((a, b) => a[0].localeCompare(b[0]));
}

async function loadPackages() {
  const root = $("#packages-list");
  try {
    const data = await api("GET", "/packages");
    const packages = data.packages || [];
    root.innerHTML = "";
    if (packages.length === 0) {
      root.append(el("div", "empty", "No packages imported yet — upload one above."));
      return;
    }
    for (const [name, versions] of groupByName(packages)) {
      const card = el("div", "card");
      const head = el("div", "app-head");
      head.append(el("h3", "", name));
      head.append(el("span", "muted", `${versions.length} version${versions.length > 1 ? "s" : ""}`));
      if (versions[0].description) head.append(el("span", "muted", "— " + versions[0].description));
      card.append(head);

      const table = el("table");
      table.innerHTML = `<thead><tr>
        <th>Version</th><th>Arch</th><th>Size</th><th>Imported</th><th></th>
      </tr></thead>`;
      const tbody = el("tbody");
      for (const p of versions) {
        const tr = el("tr");
        tr.insertAdjacentHTML("beforeend", `
          <td class="mono">${esc(p.version || "—")}</td>
          <td>${esc(p.architecture || "—")}</td>
          <td>${fmtBytes(p.size)}</td>
          <td>${fmtTime(p.importedAt)}</td>
          <td class="actions"></td>`);
        const actions = tr.lastElementChild;

        const install = el("button", "btn small primary", "Install");
        install.onclick = () => openInstallModal(p);
        const del = el("button", "btn small", "Delete");
        del.onclick = () => confirmModal(
          "Delete package",
          `Delete ${p.id} from the local store? This does not uninstall anything from the cluster.`,
          "Delete",
          async () => {
            try {
              await api("DELETE", "/packages/" + encodeURIComponent(p.id));
              toast(`Deleted ${p.id}`, "ok");
              loadPackages();
            } catch (e) { toast(e.message, "err"); }
          });
        actions.append(install, del);
        tbody.append(tr);
      }
      table.append(tbody);
      card.append(table);
      root.append(card);
    }
  } catch (e) {
    root.innerHTML = `<div class="empty">Failed to load packages: ${esc(e.message)}</div>`;
  }
}

// ---------- chunked upload ----------

const CHUNK_SIZE = 8 * 1024 * 1024;
const UPLOAD_LS_KEY = "zarf-api-upload";
let uploadInProgress = false;
let currentXhr = null;
let pauseRequested = false;
let pendingResume = null; // { state, session } waiting for the user to re-pick the file

function saveUploadState(s) { try { localStorage.setItem(UPLOAD_LS_KEY, JSON.stringify(s)); } catch { /* ignore */ } }
function loadUploadState() { try { return JSON.parse(localStorage.getItem(UPLOAD_LS_KEY)); } catch { return null; } }
function clearUploadState() { try { localStorage.removeItem(UPLOAD_LS_KEY); } catch { /* ignore */ } }

// fetch() cannot report upload progress; XHR can.
function putChunk(url, blob, onProgress) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    currentXhr = xhr;
    xhr.open("PUT", url);
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) onProgress(e.loaded / e.total);
    };
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) { resolve(); return; }
      let msg = `HTTP ${xhr.status}`;
      try { msg = JSON.parse(xhr.responseText).error || msg; } catch { /* keep default */ }
      reject(new Error(msg));
    };
    xhr.onerror = () => reject(new Error("network error"));
    xhr.onabort = () => reject(new Error("paused"));
    xhr.send(blob);
  });
}

// uploadPackage uploads file in chunks. With resumeSession, chunks the
// server already has (at the expected size) are skipped.
async function uploadPackage(file, resumeSession = null) {
  if (uploadInProgress) {
    toast("An upload is already in progress", "err");
    return;
  }
  uploadInProgress = true;
  pauseRequested = false;
  const dz = $("#dropzone");
  const wrap = $("#upload-progress");
  const bar = wrap.querySelector(".bar");
  const prog = wrap.querySelector(".progress");
  const nameEl = wrap.querySelector(".name");
  const statsEl = wrap.querySelector(".stats");
  const setProgress = (uploaded, total) => {
    const f = total ? uploaded / total : 0;
    bar.style.width = (f * 100).toFixed(1) + "%";
    statsEl.textContent = `${fmtBytes(uploaded)} / ${fmtBytes(total)} — ${(f * 100).toFixed(0)}%`;
  };
  dz.classList.add("busy");
  wrap.classList.remove("hidden");
  prog.classList.remove("indet");
  nameEl.textContent = file.name;

  const totalChunks = Math.max(1, Math.ceil(file.size / CHUNK_SIZE));
  const chunkSize = (i) => Math.min(CHUNK_SIZE, file.size - i * CHUNK_SIZE);
  let session = resumeSession;
  let startIndex = 0;
  let uploaded = 0;

  try {
    if (session) {
      // Resume: skip contiguous chunks the server already has complete.
      const have = session.chunks || {};
      for (let i = 0; i < totalChunks; i++) {
        if (have[i] === chunkSize(i)) { uploaded += chunkSize(i); startIndex = i + 1; } else break;
      }
    } else {
      // A new upload wipes any other unfinished sessions, server-side and local.
      try {
        const list = await api("GET", "/uploads");
        for (const u of list.uploads || []) {
          await api("DELETE", "/uploads/" + u.id).catch(() => {});
        }
      } catch { /* best effort */ }
      clearUploadState();
      hideResumeBanner();
      session = await api("POST", "/uploads", { fileName: file.name });
      saveUploadState({ sessionId: session.id, fileName: file.name, size: file.size });
    }

    setProgress(uploaded, file.size);
    for (let i = startIndex; i < totalChunks; i++) {
      const blob = file.slice(i * CHUNK_SIZE, (i + 1) * CHUNK_SIZE);
      await putChunk(`${API}/uploads/${session.id}/chunks/${i}`, blob,
        (f) => setProgress(uploaded + f * blob.size, file.size));
      uploaded += blob.size;
      setProgress(uploaded, file.size);
    }
    // Server-side validation + import: indeterminate phase.
    prog.classList.add("indet");
    statsEl.textContent = "validating with zarf…";
    const pkg = await api("POST", `/uploads/${session.id}/complete`);
    bar.style.width = "100%";
    clearUploadState();
    toast(`Imported ${pkg.id}`, "ok");
    loadPackages();
  } catch (e) {
    if (pauseRequested) {
      // Session stays on the server and in localStorage for later resume.
      showResumeBanner(loadUploadState(), session);
      toast("Upload paused — pick the same file later to resume", "");
    } else {
      toast("Upload failed: " + e.message + " (you can resume it later)", "err");
      if (session && loadUploadState()) showResumeBanner(loadUploadState(), session);
    }
  } finally {
    uploadInProgress = false;
    currentXhr = null;
    dz.classList.remove("busy");
    wrap.classList.add("hidden");
  }
}

// Warn before leaving the page mid-upload: the file handle cannot be
// restored, so the upload would have to be resumed by re-picking the file.
window.addEventListener("beforeunload", (e) => {
  if (uploadInProgress) {
    e.preventDefault();
    e.returnValue = ""; // triggers the browser's native leave confirmation
  }
});

// --- resume banner ---

function showResumeBanner(state, session) {
  if (!state || !session) return;
  const banner = $("#resume-banner");
  const uploaded = Object.values(session.chunks || {}).reduce((a, b) => a + b, 0);
  const pct = state.size ? Math.round((uploaded / state.size) * 100) : 0;
  banner.querySelector(".text").textContent =
    `Unfinished upload: ${state.fileName} (${pct}% uploaded). Pick the same file to resume.`;
  banner.classList.remove("hidden");
  pendingResume = { state, session };
}

function hideResumeBanner() {
  $("#resume-banner").classList.add("hidden");
  pendingResume = null;
}

async function checkUploadResume() {
  const state = loadUploadState();
  if (!state) return;
  try {
    const session = await api("GET", "/uploads/" + state.sessionId);
    showResumeBanner(state, session);
  } catch {
    clearUploadState(); // session no longer exists server-side
  }
}

function setupUpload() {
  const dz = $("#dropzone");
  const input = $("#file-input");
  const resumeInput = $("#resume-input");
  $("#browse-btn").onclick = () => input.click();
  input.onchange = () => { if (input.files[0]) uploadPackage(input.files[0]); input.value = ""; };
  dz.ondragover = (e) => { e.preventDefault(); dz.classList.add("dragover"); };
  dz.ondragleave = () => dz.classList.remove("dragover");
  dz.ondrop = (e) => {
    e.preventDefault();
    dz.classList.remove("dragover");
    if (e.dataTransfer.files[0]) uploadPackage(e.dataTransfer.files[0]);
  };

  // Pause: abort the in-flight chunk; the session survives for resume.
  $("#pause-btn").onclick = () => {
    pauseRequested = true;
    if (currentXhr) currentXhr.abort();
  };

  // Resume: the user re-picks the interrupted file (browsers can't reopen it).
  $("#resume-btn").onclick = () => resumeInput.click();
  resumeInput.onchange = () => {
    const file = resumeInput.files[0];
    resumeInput.value = "";
    if (!file || !pendingResume) return;
    const { state, session } = pendingResume;
    if (file.name !== state.fileName || file.size !== state.size) {
      toast(`File does not match the interrupted upload (expected ${state.fileName}, ${fmtBytes(state.size)})`, "err");
      return;
    }
    pendingResume = null;
    hideResumeBanner();
    uploadPackage(file, session);
  };
  $("#resume-discard-btn").onclick = async () => {
    if (!pendingResume) return;
    const { state } = pendingResume;
    try { await api("DELETE", "/uploads/" + state.sessionId); } catch { /* already gone */ }
    clearUploadState();
    hideResumeBanner();
  };
}

// ---------- deploy modal (install / edit / upgrade) ----------

// schemaFields renders form inputs from a JSON Schema's properties,
// recursing into nested objects with dot-path prefixes. Supported: string,
// integer/number, boolean, enum (select), array (comma-separated), object.
function schemaFields(schema, prefix = "") {
  const frag = document.createDocumentFragment();
  const props = schema.properties || {};
  const requiredSet = new Set(schema.required || []);
  for (const [key, prop] of Object.entries(props)) {
    const path = prefix ? `${prefix}.${key}` : key;
    const label = (prop.title || key) + (requiredSet.has(key) ? " *" : "");
    if (prop.type === "object" && prop.properties) {
      const grp = el("div", "schema-group");
      grp.append(el("div", "schema-group-title", label));
      if (prop.description) grp.append(el("div", "hint muted", prop.description));
      grp.append(schemaFields(prop, path));
      frag.append(grp);
      continue;
    }
    let input;
    if (prop.enum) {
      input = document.createElement("select");
      if (!requiredSet.has(key) && prop.default === undefined) input.append(new Option("(chart default)", ""));
      for (const opt of prop.enum) input.append(new Option(String(opt), String(opt)));
      if (prop.default !== undefined) input.value = String(prop.default);
    } else if (prop.type === "boolean") {
      input = document.createElement("input");
      input.type = "checkbox";
      input.checked = prop.default === true;
    } else if (prop.type === "integer" || prop.type === "number") {
      input = document.createElement("input");
      input.type = "number";
      if (prop.type === "integer") input.step = "1";
      if (prop.default !== undefined) input.value = prop.default;
    } else if (prop.type === "array") {
      input = document.createElement("input");
      input.type = "text";
      input.placeholder = "comma-separated values";
      if (Array.isArray(prop.default)) input.value = prop.default.join(", ");
    } else { // string or unspecified
      input = document.createElement("input");
      input.type = "text";
      if (prop.default !== undefined) input.value = prop.default;
    }
    input.dataset.schemaPath = path;
    input.dataset.schemaType = prop.enum ? "enum" : (prop.type || "string");
    if (requiredSet.has(key)) input.dataset.schemaRequired = "1";
    if (prop.type === "array") input.dataset.itemType = (prop.items && prop.items.type) || "string";
    if (prop.type === "boolean") {
      const row = el("div", "check-row");
      row.append(input, el("span", "", label));
      if (prop.description) row.append(el("span", "desc", prop.description));
      frag.append(row);
    } else {
      const f = el("div", "field");
      f.append(el("label", "", label));
      f.append(input);
      if (prop.description) f.append(el("div", "hint muted", prop.description));
      frag.append(f);
    }
  }
  return frag;
}

// collectSchemaValues reads the generated form into a flat dot-path map of
// typed values. Empty optional fields are skipped so chart defaults apply.
function collectSchemaValues(form) {
  const values = {};
  const missing = [];
  for (const i of form.querySelectorAll("[data-schema-path]")) {
    const path = i.dataset.schemaPath, type = i.dataset.schemaType;
    if (type === "boolean") { values[path] = i.checked; continue; }
    if (i.value.trim() === "") {
      if (i.dataset.schemaRequired) missing.push(path);
      continue;
    }
    if (type === "integer") values[path] = parseInt(i.value, 10);
    else if (type === "number") values[path] = parseFloat(i.value);
    else if (type === "array") {
      const parts = i.value.split(",").map((s) => s.trim()).filter((s) => s !== "");
      const numeric = i.dataset.itemType === "integer" || i.dataset.itemType === "number";
      values[path] = numeric ? parts.map(Number) : parts;
    } else {
      // Wrap values that look like bools/ints in single quotes so the
      // server-side type inference keeps them strings (zarf convention).
      const v = i.value.trim();
      values[path] = /^(true|false)$/i.test(v) || /^-?\d+$/.test(v) ? `'${v}'` : v;
    }
  }
  return { values, missing };
}

// definition: DefinitionInfo from /packages/{id}/definition (or reconstructed
// from a deployment). preselectedComponents: names to check. When the package
// ships a config.schema.json at its root, the values section is a form
// generated from that schema instead of free-form key/value rows.
async function openDeployModal(opts) {
  const { title, storeId, definition, preselectedComponents } = opts;
  let schema = null;
  try {
    const r = await fetch(`${API}/packages/${encodeURIComponent(storeId)}/config-schema`);
    if (r.ok) schema = await r.json();
  } catch { /* no schema -> free-form values */ }
  const body = el("div");

  // --- components ---
  const optional = (definition.components || []).filter((c) => c.optional);
  const required = (definition.components || []).filter((c) => !c.optional);
  if (definition.components && definition.components.length) {
    const f = el("div", "field");
    f.append(el("label", "", "Components"));
    for (const c of required) {
      const row = el("div", "check-row");
      row.innerHTML = `<input type="checkbox" checked disabled>
        <span>${esc(c.name)}</span><span class="desc">${esc(c.description || "required")}</span>`;
      f.append(row);
    }
    const groups = new Map();
    for (const c of optional) {
      const key = c.group || "";
      if (!groups.has(key)) groups.set(key, []);
      groups.get(key).push(c);
    }
    for (const [group, comps] of groups) {
      if (group) f.append(el("div", "muted", `group: ${group} (pick at most one)`));
      for (const c of comps) {
        const row = el("div", "check-row");
        const input = document.createElement("input");
        input.type = group ? "radio" : "checkbox";
        input.name = group ? "grp-" + group : "";
        input.value = c.name;
        input.dataset.component = c.name;
        const pre = preselectedComponents
          ? preselectedComponents.includes(c.name)
          : c.default;
        input.checked = !!pre;
        row.append(input, el("span", "", c.name), el("span", "desc", c.description || ""));
        f.append(row);
      }
    }
    body.append(f);
  }

  // --- configuration: schema-generated form when the package ships a
  // config.schema.json, otherwise free-form per-chart helm values overrides ---
  const charts = [];
  for (const c of definition.components || []) {
    for (const ch of c.charts || []) charts.push({ component: c.name, ...ch });
  }
  if (schema) {
    const cf = el("div", "field");
    cf.dataset.schemaForm = "1";
    cf.append(el("label", "", "Configuration"));
    cf.append(schemaFields(schema));
    body.append(cf);
  } else if (charts.length) {
    const vf = el("div", "field");
    vf.append(el("label", "", "Helm values overrides"));
    const hint = el("div", "hint", "Dot-path key/value pairs per chart. Types are inferred: 3 → number, true → bool.");
    hint.classList.add("muted");
    vf.append(hint);
    for (const ch of charts) {
      const box = el("div", "chart-values");
      box.dataset.component = ch.component;
      box.dataset.chart = ch.name;
      const head = el("div", "check-row");
      head.append(el("span", "mono", ch.name),
        el("span", "desc", [ch.version, ch.namespace && `ns: ${ch.namespace}`].filter(Boolean).join(" · ")));
      box.append(head);
      const rows = el("div");
      const addRow = (k = "", v = "") => {
        const row = el("div", "check-row values-row");
        const ki = document.createElement("input");
        ki.type = "text";
        ki.placeholder = "replicaCount";
        ki.className = "value-key";
        ki.value = k;
        ki.style.flex = "1";
        const vi = document.createElement("input");
        vi.type = "text";
        vi.placeholder = "3";
        vi.className = "value-val";
        vi.value = v;
        vi.style.flex = "1";
        const rm = el("button", "btn small", "✕");
        rm.onclick = () => row.remove();
        row.append(ki, vi, rm);
        rows.append(row);
      };
      addRow();
      const addBtn = el("button", "btn small", "+ Add value");
      addBtn.onclick = () => addRow();
      box.append(rows, addBtn);
      vf.append(box);
    }
    body.append(vf);
  }

  // --- advanced ---
  const adv = el("details", "advanced");
  adv.innerHTML = `<summary>Advanced options</summary>`;
  const mkText = (labelText, name, placeholder) => {
    const f = el("div", "field");
    f.append(el("label", "", labelText));
    const input = document.createElement("input");
    input.type = "text";
    input.dataset.opt = name;
    if (placeholder) input.placeholder = placeholder;
    f.append(input);
    adv.append(f);
  };
  const mkCheck = (labelText, name, hint) => {
    const f = el("div", "check-row");
    const input = document.createElement("input");
    input.type = "checkbox";
    input.dataset.opt = name;
    f.append(input, el("span", "", labelText));
    if (hint) f.append(el("span", "desc", hint));
    adv.append(f);
  };
  mkText("Timeout", "timeout", "e.g. 15m (zarf default when empty)");
  mkCheck("Take ownership of existing resources", "takeOwnership");
  mkCheck("Connected deploy (no image/repo mirroring)", "connected", "for clusters without zarf init");
  mkCheck("Force conflicts (server-side apply)", "forceConflicts");
  mkCheck("Skip version check", "skipVersionCheck");
  mkCheck("Delete package from store after successful deploy", "deletePackageAfterDeploy",
    "the install stays fully managed from cluster state; re-upload the package to edit it later");
  body.append(adv);

  const submit = el("button", "btn primary", opts.submitLabel || "Deploy");
  submit.onclick = async () => {
    const components = [...body.querySelectorAll("[data-component]")]
      .filter((i) => i.checked).map((i) => i.dataset.component);
    const valuesOverrides = {};
    const schemaForm = body.querySelector("[data-schema-form]");
    if (schemaForm) {
      // Generated form: same values for every chart of the deployed
      // (required + selected) components.
      const { values, missing } = collectSchemaValues(schemaForm);
      if (missing.length) {
        toast("Missing required configuration: " + missing.join(", "), "err");
        return;
      }
      if (Object.keys(values).length) {
        const selected = new Set(required.map((c) => c.name));
        for (const n of components) selected.add(n);
        for (const c of definition.components || []) {
          if (!selected.has(c.name)) continue;
          for (const ch of c.charts || []) {
            (valuesOverrides[c.name] ??= {})[ch.name] ??= {};
            Object.assign(valuesOverrides[c.name][ch.name], values);
          }
        }
      }
    } else {
      body.querySelectorAll(".chart-values").forEach((box) => {
        box.querySelectorAll(".values-row").forEach((row) => {
          const k = row.querySelector(".value-key").value.trim();
          const v = row.querySelector(".value-val").value;
          if (k === "") return;
          const comp = box.dataset.component, chart = box.dataset.chart;
          (valuesOverrides[comp] ??= {})[chart] ??= {};
          valuesOverrides[comp][chart][k] = v;
        });
      });
    }
    const req = {};
    if (Object.keys(valuesOverrides).length) req.valuesOverrides = valuesOverrides;
    if (components.length) req.components = components.join(",");
    for (const i of body.querySelectorAll("[data-opt]")) {
      const k = i.dataset.opt;
      if (i.type === "checkbox") { if (i.checked) req[k] = true; }
      else if (i.value.trim() !== "") req[k] = i.value.trim();
    }
    submit.disabled = true;
    try {
      const job = await api("POST", `/packages/${encodeURIComponent(storeId)}/deploy`, req);
      closeModal();
      toast(`${opts.actionLabel || "Deploy"} started (job ${job.id})`, "ok");
      switchToJobs();
    } catch (e) {
      toast(e.message, "err");
      submit.disabled = false;
    }
  };
  const cancel = el("button", "btn", "Cancel");
  cancel.onclick = closeModal;
  openModal(title, body, [cancel, submit]);
}

async function openInstallModal(pkg) {
  try {
    const definition = await api("GET", `/packages/${encodeURIComponent(pkg.id)}/definition`);
    openDeployModal({
      title: `Install ${pkg.name} ${pkg.version || ""}`,
      storeId: pkg.id,
      definition,
      actionLabel: "Deploy",
    });
  } catch (e) { toast("Unable to load package definition: " + e.message, "err"); }
}

// ---------- installed ----------

let storePackages = [];

async function loadInstalled() {
  const root = $("#installed-list");
  try {
    const [deps, pkgs] = await Promise.all([
      api("GET", "/deployments"),
      api("GET", "/packages"),
    ]);
    storePackages = pkgs.packages || [];
    const deployments = deps.deployments || [];
    root.innerHTML = "";
    if (deployments.length === 0) {
      root.append(el("div", "empty", "Nothing deployed yet."));
      return;
    }
    const card = el("div", "card");
    const table = el("table");
    table.innerHTML = `<thead><tr>
      <th>Package</th><th>Version</th><th>Components</th><th>Connectivity</th><th>Gen</th><th></th>
    </tr></thead>`;
    const tbody = el("tbody");
    for (const d of deployments) {
      const tr = el("tr");
      const comps = (d.components || [])
        .map((c) => `<span class="badge ${c.status === "Succeeded" ? "ok" : "err"}">${esc(c.name)}</span>`)
        .join(" ");
      tr.insertAdjacentHTML("beforeend", `
        <td class="mono">${esc(d.package)}</td>
        <td class="mono">${esc(d.version || "—")}</td>
        <td>${comps}</td>
        <td>${esc(d.connectivity || "—")}</td>
        <td>${d.generation ?? "—"}</td>
        <td class="actions"></td>`);
      const actions = tr.lastElementChild;

      const edit = el("button", "btn small", "Edit");
      edit.onclick = () => openEditModal(d);
      const upgrade = el("button", "btn small", "Upgrade");
      upgrade.onclick = () => openUpgradeModal(d);
      const del = el("button", "btn small danger", "Delete");
      del.onclick = () => confirmModal(
        "Remove deployment",
        `Remove ${d.package} from the cluster? All its components will be uninstalled.`,
        "Remove",
        async () => {
          try {
            const job = await api("DELETE", `/deployments/${encodeURIComponent(d.package)}`);
            toast(`Remove started (job ${job.id})`, "ok");
            switchToJobs();
          } catch (e) { toast(e.message, "err"); }
        });
      actions.append(edit, upgrade, del);
      tbody.append(tr);
    }
    table.append(tbody);
    card.append(table);
    root.append(card);
  } catch (e) {
    root.innerHTML = `<div class="empty">Failed to load deployments: ${esc(e.message)}</div>`;
  }
}

// Find a store package matching a deployment's name+version.
function findStoreVersion(name, version) {
  return storePackages.find((p) => p.name === name && p.version === version);
}

// Reconstruct a DefinitionInfo from a deployed package's stored definition.
function definitionFromDeployment(full) {
  const data = full.data || {};
  return {
    name: data.metadata?.name,
    version: data.metadata?.version,
    variables: (data.variables || []).map((v) => ({
      name: v.name, default: v.default, description: v.description,
      sensitive: v.sensitive, prompt: v.prompt,
    })),
    components: (data.components || []).map((c) => ({
      name: c.name, description: c.description,
      optional: c.required !== true,
      default: c.default, group: c.group,
      charts: (c.charts || []).map((ch) => ({
        name: ch.name, namespace: ch.namespace, version: ch.version,
      })),
    })),
  };
}

async function openEditModal(d) {
  const storePkg = findStoreVersion(d.package, d.version);
  if (!storePkg) {
    toast(`Version ${d.version} of ${d.package} is not in the local store — upload it to edit.`, "err");
    return;
  }
  try {
    const full = await api("GET", `/deployments/${encodeURIComponent(d.package)}`);
    const definition = definitionFromDeployment(full);
    const deployedNames = (full.deployedComponents || []).map((c) => c.name);
    openDeployModal({
      title: `Edit ${d.package} ${d.version || ""}`,
      storeId: storePkg.id,
      definition,
      preselectedComponents: deployedNames,
      submitLabel: "Apply",
      actionLabel: "Redeploy",
    });
  } catch (e) { toast(e.message, "err"); }
}

function openUpgradeModal(d) {
  const candidates = storePackages
    .filter((p) => p.name === d.package && p.version !== d.version)
    .sort((a, b) => -cmpVersion(a.version, b.version));
  if (candidates.length === 0) {
    toast(`No other versions of ${d.package} in the local store.`, "err");
    return;
  }
  const body = el("div");
  const f = el("div", "field");
  f.append(el("label", "", `Upgrade ${d.package} (currently ${d.version || "unknown"}) to:`));
  const sel = document.createElement("select");
  for (const p of candidates) {
    const opt = document.createElement("option");
    opt.value = p.id;
    opt.textContent = `${p.version} (${fmtBytes(p.size)})`;
    sel.append(opt);
  }
  f.append(sel);
  body.append(f);

  const next = el("button", "btn primary", "Continue");
  next.onclick = async () => {
    const storePkg = candidates.find((p) => p.id === sel.value);
    closeModal();
    try {
      const full = await api("GET", `/deployments/${encodeURIComponent(d.package)}`);
      const deployedNames = (full.deployedComponents || []).map((c) => c.name);
      const definition = await api("GET", `/packages/${encodeURIComponent(storePkg.id)}/definition`);
      openDeployModal({
        title: `Upgrade ${d.package} → ${storePkg.version}`,
        storeId: storePkg.id,
        definition,
        preselectedComponents: deployedNames,
        submitLabel: "Upgrade",
        actionLabel: "Upgrade",
      });
    } catch (e) { toast(e.message, "err"); }
  };
  const cancel = el("button", "btn", "Cancel");
  cancel.onclick = closeModal;
  openModal("Upgrade " + d.package, body, [cancel, next]);
}

function openNewInstallModal() {
  const groups = groupByName(storePackages);
  if (groups.length === 0) {
    toast("No packages in the store — upload one first.", "err");
    return;
  }
  const body = el("div");
  const f = el("div", "field");
  f.append(el("label", "", "Package"));
  const sel = document.createElement("select");
  for (const [name, versions] of groups) {
    for (const p of versions) {
      const opt = document.createElement("option");
      opt.value = p.id;
      opt.textContent = `${name} ${p.version || ""}`.trim();
      sel.append(opt);
    }
  }
  f.append(sel);
  body.append(f);
  const next = el("button", "btn primary", "Continue");
  next.onclick = () => {
    const pkg = storePackages.find((p) => p.id === sel.value);
    closeModal();
    openInstallModal(pkg);
  };
  const cancel = el("button", "btn", "Cancel");
  cancel.onclick = closeModal;
  openModal("New installation", body, [cancel, next]);
}

// ---------- jobs ----------

let lastJobStatus = new Map();

async function loadJobs() {
  try {
    const data = await api("GET", "/jobs");
    const jobs = (data.jobs || []).slice().reverse(); // newest first
    renderJobs(jobs);
    updateJobsBadge(jobs);
    // Detect transitions to terminal states.
    for (const j of jobs) {
      const prev = lastJobStatus.get(j.id);
      if (prev && prev !== j.status) {
        if (j.status === "succeeded") toast(`${j.kind} ${j.package} succeeded`, "ok");
        if (j.status === "failed") toast(`${j.kind} ${j.package} failed: ${j.error || ""}`, "err");
        loadInstalledIfVisible();
      }
      lastJobStatus.set(j.id, j.status);
    }
  } catch { /* jobs endpoint unreachable — ignore during polling */ }
}

function loadInstalledIfVisible() {
  if (activeTab === "installed") loadInstalled();
}

function renderJobs(jobs) {
  const root = $("#jobs-list");
  root.innerHTML = "";
  if (jobs.length === 0) {
    root.append(el("div", "empty", "No jobs yet."));
    return;
  }
  const card = el("div", "card");
  const table = el("table");
  table.innerHTML = `<thead><tr>
    <th>Kind</th><th>Package</th><th>Status</th><th>Started</th><th>Duration</th><th></th>
  </tr></thead>`;
  const tbody = el("tbody");
  for (const j of jobs) {
    const tr = el("tr");
    const cls = j.status === "succeeded" ? "ok" : j.status === "failed" ? "err" : "run";
    let dur = "—";
    if (j.startedAt) {
      const end = j.finishedAt ? new Date(j.finishedAt) : new Date();
      dur = ((end - new Date(j.startedAt)) / 1000).toFixed(1) + "s";
    }
    tr.insertAdjacentHTML("beforeend", `
      <td>${esc(j.kind)}</td>
      <td class="mono">${esc(j.package)}</td>
      <td><span class="badge ${cls}">${esc(j.status)}</span></td>
      <td>${fmtTime(j.startedAt)}</td>
      <td>${dur}</td>
      <td class="actions"></td>`);
    const logs = el("button", "btn small", "Logs");
    logs.onclick = () => showJobLogs(j);
    tr.lastElementChild.append(logs);
    tbody.append(tr);
  }
  table.append(tbody);
  card.append(table);
  root.append(card);
}

async function showJobLogs(j) {
  $("#job-logs-title").textContent = `${j.kind} ${j.package} (${j.id})`;
  $("#job-logs").classList.remove("hidden");
  const render = (data) => {
    const body = $("#job-logs-body");
    body.innerHTML = "";
    for (const line of data.logs || []) {
      const span = el("span", "lvl-" + line.level, `[${line.level}] ${line.message}`);
      if (line.attrs && Object.keys(line.attrs).length) {
        span.textContent += " " + Object.entries(line.attrs).map(([k, v]) => `${k}=${v}`).join(" ");
      }
      body.append(span, "\n");
    }
    if (data.job && data.job.error) body.append(el("span", "lvl-ERROR", "error: " + data.job.error));
  };
  try {
    render(await api("GET", `/jobs/${j.id}?tail=500`));
  } catch (e) { toast(e.message, "err"); }
}

function updateJobsBadge(jobs) {
  const running = jobs.filter((j) => j.status === "running" || j.status === "pending").length;
  const badge = $("#jobs-badge");
  badge.classList.toggle("hidden", running === 0);
  badge.classList.add("count");
  badge.textContent = running || "";
}

function switchToJobs() {
  location.hash = "/jobs";
}

// ---------- boot ----------

$("#modal-close").onclick = closeModal;
$("#modal-backdrop").onclick = (e) => { if (e.target.id === "modal-backdrop") closeModal(); };
$("#new-install-btn").onclick = openNewInstallModal;
$("#refresh-installed-btn").onclick = loadInstalled;
$("#job-logs-close").onclick = () => $("#job-logs").classList.add("hidden");

setupUpload();
applyRoute();
checkUploadResume();
api("GET", "/version").then((v) => { $("#version").textContent = v.version; }).catch(() => {});
setInterval(loadJobs, 3000);
loadJobs();
