// Configuration: view and edit runtime config keys, grouped by section.

import { api } from "../api.js";
import { toast, toastError, formModal } from "../ui.js";
import { escapeHtml, debounce } from "../util.js";

let entries = [];
let search = "";

export function mount(container) {
  container.innerHTML = `<div id="cfg"><div class="empty">Loading configuration…</div></div>`;
  load();
}
export function unmount() {}

async function load() {
  try { entries = await api.get("/config"); render(); }
  catch (e) { document.getElementById("cfg").innerHTML = `<div class="empty">${escapeHtml(e.message)}</div>`; }
}

function render() {
  const root = document.getElementById("cfg");
  if (!root) return;
  const groups = {};
  for (const e of entries) {
    if (search && !e.key.toLowerCase().includes(search) && !(e.value || "").toLowerCase().includes(search)) continue;
    const section = e.key.includes(".") ? e.key.split(".")[0] : "general";
    (groups[section] ||= []).push(e);
  }
  const sections = Object.keys(groups).sort();
  root.innerHTML = `
    <div class="toolbar"><input class="search" id="cfg-search" placeholder="Filter keys…" value="${escapeHtml(search)}" />
      <div class="spacer"></div><span class="faint">${entries.length} keys</span></div>
    ${sections.map((sec) => `
      <div class="section-title">${escapeHtml(sec)}</div>
      <div class="card"><div class="table-wrap"><table class="data">
        <tbody>${groups[sec].map(row).join("")}</tbody>
      </table></div></div>`).join("") || `<div class="empty">No keys match.</div>`}`;
  document.getElementById("cfg-search").oninput = debounce((e) => { search = e.target.value.trim().toLowerCase(); render(); }, 150);
  root.querySelectorAll("[data-edit]").forEach((b) => b.onclick = () => edit(b.dataset.edit, b.dataset.val));
}

function row(e) {
  const secret = /secret|token|password/i.test(e.key);
  const shown = secret && e.value ? "••••••••" : (e.value || "");
  return `<tr>
    <td class="mono" style="width:40%;">${escapeHtml(e.key)}</td>
    <td class="mono muted">${escapeHtml(shown) || '<span class="faint">—</span>'}</td>
    <td style="width:80px;"><div class="row-actions">
      <button class="btn btn-sm btn-ghost" data-edit="${escapeHtml(e.key)}" data-val="${escapeHtml(secret ? "" : (e.value || ""))}">Edit</button>
    </div></td>
  </tr>`;
}

async function edit(key, value) {
  const v = await formModal({ title: `Set ${key}`, submitLabel: "Save", fields: [
    { name: "key", label: "Key", type: "static", value: key },
    { name: "value", label: "Value", value },
  ]});
  if (!v) return;
  try {
    await api.put(`/config/${encodeURIComponent(key)}`, { value: v.value });
    toast(`Updated ${key}`, { type: "ok" });
    await load();
  } catch (e) { toastError(e); }
}
