// OpsView Web Viewer
// OVP protocol + Camera (MJPEG/HLS) support

'use strict';

// --- OVP Protocol constants ---
const OVP_MAGIC = 0x4F565031;
const OVP_HEADER_SIZE = 12;
const FRAME_DELTA_HEADER_SIZE = 22;
const TILE_HEADER_SIZE = 10;

const MSG_HELLO = 1;
const MSG_AUTH = 2;
const MSG_FRAME_DELTA = 3;
const MSG_HEARTBEAT = 6;
const MSG_ERROR = 7;
const MSG_SURV_CONFIG = 8;

// --- State ---
let ws = null;
let connected = false;
let canvas, ctx;
let screenWidth = 0, screenHeight = 0;
let frameCount = 0;
let lastFpsTime = 0;
let fps = 0;
let bytesReceived = 0;
let tilesReceived = 0;
let reconnectAttempts = 0;
const MAX_RECONNECT = 5;
let reconnectTimer = null;

// Surv state
let survPlayers = {}; // id -> { hls, interval }

// --- Init ---
document.addEventListener('DOMContentLoaded', () => {
  canvas = document.getElementById('screen');
  ctx = canvas.getContext('2d');

  // Load saved settings
  const savedIp = localStorage.getItem('opsview_relay_ip');
  const savedPort = localStorage.getItem('opsview_relay_port');
  const savedPin = localStorage.getItem('opsview_pin');
  const savedProto = localStorage.getItem('opsview_proto');
  if (savedIp) document.getElementById('relayIp').value = savedIp;
  if (savedPort) document.getElementById('relayPort').value = savedPort;
  if (savedPin) document.getElementById('pin').value = savedPin;
  if (savedProto) document.getElementById('proto').value = savedProto;

  // Auto-detect protocol
  if (!savedProto && location.protocol === 'https:') {
    document.getElementById('proto').value = 'wss';
  }

  setInterval(updateStats, 1000);
});

// --- Tabs ---
function switchTab(panel) {
  document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
  document.querySelector(`.tab[data-panel="${panel}"]`).classList.add('active');
  document.getElementById('panel-ops').style.display = panel === 'ops' ? '' : 'none';
  document.getElementById('panel-surv').style.display = panel === 'surv' ? '' : 'none';
}

// --- Error toast ---
let errorTimer = null;
function showError(msg) {
  const el = document.getElementById('errorToast');
  el.textContent = msg;
  el.style.display = 'block';
  clearTimeout(errorTimer);
  errorTimer = setTimeout(() => { el.style.display = 'none'; }, 5000);
}

// --- Connection ---
function toggleConnection() {
  if (connected) {
    disconnect();
  } else {
    connect();
  }
}

function connect() {
  const proto = document.getElementById('proto').value;
  const ip = document.getElementById('relayIp').value.trim();
  const port = document.getElementById('relayPort').value.trim();
  const pin = document.getElementById('pin').value.trim();

  if (!ip || !port) {
    showError('Please enter IP and port');
    return;
  }

  const url = `${proto}://${ip}:${port}/watch`;

  // Save settings
  localStorage.setItem('opsview_relay_ip', ip);
  localStorage.setItem('opsview_relay_port', port);
  localStorage.setItem('opsview_pin', pin);
  localStorage.setItem('opsview_proto', proto);

  setStatus('connecting', 'Connecting...');

  if (ws) { ws.close(); ws = null; }

  try {
    ws = new WebSocket(url);
    ws.binaryType = 'arraybuffer';
  } catch (e) {
    setStatus('error', 'Invalid URL');
    showError('Invalid WebSocket URL');
    return;
  }

  ws.onopen = () => {
    connected = true;
    reconnectAttempts = 0;
    clearTimeout(reconnectTimer);
    setStatus('connected', 'Connected');
    document.getElementById('connectBtn').textContent = 'Disconnect';
    document.getElementById('connectBtn').classList.add('!from-rose-600', '!to-rose-500');

    // Send OVP HELLO + AUTH
    const hello = {
      role: 'watcher',
      client: 'opsview-web',
      client_version: '0.2.0',
      supports: ['zstd'],
      want_profile: null
    };
    sendOVPMessage(MSG_HELLO, JSON.stringify(hello));

    const auth = { token: pin };
    sendOVPMessage(MSG_AUTH, JSON.stringify(auth));
  };

  ws.onmessage = (event) => {
    if (!(event.data instanceof ArrayBuffer)) return;
    const buf = event.data;
    bytesReceived += buf.byteLength;

    if (buf.byteLength < OVP_HEADER_SIZE) return;
    const view = new DataView(buf);
    const magic = view.getUint32(0, true);
    if (magic !== OVP_MAGIC) return;

    const msgType = view.getUint16(6, true);
    const payloadLen = view.getUint32(8, true);

    if (msgType === MSG_FRAME_DELTA) {
      handleFrameDelta(buf, OVP_HEADER_SIZE, payloadLen);
    } else if (msgType === MSG_ERROR) {
      const payload = new Uint8Array(buf, OVP_HEADER_SIZE, payloadLen);
      const text = new TextDecoder().decode(payload);
      try {
        const err = JSON.parse(text);
        const msg = `Error ${err.code || ''}: ${err.message || text}`;
        console.error('[ovp]', msg);
        setStatus('error', err.message || 'Error');
        showError(msg);
      } catch (e) {
        showError('Server error: ' + text);
      }
    } else if (msgType === MSG_SURV_CONFIG) {
      const payload = new Uint8Array(buf, OVP_HEADER_SIZE, payloadLen);
      const text = new TextDecoder().decode(payload);
      try {
        const cfg = JSON.parse(text);
        handleSurvConfig(cfg);
      } catch (e) { console.error('[surv] parse error:', e); }
    } else if (msgType === MSG_HEARTBEAT) {
      // keepalive
    }
  };

  ws.onclose = () => {
    connected = false;
    document.getElementById('connectBtn').textContent = 'Connect';
    document.getElementById('connectBtn').classList.remove('!from-rose-600', '!to-rose-500');

    if (reconnectAttempts < MAX_RECONNECT) {
      reconnectAttempts++;
      setStatus('connecting', `Reconnecting (${reconnectAttempts}/${MAX_RECONNECT})...`);
      reconnectTimer = setTimeout(connect, 3000);
    } else {
      setStatus('error', 'Disconnected');
    }
    ws = null;
  };

  ws.onerror = () => {
    setStatus('error', 'Connection error');
  };
}

function disconnect() {
  reconnectAttempts = MAX_RECONNECT; // prevent auto-reconnect
  clearTimeout(reconnectTimer);
  if (ws) { ws.close(); ws = null; }
  connected = false;
  setStatus('error', 'Disconnected');
  document.getElementById('connectBtn').textContent = 'Connect';
  document.getElementById('connectBtn').classList.remove('!from-rose-600', '!to-rose-500');
}

// --- OVP Message send ---
function sendOVPMessage(type, jsonStr) {
  if (!ws || ws.readyState !== WebSocket.OPEN) return;
  const payload = new TextEncoder().encode(jsonStr);
  const msg = new ArrayBuffer(OVP_HEADER_SIZE + payload.length);
  const view = new DataView(msg);
  view.setUint32(0, OVP_MAGIC, true);
  view.setUint16(4, 1, true);
  view.setUint16(6, type, true);
  view.setUint32(8, payload.length, true);
  new Uint8Array(msg, OVP_HEADER_SIZE).set(payload);
  ws.send(msg);
}

// --- Frame Delta ---
function handleFrameDelta(buffer, offset, payloadLen) {
  if (payloadLen < FRAME_DELTA_HEADER_SIZE) return;

  const view = new DataView(buffer, offset);
  let pos = 0;

  pos += 4; // seq
  pos += 8; // ts_ms
  pos += 2; // profile
  const width = view.getUint16(pos, true); pos += 2;
  const height = view.getUint16(pos, true); pos += 2;
  const tileSize = view.getUint16(pos, true); pos += 2;
  const tileCount = view.getUint16(pos, true); pos += 2;

  if (width !== screenWidth || height !== screenHeight) {
    screenWidth = width;
    screenHeight = height;
    canvas.width = width;
    canvas.height = height;
  }

  for (let i = 0; i < tileCount; i++) {
    if (pos + TILE_HEADER_SIZE > payloadLen) break;

    const tx = view.getUint16(pos, true); pos += 2;
    const ty = view.getUint16(pos, true); pos += 2;
    const codec = view.getUint16(pos, true); pos += 2;
    const dataLen = view.getUint32(pos, true); pos += 4;

    if (pos + dataLen > payloadLen) break;

    const compressedData = new Uint8Array(buffer, offset + pos, dataLen);
    pos += dataLen;
    tilesReceived++;

    let bgraData;
    try {
      if (codec === 1) {
        bgraData = fzstd.decompress(compressedData);
      } else {
        continue;
      }
    } catch (e) {
      continue;
    }

    const pixelX = tx * tileSize;
    const pixelY = ty * tileSize;
    const tileW = Math.min(tileSize, width - pixelX);
    const tileH = Math.min(tileSize, height - pixelY);
    if (tileW <= 0 || tileH <= 0) continue;

    const rgbaData = new Uint8ClampedArray(tileW * tileH * 4);
    for (let p = 0; p < tileW * tileH; p++) {
      const off = p * 4;
      rgbaData[off] = bgraData[off + 2];
      rgbaData[off + 1] = bgraData[off + 1];
      rgbaData[off + 2] = bgraData[off];
      rgbaData[off + 3] = bgraData[off + 3];
    }

    ctx.putImageData(new ImageData(rgbaData, tileW, tileH), pixelX, pixelY);
  }

  frameCount++;
  const now = performance.now();
  if (now - lastFpsTime >= 1000) {
    fps = frameCount;
    frameCount = 0;
    lastFpsTime = now;
  }
}

// --- UI helpers ---
function setStatus(state, text) {
  const dot = document.getElementById('statusDot');
  const label = document.getElementById('statusText');
  dot.className = 'status-dot ' + state;
  label.textContent = text;
}

function updateStats() {
  const el = document.getElementById('stats');
  if (!connected) { el.textContent = ''; return; }
  const kbps = ((bytesReceived * 8) / 1000).toFixed(0);
  el.textContent = `${fps} fps | ${tilesReceived} tiles | ${kbps} kbps`;
  bytesReceived = 0;
  tilesReceived = 0;
}

// ============================================
// Surv Section — DVR-grouped
// ============================================

// DVR data: { id, name, addr, port, user, pass, proto, quality, channels, channelCount }
let dvrs = [];
let activeDvrId = null;

function loadDVRData() {
  const saved = localStorage.getItem('opsview_dvrs');
  if (saved) try { dvrs = JSON.parse(saved); } catch(e) {}
  if (dvrs.length > 0 && !activeDvrId) activeDvrId = dvrs[0].id;
}

function saveDVRData() {
  localStorage.setItem('opsview_dvrs', JSON.stringify(dvrs));
}

function handleSurvConfig(cfg) {
  console.log('[surv] config received:', cfg);
  // Could merge remote DVR configs here in the future
}

// --- DVR Tabs ---
function renderDVRTabs() {
  const bar = document.getElementById('dvrTabs');
  const empty = document.getElementById('survEmpty');
  const grid = document.getElementById('survGrid');

  bar.innerHTML = '';

  if (dvrs.length === 0) {
    empty.style.display = 'flex';
    grid.style.display = 'none';
    return;
  }

  empty.style.display = 'none';
  grid.style.display = 'grid';

  dvrs.forEach(d => {
    const isActive = d.id === activeDvrId;
    const tab = document.createElement('div');
    tab.className = `flex items-center gap-1.5 px-3 py-1.5 text-[11px] rounded-md cursor-pointer transition-all whitespace-nowrap ${isActive ? 'text-cyan-300 bg-cyan-500/10 font-semibold' : 'text-slate-500 hover:text-slate-300 hover:bg-white/[0.04]'}`;
    tab.innerHTML = `
      <i data-lucide="hard-drive" class="w-3 h-3"></i>
      <span>${d.name}</span>
      <span class="text-[9px] ${isActive ? 'text-cyan-500' : 'text-slate-600'} ml-1">${d.channelCount}ch</span>
      <button class="edit-dvr ml-1 text-slate-600 hover:text-cyan-400 transition-colors text-[10px]" title="Edit">&#9998;</button>
      <button class="del-dvr ml-0.5 text-slate-600 hover:text-rose-400 transition-colors text-[10px]" title="Delete">&times;</button>
    `;
    tab.addEventListener('click', (e) => {
      if (e.target.closest('.del-dvr')) { e.stopPropagation(); deleteDVR(d.id); return; }
      if (e.target.closest('.edit-dvr')) { e.stopPropagation(); showEditDVRForm(d.id); return; }
      selectDVR(d.id);
    });
    bar.appendChild(tab);
  });
  lucide.createIcons();
}

function selectDVR(id) {
  stopAllStreams();
  activeDvrId = id;
  renderDVRTabs();
  renderSurvGrid();
}

function deleteDVR(id) {
  stopAllStreams();
  dvrs = dvrs.filter(d => d.id !== id);
  saveDVRData();
  if (activeDvrId === id) activeDvrId = dvrs.length > 0 ? dvrs[0].id : null;
  renderDVRTabs();
  renderSurvGrid();
}

// --- DVR Form ---
let editingDvrId = null;

function showAddDVRForm() {
  editingDvrId = null;
  document.getElementById('dvrFormTitle').textContent = 'Add DVR';
  document.getElementById('dvrName').value = 'DVR-' + (dvrs.length + 1);
  document.getElementById('dvrAddr').value = '';
  document.getElementById('dvrPort').value = '80';
  document.getElementById('dvrUser').value = '';
  document.getElementById('dvrPass').value = '';
  document.getElementById('dvrChannels').value = '16';
  document.getElementById('dvrProto').value = 'isapi';
  document.getElementById('dvrQuality').value = '02';
  document.getElementById('dvrForm').style.display = 'block';
  lucide.createIcons();
}

function showEditDVRForm(id) {
  const d = dvrs.find(x => x.id === id);
  if (!d) return;
  editingDvrId = id;
  document.getElementById('dvrFormTitle').textContent = 'Edit DVR';
  document.getElementById('dvrName').value = d.name;
  document.getElementById('dvrAddr').value = d.addr;
  document.getElementById('dvrPort').value = d.port;
  document.getElementById('dvrUser').value = d.user || '';
  document.getElementById('dvrPass').value = d.pass || '';
  document.getElementById('dvrChannels').value = d.channelCount;
  document.getElementById('dvrProto').value = d.proto;
  document.getElementById('dvrQuality').value = d.quality;
  document.getElementById('dvrForm').style.display = 'block';
  lucide.createIcons();
}

function hideDVRForm() {
  document.getElementById('dvrForm').style.display = 'none';
  editingDvrId = null;
}

function saveDVR() {
  const name = document.getElementById('dvrName').value.trim() || 'DVR';
  const addr = document.getElementById('dvrAddr').value.trim();
  const port = parseInt(document.getElementById('dvrPort').value) || 80;
  const user = document.getElementById('dvrUser').value.trim();
  const pass = document.getElementById('dvrPass').value.trim();
  const channelCount = parseInt(document.getElementById('dvrChannels').value) || 16;
  const proto = document.getElementById('dvrProto').value;
  const quality = document.getElementById('dvrQuality').value;

  if (!addr) { showError('Please enter DVR IP address'); return; }

  const auth = user ? `${encodeURIComponent(user)}:${encodeURIComponent(pass)}@` : '';

  // Generate channels
  const channels = [];
  for (let ch = 1; ch <= channelCount; ch++) {
    let url = '', type = 'snapshot';
    if (proto === 'isapi') {
      const chId = ch * 100 + parseInt(quality);
      url = `http://${auth}${addr}:${port}/ISAPI/Streaming/channels/${chId}/picture`;
      type = 'snapshot';
    } else {
      const rtspPort = port === 80 ? 554 : port;
      url = `rtsp://${auth}${addr}:${rtspPort}/Streaming/Channels/${ch}${quality}`;
      type = 'rtsp';
    }
    channels.push({ ch, name: `CH ${ch}`, url, type });
  }

  if (editingDvrId) {
    const d = dvrs.find(x => x.id === editingDvrId);
    if (d) {
      Object.assign(d, { name, addr, port, user, pass, proto, quality, channelCount, channels });
    }
  } else {
    dvrs.push({
      id: 'dvr-' + Date.now(),
      name, addr, port, user, pass, proto, quality, channelCount, channels
    });
  }

  saveDVRData();
  hideDVRForm();
  if (!activeDvrId) activeDvrId = dvrs[dvrs.length - 1].id;
  else if (editingDvrId) activeDvrId = editingDvrId;
  renderDVRTabs();
  renderSurvGrid();
}

// --- Surv Grid ---
function renderSurvGrid() {
  const grid = document.getElementById('survGrid');
  const dvr = dvrs.find(d => d.id === activeDvrId);
  if (!dvr || !dvr.channels) { grid.innerHTML = ''; return; }

  const cols = dvr.channels.length <= 4 ? 2 : dvr.channels.length <= 9 ? 3 : 4;
  grid.style.gridTemplateColumns = `repeat(${cols}, 1fr)`;

  grid.innerHTML = dvr.channels.map(ch => `
    <div class="surv-cell" id="cell-${dvr.id}-${ch.ch}">
      <div class="label">
        <span class="text-slate-300 font-medium">${ch.name}</span>
        <span class="text-slate-600 text-[9px] ml-2">${dvr.name}</span>
      </div>
    </div>
  `).join('');

  dvr.channels.forEach(ch => startStream(dvr, ch));
}

function startStream(dvr, ch) {
  const cellId = `${dvr.id}-${ch.ch}`;
  stopSurvPlayer(cellId);
  const cell = document.getElementById('cell-' + cellId);
  if (!cell) return;

  if (ch.type === 'hls') {
    const video = document.createElement('video');
    video.autoplay = true; video.muted = true; video.playsInline = true;
    cell.prepend(video);
    if (typeof Hls !== 'undefined' && Hls.isSupported()) {
      const hls = new Hls({ enableWorker: true, lowLatencyMode: true });
      hls.loadSource(ch.url);
      hls.attachMedia(video);
      hls.on(Hls.Events.MANIFEST_PARSED, () => video.play());
      survPlayers[cellId] = { hls };
    } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
      video.src = ch.url; video.play();
      survPlayers[cellId] = {};
    }
  } else if (ch.type === 'mjpeg') {
    const img = document.createElement('img');
    img.src = ch.url;
    cell.prepend(img);
    survPlayers[cellId] = {};
  } else if (ch.type === 'snapshot') {
    const img = document.createElement('img');
    const sep = ch.url.includes('?') ? '&' : '?';
    img.src = ch.url + sep + '_t=' + Date.now();
    img.onerror = () => { img.style.opacity = '0.2'; };
    img.onload = () => { img.style.opacity = '1'; };
    cell.prepend(img);
    const interval = setInterval(() => {
      const fresh = new Image();
      fresh.src = ch.url + sep + '_t=' + Date.now();
      fresh.onload = () => { img.src = fresh.src; img.style.opacity = '1'; };
    }, 2000);
    survPlayers[cellId] = { interval };
  }
}

function stopSurvPlayer(id) {
  const player = survPlayers[id];
  if (!player) return;
  if (player.hls) player.hls.destroy();
  if (player.interval) clearInterval(player.interval);
  delete survPlayers[id];
}

function stopAllStreams() {
  Object.keys(survPlayers).forEach(id => stopSurvPlayer(id));
}

// --- Init Surv ---
loadDVRData();
renderDVRTabs();
if (activeDvrId) renderSurvGrid();
