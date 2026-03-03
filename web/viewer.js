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
const LS_PANEL_KEY = 'opsview_web_panel';
const LS_SPLIT_RATIO_KEY = 'opsview_web_split_ratio';
const LS_AUTO_RECONNECT_KEY = 'opsview_web_auto_reconnect';

// --- State ---
let ws = null;
let connected = false;
let canvas, ctx;
let opsStage = null;
let screenWidth = 0, screenHeight = 0;
let activePanel = 'ops';
let splitRatio = parseFloat(localStorage.getItem(LS_SPLIT_RATIO_KEY) || '56');
const SPLIT_MIN = 28;
const SPLIT_MAX = 72;
let splitDragging = false;
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
let relayStreams = []; // from /api/surv/streams
let survMode = 'auto'; // 'auto' (relay HLS) or 'manual' (local DVR config)

// --- Init ---
document.addEventListener('DOMContentLoaded', () => {
  canvas = document.getElementById('screen');
  ctx = canvas.getContext('2d');
  opsStage = document.querySelector('.ops-stage');

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

  window.addEventListener('resize', fitCanvasToStage);
  window.addEventListener('mousemove', onSplitDragMove);
  window.addEventListener('mouseup', stopSplitDrag);
  requestAnimationFrame(fitCanvasToStage);

  setInterval(updateStats, 1000);
  applyI18n();

  const savedPanel = localStorage.getItem(LS_PANEL_KEY);
  if (savedPanel === 'surv' || savedPanel === 'split' || savedPanel === 'ops') {
    switchTab(savedPanel);
  } else {
    switchTab('ops');
  }

  // Restore last successful connection after refresh unless user explicitly disconnected.
  if (
    localStorage.getItem(LS_AUTO_RECONNECT_KEY) === '1' &&
    savedIp && savedPort
  ) {
    setTimeout(() => connect(), 200);
  }
});

// --- Tabs ---
function switchTab(panel) {
  activePanel = panel;
  localStorage.setItem(LS_PANEL_KEY, panel);

  document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
  document.querySelector(`.tab[data-panel="${panel}"]`).classList.add('active');

  const mainPanels = document.getElementById('mainPanels');
  const opsPanel = document.getElementById('panel-ops');
  const survPanel = document.getElementById('panel-surv');
  const split = panel === 'split';

  if (mainPanels) mainPanels.classList.toggle('split-layout', split);

  opsPanel.style.display = (panel === 'ops' || split) ? 'flex' : 'none';
  survPanel.style.display = (panel === 'surv' || split) ? 'flex' : 'none';

  applySplitRatio();
  if (panel === 'ops' || split) requestAnimationFrame(fitCanvasToStage);
}

function clampSplitRatio(ratio) {
  if (!Number.isFinite(ratio)) return 56;
  return Math.min(SPLIT_MAX, Math.max(SPLIT_MIN, ratio));
}

function applySplitRatio() {
  const mainPanels = document.getElementById('mainPanels');
  const opsPanel = document.getElementById('panel-ops');
  const survPanel = document.getElementById('panel-surv');
  if (!mainPanels || !opsPanel || !survPanel) return;

  splitRatio = clampSplitRatio(splitRatio);

  if (mainPanels.classList.contains('split-layout')) {
    opsPanel.style.flex = `0 0 ${splitRatio}%`;
    survPanel.style.flex = '1 1 0';
  } else {
    opsPanel.style.flex = '';
    survPanel.style.flex = '';
  }
}

function startSplitDrag(event) {
  const mainPanels = document.getElementById('mainPanels');
  if (!mainPanels || !mainPanels.classList.contains('split-layout')) return;

  splitDragging = true;
  document.body.style.cursor = 'col-resize';
  document.body.style.userSelect = 'none';
  event.preventDefault();
}

function onSplitDragMove(event) {
  if (!splitDragging || activePanel !== 'split') return;

  const mainPanels = document.getElementById('mainPanels');
  if (!mainPanels) return;

  const rect = mainPanels.getBoundingClientRect();
  if (rect.width <= 0) return;

  const x = event.clientX - rect.left;
  splitRatio = (x / rect.width) * 100;
  applySplitRatio();
  requestAnimationFrame(fitCanvasToStage);
}

function stopSplitDrag() {
  if (!splitDragging) return;
  splitDragging = false;
  document.body.style.cursor = '';
  document.body.style.userSelect = '';
  localStorage.setItem(LS_SPLIT_RATIO_KEY, String(clampSplitRatio(splitRatio)));
}

function fitCanvasToStage() {
  if (!canvas || !opsStage) return;

  const stageRect = opsStage.getBoundingClientRect();
  if (stageRect.width <= 0 || stageRect.height <= 0) return;

  // Exclude stage padding so we fit to the actual drawable viewport.
  const style = getComputedStyle(opsStage);
  const padX = parseFloat(style.paddingLeft) + parseFloat(style.paddingRight);
  const padY = parseFloat(style.paddingTop) + parseFloat(style.paddingBottom);
  const availW = Math.max(1, stageRect.width - padX);
  const availH = Math.max(1, stageRect.height - padY);

  if (screenWidth <= 0 || screenHeight <= 0) {
    canvas.style.width = `${Math.floor(availW)}px`;
    canvas.style.height = `${Math.floor(availH)}px`;
    return;
  }

  const sourceRatio = screenWidth / screenHeight;
  const stageRatio = availW / availH;

  let renderW, renderH;
  if (stageRatio > sourceRatio) {
    renderH = availH;
    renderW = renderH * sourceRatio;
  } else {
    renderW = availW;
    renderH = renderW / sourceRatio;
  }

  canvas.style.width = `${Math.floor(renderW)}px`;
  canvas.style.height = `${Math.floor(renderH)}px`;
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
    showError(t('enterIpPort'));
    return;
  }

  const url = `${proto}://${ip}:${port}/watch`;

  // Save settings
  localStorage.setItem('opsview_relay_ip', ip);
  localStorage.setItem('opsview_relay_port', port);
  localStorage.setItem('opsview_pin', pin);
  localStorage.setItem('opsview_proto', proto);

  setStatus('connecting', t('connecting'));

  if (ws) { ws.close(); ws = null; }

  try {
    ws = new WebSocket(url);
    ws.binaryType = 'arraybuffer';
  } catch (e) {
    setStatus('error', t('invalidUrl'));
    showError(t('invalidUrl'));
    return;
  }

  ws.onopen = () => {
    connected = true;
    reconnectAttempts = 0;
    clearTimeout(reconnectTimer);
    localStorage.setItem(LS_AUTO_RECONNECT_KEY, '1');
    setStatus('connected', t('connected'));
    document.getElementById('connectBtnText').textContent = t('disconnect');
    document.getElementById('connectBtn').classList.add('!from-rose-600', '!to-rose-500');

    // Fetch relay HLS streams on connect
    if (survMode === 'auto') {
      setTimeout(fetchRelayStreams, 1000);
    }

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
        setStatus('error', err.message || t('connError'));
        showError(msg);
      } catch (e) {
        showError(t('serverError') + ': ' + text);
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
    document.getElementById('connectBtnText').textContent = t('connect');
    document.getElementById('connectBtn').classList.remove('!from-rose-600', '!to-rose-500');

    if (reconnectAttempts < MAX_RECONNECT) {
      reconnectAttempts++;
      setStatus('connecting', `${t('reconnecting')} (${reconnectAttempts}/${MAX_RECONNECT})...`);
      reconnectTimer = setTimeout(connect, 3000);
    } else {
      setStatus('error', t('disconnected'));
    }
    ws = null;
  };

  ws.onerror = () => {
    setStatus('error', t('connError'));
  };
}

function disconnect() {
  reconnectAttempts = MAX_RECONNECT; // prevent auto-reconnect
  clearTimeout(reconnectTimer);
  localStorage.removeItem(LS_AUTO_RECONNECT_KEY);
  if (ws) { ws.close(); ws = null; }
  connected = false;
  setStatus('error', t('disconnected'));
  document.getElementById('connectBtnText').textContent = t('connect');
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
    fitCanvasToStage();
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
  if (survMode === 'auto') {
    fetchRelayStreams();
  }
}

// --- Relay HLS auto mode ---
function getHttpBase() {
  const proto = document.getElementById('proto').value === 'wss' ? 'https' : 'http';
  const ip = document.getElementById('relayIp').value.trim();
  const port = document.getElementById('relayPort').value.trim();
  return `${proto}://${ip}:${port}`;
}

function fetchRelayStreams() {
  const base = getHttpBase();
  fetch(`${base}/api/surv/streams`)
    .then(r => r.json())
    .then(streams => {
      relayStreams = streams || [];
      if (survMode === 'auto') renderRelayGrid();
    })
    .catch(err => console.log('[surv] streams fetch error:', err));
}

function renderRelayGrid() {
  const grid = document.getElementById('survGrid');
  const empty = document.getElementById('survEmpty');

  if (relayStreams.length === 0) {
    empty.style.display = 'flex';
    grid.style.display = 'none';
    empty.innerHTML = '<i data-lucide="camera-off" class="w-5 h-5"></i> ' + t('waitingStreams');
    lucide.createIcons();
    return;
  }

  empty.style.display = 'none';
  grid.style.display = 'grid';

  const cols = relayStreams.length <= 4 ? 2 : relayStreams.length <= 9 ? 3 : 4;
  grid.style.gridTemplateColumns = `repeat(${cols}, 1fr)`;

  stopAllStreams();

  grid.innerHTML = relayStreams.map(s => `
    <div class="surv-cell" id="cell-relay-${s.id}">
      <div class="label">
        <span class="text-slate-300 font-medium">${s.name || s.id}</span>
        <span class="text-emerald-500 text-[9px] ml-2">${t('live')}</span>
      </div>
    </div>
  `).join('');

  const base = getHttpBase();
  relayStreams.forEach(s => {
    const cellId = `relay-${s.id}`;
    const cell = document.getElementById('cell-' + cellId);
    if (!cell) return;

    const hlsUrl = `${base}/surv/${s.id}/index.m3u8`;
    const video = document.createElement('video');
    video.autoplay = true; video.muted = true; video.playsInline = true;
    cell.prepend(video);

    if (typeof Hls !== 'undefined' && Hls.isSupported()) {
      const hls = new Hls({ enableWorker: true, lowLatencyMode: true });
      hls.loadSource(hlsUrl);
      hls.attachMedia(video);
      hls.on(Hls.Events.MANIFEST_PARSED, () => video.play());
      hls.on(Hls.Events.ERROR, (_, data) => {
        if (data.fatal) console.error('[surv] HLS error:', data);
      });
      survPlayers[cellId] = { hls };
    } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
      video.src = hlsUrl; video.play();
      survPlayers[cellId] = {};
    }
  });
}

function setSurvMode(mode) {
  survMode = mode;
  stopAllStreams();

  document.querySelectorAll('.surv-mode-btn').forEach(b => b.classList.remove('active-mode'));
  const btn = document.querySelector(`.surv-mode-btn[data-mode="${mode}"]`);
  if (btn) btn.classList.add('active-mode');

  const dvrBar = document.getElementById('dvrManualBar');

  if (mode === 'auto') {
    dvrBar.style.display = 'none';
    fetchRelayStreams();
  } else {
    dvrBar.style.display = 'flex';
    renderDVRTabs();
    if (activeDvrId) renderSurvGrid();
  }
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
    tab.className = `dvr-tab${isActive ? ' active' : ''}`;
    tab.innerHTML = `
      <i data-lucide="hard-drive" class="w-3 h-3"></i>
      <span class="dvr-tab-name">${d.name}</span>
      <span class="dvr-tab-meta">${d.channelCount}ch</span>
      <button class="edit-dvr dvr-tab-action" title="Edit">&#9998;</button>
      <button class="del-dvr dvr-tab-action danger" title="Delete">&times;</button>
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
  document.getElementById('dvrFormTitle').textContent = t('addDvrTitle');
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
  document.getElementById('dvrFormTitle').textContent = t('editDvrTitle');
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

  if (!addr) { showError(t('enterIpPort')); return; }

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
// Start in auto mode by default (relay HLS)
setSurvMode('auto');
