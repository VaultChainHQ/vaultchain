'use strict';

// Everything from the chain (file names, addresses) is untrusted text, so the
// page only ever uses textContent / DOM APIs, never innerHTML.
const $ = (id) => document.getElementById(id);
const HDR = { 'X-Vault-UI': '1' };

function el(tag, props, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (k === 'class') e.className = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else e[k] = v;
  }
  for (const k of kids) e.append(k);
  return e;
}

async function api(path, opts = {}) {
  const r = await fetch(path, { ...opts, headers: { ...HDR, ...(opts.headers || {}) } });
  const text = await r.text();
  let body = null;
  try { body = JSON.parse(text); } catch (_) { /* not json */ }
  if (!r.ok) throw new Error((body && body.error) || text.trim() || ('HTTP ' + r.status));
  return body;
}

function postJSON(path, obj) {
  return api(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(obj) });
}

function say(msg, cls) {
  const s = $('status');
  s.textContent = msg;
  s.className = 'muted ' + (cls || '');
}

function fmtSize(n) {
  if (n < 1024) return n + ' B';
  const u = ['KiB', 'MiB', 'GiB'];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
  return n.toFixed(1) + ' ' + u[i];
}

let me = null;

async function loadInfo() {
  try {
    me = await api('/api/info');
    const i = $('info');
    i.textContent = '';
    i.append(
      'account ', el('code', { textContent: me.address }),
      ` · ${me.balance} ${me.denom} · chain ${me.chain_id} · height ${me.height} · provider `,
      el('code', { textContent: me.provider_address }),
    );
  } catch (e) {
    $('info').textContent = 'cannot reach the node: ' + e.message;
    $('info').className = 'err';
  }
}

async function download(f, decrypt) {
  say('downloading ' + f.name + '…');
  try {
    const r = await fetch('/api/download?root=' + encodeURIComponent(f.root) + (decrypt ? '&decrypt=1' : ''), { headers: HDR });
    if (!r.ok) throw new Error((await r.text()).trim() || 'HTTP ' + r.status);
    const blob = await r.blob();
    let name = f.name || 'download';
    if (f.encrypted && !decrypt) name += '.vle';
    const a = el('a', { href: URL.createObjectURL(blob), download: name });
    document.body.append(a);
    a.click();
    a.remove();
    say('downloaded ' + name, 'ok');
  } catch (e) {
    say('download failed: ' + e.message, 'err');
  }
}

async function grant(f, who, pub) {
  if (!who.value.trim()) return say('enter a grantee address', 'err');
  say('granting…');
  try {
    const res = await postJSON('/api/grant', { root: f.root, grantee: who.value.trim(), grantee_pubkey: pub.value.trim() });
    say(res.key_shared ? 'granted; the file key was shared with the grantee' : 'granted', 'ok');
    who.value = ''; pub.value = '';
    await loadFiles();
  } catch (e) { say('grant failed: ' + e.message, 'err'); }
}

async function revoke(f, g) {
  say('revoking…');
  try {
    await postJSON('/api/revoke', { root: f.root, grant_id: g.id });
    say('revoked. The provider stops serving them within a few seconds; a key they already hold stays valid.', 'ok');
    await loadFiles();
  } catch (e) { say('revoke failed: ' + e.message, 'err'); }
}

function card(f) {
  const title = el('div', {}, el('span', { class: 'name', textContent: f.name || '(unnamed)' }),
    el('span', { class: 'tag' + (f.encrypted ? ' enc' : ''), textContent: f.encrypted ? 'encrypted' : 'plaintext' }),
    el('span', { class: 'tag', textContent: f.mine ? 'mine' : 'shared with me' }));
  const meta = el('div', { class: 'muted' }, fmtSize(f.size) + ' · ', el('code', { textContent: f.root }));
  const row = el('div', { class: 'row' });
  if (f.encrypted) {
    row.append(el('button', { textContent: 'download (decrypt)', onclick: () => download(f, true) }),
      el('button', { textContent: 'download ciphertext', onclick: () => download(f, false) }));
  } else {
    row.append(el('button', { textContent: 'download', onclick: () => download(f, false) }));
  }
  const c = el('div', { class: 'card' }, title, meta, row);
  if (f.mine) {
    const d = el('div', { class: 'muted' });
    d.textContent = f.deals.length
      ? 'deals: ' + f.deals.map((x) => `#${x.id} ${x.status}${x.replica_status ? '/' + x.replica_status : ''}${x.last_proven_epoch ? ' proven@' + x.last_proven_epoch : ''}`).join(', ')
      : 'no storage deal';
    c.append(d);
    if (f.grants.length) {
      const ul = el('ul', { class: 'grants' });
      for (const g of f.grants) {
        ul.append(el('li', {}, `${g.capability} → `, el('code', { textContent: g.grantee }),
          g.expiry_height ? ` (until ${g.expiry_height}) ` : ' ',
          el('button', { textContent: 'revoke', onclick: () => revoke(f, g) })));
      }
      c.append(ul);
    }
    const who = el('input', { type: 'text', placeholder: 'grantee address (vault1…)', size: 44 });
    const pub = el('input', { type: 'text', placeholder: 'grantee pubkey hex (only if they never sent a tx)', size: 30 });
    const g = el('div', { class: 'row' }, who);
    if (f.encrypted) g.append(pub);
    g.append(el('button', { textContent: 'grant read', onclick: () => grant(f, who, pub) }));
    c.append(g);
  }
  return c;
}

async function loadFiles() {
  const box = $('files');
  try {
    const { files } = await api('/api/files');
    box.textContent = '';
    box.className = '';
    if (!files.length) { box.textContent = 'no files yet'; box.className = 'muted'; return; }
    for (const f of files) box.append(card(f));
  } catch (e) {
    box.textContent = 'cannot list files: ' + e.message;
    box.className = 'err';
  }
}

$('upload').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const file = $('file').files[0];
  if (!file) return;
  const fd = new FormData();
  fd.append('encrypt', $('encrypt').checked ? '1' : '0'); // fields first, the file part last
  fd.append('name', file.name);
  fd.append('file', file, file.name);
  $('go').disabled = true;
  say(`uploading ${file.name} (${fmtSize(file.size)})… commit, deal and upload take a few blocks`);
  try {
    const r = await api('/api/upload', { method: 'POST', body: fd });
    say(`done: ${r.steps.join(', ')} · root ${r.root}`, 'ok');
    $('upload').reset();
    await Promise.all([loadInfo(), loadFiles()]);
  } catch (e) {
    say('upload failed: ' + e.message, 'err');
  } finally {
    $('go').disabled = false;
  }
});

$('refresh').addEventListener('click', () => { loadInfo(); loadFiles(); });
loadInfo();
loadFiles();
setInterval(loadFiles, 15000);
