// OpsView Web Viewer
// Connects to relay via WebSocket, receives OVP tile deltas, renders on Canvas.

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

// --- Init ---
document.addEventListener('DOMContentLoaded', () => {
  canvas = document.getElementById('screen');
  ctx = canvas.getContext('2d');

  // Load saved settings
  const savedIp = localStorage.getItem('opsview_relay_ip');
  const savedPort = localStorage.getItem('opsview_relay_port');
  const savedPin = localStorage.getItem('opsview_pin');
  if (savedIp) document.getElementById('relayIp').value = savedIp;
  if (savedPort) document.getElementById('relayPort').value = savedPort;
  if (savedPin) document.getElementById('pin').value = savedPin;

  // Stats update loop
  setInterval(updateStats, 1000);
});

// --- Connection ---
function toggleConnection() {
  if (connected) {
    disconnect();
  } else {
    connect();
  }
}

function connect() {
  const ip = document.getElementById('relayIp').value.trim();
  const port = document.getElementById('relayPort').value.trim();
  const pin = document.getElementById('pin').value.trim();

  if (!ip || !port) return;

  const url = `ws://${ip}:${port}/watch`;

  // Save settings
  localStorage.setItem('opsview_relay_ip', ip);
  localStorage.setItem('opsview_relay_port', port);
  localStorage.setItem('opsview_pin', pin);

  setStatus('connecting', 'Connecting...');

  try {
    ws = new WebSocket(url);
    ws.binaryType = 'arraybuffer';
  } catch (e) {
    setStatus('error', 'Invalid URL');
    return;
  }

  ws.onopen = () => {
    // Send HELLO
    const hello = {
      role: 'watcher',
      client: 'opsview-web',
      client_version: '0.1.0',
      supports: ['zstd'],
      want_profile: null
    };
    sendOVPMessage(MSG_HELLO, JSON.stringify(hello));

    // Send AUTH
    const auth = { token: pin };
    sendOVPMessage(MSG_AUTH, JSON.stringify(auth));

    connected = true;
    setStatus('connected', 'Connected');
    document.getElementById('connectBtn').textContent = 'Disconnect';
    document.getElementById('connectBtn').classList.add('disconnect');
  };

  ws.onmessage = (event) => {
    if (event.data instanceof ArrayBuffer) {
      handleBinaryMessage(event.data);
    }
  };

  ws.onclose = () => {
    connected = false;
    setStatus('error', 'Disconnected');
    document.getElementById('connectBtn').textContent = 'Connect';
    document.getElementById('connectBtn').classList.remove('disconnect');
    ws = null;
  };

  ws.onerror = () => {
    setStatus('error', 'Connection error');
  };
}

function disconnect() {
  if (ws) {
    ws.close();
    ws = null;
  }
  connected = false;
  setStatus('error', 'Disconnected');
  document.getElementById('connectBtn').textContent = 'Connect';
  document.getElementById('connectBtn').classList.remove('disconnect');
}

// --- OVP Message handling ---
function sendOVPMessage(type, jsonStr) {
  const payload = new TextEncoder().encode(jsonStr);
  const msg = new ArrayBuffer(OVP_HEADER_SIZE + payload.length);
  const view = new DataView(msg);

  view.setUint32(0, OVP_MAGIC, true);
  view.setUint16(4, 1, true); // version
  view.setUint16(6, type, true);
  view.setUint32(8, payload.length, true);

  new Uint8Array(msg, OVP_HEADER_SIZE).set(payload);
  ws.send(msg);
}

function handleBinaryMessage(buffer) {
  bytesReceived += buffer.byteLength;

  if (buffer.byteLength < OVP_HEADER_SIZE) return;

  const view = new DataView(buffer);
  const magic = view.getUint32(0, true);
  if (magic !== OVP_MAGIC) return;

  const msgType = view.getUint16(6, true);
  const payloadLen = view.getUint32(8, true);

  if (msgType === MSG_FRAME_DELTA) {
    handleFrameDelta(buffer, OVP_HEADER_SIZE, payloadLen);
  } else if (msgType === MSG_ERROR) {
    const payload = new Uint8Array(buffer, OVP_HEADER_SIZE, payloadLen);
    const text = new TextDecoder().decode(payload);
    try {
      const err = JSON.parse(text);
      console.error('[ovp] error:', err.code, err.message);
      setStatus('error', `Error: ${err.message}`);
    } catch (e) {
      console.error('[ovp] error:', text);
    }
  } else if (msgType === MSG_HEARTBEAT) {
    // keepalive, no action needed
  }
}

function handleFrameDelta(buffer, offset, payloadLen) {
  if (payloadLen < FRAME_DELTA_HEADER_SIZE) return;

  const view = new DataView(buffer, offset);
  let pos = 0;

  const seq = view.getUint32(pos, true); pos += 4;
  const tsMs = Number(view.getBigUint64(pos, true)); pos += 8;
  const profile = view.getUint16(pos, true); pos += 2;
  const width = view.getUint16(pos, true); pos += 2;
  const height = view.getUint16(pos, true); pos += 2;
  const tileSize = view.getUint16(pos, true); pos += 2;
  const tileCount = view.getUint16(pos, true); pos += 2;

  // Resize canvas if needed
  if (width !== screenWidth || height !== screenHeight) {
    screenWidth = width;
    screenHeight = height;
    canvas.width = width;
    canvas.height = height;
  }

  // Process tiles
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

    // Decompress
    let bgraData;
    try {
      if (codec === 1) {
        // zstd compressed BGRA
        bgraData = fzstd.decompress(compressedData);
      } else {
        console.warn('[ovp] unknown codec:', codec);
        continue;
      }
    } catch (e) {
      console.error('[ovp] decompress error:', e);
      continue;
    }

    // Calculate actual tile dimensions (edge tiles may be smaller)
    const pixelX = tx * tileSize;
    const pixelY = ty * tileSize;
    const tileW = Math.min(tileSize, width - pixelX);
    const tileH = Math.min(tileSize, height - pixelY);

    if (tileW <= 0 || tileH <= 0) continue;

    // Convert BGRA → RGBA for Canvas ImageData
    const rgbaData = new Uint8ClampedArray(tileW * tileH * 4);
    for (let p = 0; p < tileW * tileH; p++) {
      const srcOff = p * 4;
      const dstOff = p * 4;
      rgbaData[dstOff + 0] = bgraData[srcOff + 2]; // R ← B position
      rgbaData[dstOff + 1] = bgraData[srcOff + 1]; // G
      rgbaData[dstOff + 2] = bgraData[srcOff + 0]; // B ← R position
      rgbaData[dstOff + 3] = bgraData[srcOff + 3]; // A
    }

    const imgData = new ImageData(rgbaData, tileW, tileH);
    ctx.putImageData(imgData, pixelX, pixelY);
  }

  // FPS counter
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
  if (!connected) {
    el.textContent = '';
    return;
  }
  const kbps = ((bytesReceived * 8) / 1000).toFixed(0);
  el.textContent = `${fps} fps | ${tilesReceived} tiles | ${kbps} kbps`;
  bytesReceived = 0;
  tilesReceived = 0;
}
