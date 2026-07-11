// Toast notifications, modal dialogs, and confirm prompts.

export function toast(message, { type = "info", title = "", timeout = 4000 } = {}) {
  const stack = document.getElementById("toast-stack");
  const node = document.createElement("div");
  node.className = `toast ${type}`;
  node.innerHTML = (title ? `<strong>${escape(title)}</strong>` : "") + escape(message);
  stack.appendChild(node);
  setTimeout(() => {
    node.style.transition = "opacity .2s";
    node.style.opacity = "0";
    setTimeout(() => node.remove(), 200);
  }, timeout);
}

export function toastError(e, title = "Error") {
  toast(e && e.message ? e.message : String(e), { type: "err", title });
}

// Generic modal. fields: [{name,label,type,value,placeholder,options}]
// Returns a Promise resolving to the collected values, or null if cancelled.
export function formModal({ title, fields = [], submitLabel = "Save", submitClass = "btn-primary" }) {
  return new Promise((resolve) => {
    const backdrop = document.createElement("div");
    backdrop.className = "modal-backdrop";
    const body = fields.map((f) => {
      if (f.type === "select") {
        const opts = f.options.map((o) => {
          const val = typeof o === "string" ? o : o.value;
          const lbl = typeof o === "string" ? o : o.label;
          return `<option value="${escape(val)}" ${val === f.value ? "selected" : ""}>${escape(lbl)}</option>`;
        }).join("");
        return `<div class="field"><label>${escape(f.label)}</label><select name="${f.name}">${opts}</select></div>`;
      }
      if (f.type === "static") {
        return `<div class="field"><label>${escape(f.label)}</label><div class="mono muted">${escape(f.value || "")}</div></div>`;
      }
      return `<div class="field"><label>${escape(f.label)}</label>
        <input name="${f.name}" type="${f.type || "text"}" value="${escape(f.value || "")}" placeholder="${escape(f.placeholder || "")}" /></div>`;
    }).join("");
    backdrop.innerHTML = `<div class="modal"><div class="modal-head">${escape(title)}</div>
      <form class="modal-body">${body}</form>
      <div class="modal-foot"><button class="btn" data-cancel>Cancel</button>
      <button class="btn ${submitClass}" data-ok>${escape(submitLabel)}</button></div></div>`;
    document.body.appendChild(backdrop);
    const form = backdrop.querySelector("form");
    const first = form.querySelector("input,select");
    if (first) first.focus();
    const close = (result) => { backdrop.remove(); resolve(result); };
    const submit = () => {
      const data = {};
      for (const f of fields) {
        if (f.type === "static") continue;
        const node = form.querySelector(`[name="${f.name}"]`);
        if (node) data[f.name] = node.value.trim();
      }
      close(data);
    };
    backdrop.querySelector("[data-cancel]").onclick = () => close(null);
    backdrop.querySelector("[data-ok]").onclick = submit;
    form.onsubmit = (e) => { e.preventDefault(); submit(); };
    backdrop.onclick = (e) => { if (e.target === backdrop) close(null); };
  });
}

export function confirmModal({ title, message, confirmLabel = "Confirm", danger = false }) {
  return new Promise((resolve) => {
    const backdrop = document.createElement("div");
    backdrop.className = "modal-backdrop";
    backdrop.innerHTML = `<div class="modal"><div class="modal-head">${escape(title)}</div>
      <div class="modal-body"><p style="margin:0;color:var(--text-muted)">${escape(message)}</p></div>
      <div class="modal-foot"><button class="btn" data-cancel>Cancel</button>
      <button class="btn ${danger ? "btn-danger" : "btn-primary"}" data-ok>${escape(confirmLabel)}</button></div></div>`;
    document.body.appendChild(backdrop);
    backdrop.querySelector("[data-ok]").focus();
    const close = (v) => { backdrop.remove(); resolve(v); };
    backdrop.querySelector("[data-cancel]").onclick = () => close(false);
    backdrop.querySelector("[data-ok]").onclick = () => close(true);
    backdrop.onclick = (e) => { if (e.target === backdrop) close(false); };
  });
}

function escape(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => (
    { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]
  ));
}
