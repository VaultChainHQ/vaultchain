const out = document.getElementById("out");
const meta = document.getElementById("meta");
const providerFiles = document.getElementById("provider-files");

function pretty(value) {
  if (typeof value === "string") {
    try { return JSON.stringify(JSON.parse(value), null, 2); }
    catch { return value; }
  }
  return JSON.stringify(value, null, 2);
}

function setOut(title, payload) {
  out.textContent = `${title}\n${"─".repeat(48)}\n${pretty(payload)}`;
}

async function call(method, url) {
  const res = await fetch(url, { method });
  const text = await res.text();
  let data;
  try { data = JSON.parse(text); }
  catch { data = text; }
  if (!res.ok) {
    throw new Error(typeof data === "object" ? (data.error || pretty(data)) : text);
  }
  return data;
}

function renderProviderFiles(data) {
  const files = data?.data?.files || data?.files || [];
  providerFiles.innerHTML = "";
  if (!files.length) {
    providerFiles.textContent = "No objects on provider yet.";
    return;
  }
  for (const cid of files) {
    const a = document.createElement("a");
    a.href = `/api/provider/download/${cid}`;
    a.textContent = cid;
    a.download = cid;
    providerFiles.appendChild(a);
  }
}

async function runButton(btn) {
  const method = btn.dataset.action;
  const url = btn.dataset.url;
  const after = btn.dataset.after;
  btn.classList.add("busy");
  try {
    const data = await call(method, url);
    setOut(`${method} ${url}`, data);
    if (after === "renderProviderFiles") renderProviderFiles(data);
  } catch (err) {
    setOut(`${method} ${url} FAILED`, String(err.message || err));
  } finally {
    btn.classList.remove("busy");
  }
}

document.querySelectorAll("button[data-url]").forEach((btn) => {
  btn.addEventListener("click", () => runButton(btn));
});

document.getElementById("clear-out").addEventListener("click", () => {
  out.textContent = "Ready. Pick an action above.";
});

document.getElementById("upload-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const fileInput = document.getElementById("upload-file");
  const text = document.getElementById("upload-text").value.trim();
  const fd = new FormData();
  if (fileInput.files?.[0]) {
    fd.append("file", fileInput.files[0]);
  } else if (text) {
    fd.append("text", text);
  } else {
    setOut("Upload", "Provide a file or some text.");
    return;
  }
  setOut("POST /api/storage/upload", "working…");
  try {
    const res = await fetch("/api/storage/upload", { method: "POST", body: fd });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || pretty(data));
    setOut("POST /api/storage/upload", data);
    document.getElementById("upload-text").value = "";
    fileInput.value = "";
  } catch (err) {
    setOut("POST /api/storage/upload FAILED", String(err.message || err));
  }
});

(async function boot() {
  try {
    const health = await call("GET", "/api/health");
    const status = await call("GET", "/api/status");
    const height =
      status?.data?.result?.sync_info?.latest_block_height ||
      status?.data?.sync_info?.latest_block_height ||
      status?.data?.height ||
      "?";
    meta.textContent = `${health.chain_id} · height ${height} · key ${health.key}`;
  } catch (err) {
    meta.textContent = `offline — ${err.message || err}`;
  }
})();
