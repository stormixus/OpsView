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
const MSG_SURV_CONFIG = 9;
const MSG_SURV_SNAPSHOT = 10;

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

  // Stats update loop
  setInterval(updateStats, 1000);
});

// --- Connection ---
function connect() {
  const url = document.getElementById('relayUrl').value.trim();
  const pin = document.getElementById('pin').value.trim();

  if (!url) return;

  setStatus('connecting', 'Connecting...');

  try {
    ws = new WebSocket(url);
    ws.binaryType = 'arraybuffer';
  } catch (e) {
    setStatus('error', 'Invalid URL');
    return;
  }

  ws.onopen = () => {
    const hello = {
      role: 'watcher',
      client: 'opsview-web',
      client_version: '0.3.7',
      supports: ['zstd'],
      want_profile: null
    };
    sendOVPMessage(MSG_HELLO, JSON.stringify(hello));

    const auth = { token: pin };
    sendOVPMessage(MSG_AUTH, JSON.stringify(auth));

    connected = true;
    setStatus('connected', 'Connected');
  };

  ws.onmessage = (event) => {
    if (event.data instanceof ArrayBuffer) {
      handleBinaryMessage(event.data);
    }
  };

  ws.onclose = () => {
    connected = false;
    setStatus('error', 'Disconnected');
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
  } else if (msgType === MSG_SURV_CONFIG) {
    const payload = new Uint8Array(buffer, OVP_HEADER_SIZE, payloadLen);
    const text = new TextDecoder().decode(payload);
    try {
      window.survConfig = JSON.parse(text);
      document.dispatchEvent(new CustomEvent('surv-config-updated', { detail: window.survConfig }));
      console.log('[surv] config received:', window.survConfig.dvrs?.length, 'DVRs,', window.survConfig.channels?.length, 'channels');
    } catch (e) {
      console.error('[surv] config parse error:', e);
    }
  } else if (msgType === MSG_SURV_SNAPSHOT) {
    const payload = new Uint8Array(buffer, OVP_HEADER_SIZE, payloadLen);
    const text = new TextDecoder().decode(payload);
    try {
      const resp = JSON.parse(text);
      const cb = window._survSnapshotCallbacks && window._survSnapshotCallbacks[resp.req_id];
      if (cb) {
        delete window._survSnapshotCallbacks[resp.req_id];
        if (resp.error) {
          cb(null, resp.error);
        } else {
          cb(resp.data, null);
        }
      }
    } catch (e) {
      console.error('[surv] snapshot response parse error:', e);
    }
  }
}

// Request a snapshot via WebSocket proxy (agent relay)
function requestSnapshotProxy(dvrId, chNum, callback) {
  if (!ws || !connected) {
    callback(null, 'not connected');
    return;
  }
  const reqId = 'snap_' + Date.now() + '_' + Math.random().toString(36).substr(2, 6);
  if (!window._survSnapshotCallbacks) window._survSnapshotCallbacks = {};
  window._survSnapshotCallbacks[reqId] = callback;

  // Timeout after 15 seconds
  setTimeout(() => {
    if (window._survSnapshotCallbacks && window._survSnapshotCallbacks[reqId]) {
      delete window._survSnapshotCallbacks[reqId];
      callback(null, 'timeout');
    }
  }, 15000);

  const req = JSON.stringify({ req_id: reqId, dvr_id: dvrId, ch_num: chNum });
  sendOVPMessage(MSG_SURV_SNAPSHOT, req);
}

// Frames are drawn through this promise chain so tiles never apply out of order
// while JPEG decode (async, variable latency) is in flight.
let frameChain = Promise.resolve();

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

  // Collect tiles; the actual draw runs asynchronously (JPEG native decode).
  const tiles = [];
  for (let i = 0; i < tileCount; i++) {
    if (pos + TILE_HEADER_SIZE > payloadLen) break;

    const tx = view.getUint16(pos, true); pos += 2;
    const ty = view.getUint16(pos, true); pos += 2;
    const codec = view.getUint16(pos, true); pos += 2;
    const dataLen = view.getUint32(pos, true); pos += 4;

    if (pos + dataLen > payloadLen) break;

    // Copy out: we must not retain a view into the WebSocket frame buffer
    // across the async decode.
    const data = new Uint8Array(buffer, offset + pos, dataLen).slice();
    pos += dataLen;

    tilesReceived++;

    const pixelX = tx * tileSize;
    const pixelY = ty * tileSize;
    const tileW = Math.min(tileSize, width - pixelX);
    const tileH = Math.min(tileSize, height - pixelY);

    if (tileW <= 0 || tileH <= 0) continue;

    tiles.push({ codec, data, pixelX, pixelY, tileW, tileH });
  }

  frameChain = frameChain.then(() => drawTiles(tiles)).catch(() => {});

  // FPS counter
  frameCount++;
  const now = performance.now();
  if (now - lastFpsTime >= 1000) {
    fps = frameCount;
    frameCount = 0;
    lastFpsTime = now;
  }
}

// drawTiles decodes a frame's tiles (JPEG natively/async, zstd-BGRA on-thread)
// then blits them in order onto the canvas.
async function drawTiles(tiles) {
  const drawables = await Promise.all(tiles.map(async (tn) => {
    if (tn.codec === 2) { // CodecJPEG — native async decode, off the main thread
      try {
        const bmp = await createImageBitmap(new Blob([tn.data], { type: 'image/jpeg' }));
        return { bitmap: bmp, x: tn.pixelX, y: tn.pixelY };
      } catch (e) { return null; }
    }
    if (tn.codec === 1) { // CodecZstdRawBGRA — legacy path
      try {
        const bgra = fzstd.decompress(tn.data);
        const rgba = new Uint8ClampedArray(tn.tileW * tn.tileH * 4);
        for (let p = 0; p < tn.tileW * tn.tileH; p++) {
          const o = p * 4;
          rgba[o + 0] = bgra[o + 2]; // R ← B position
          rgba[o + 1] = bgra[o + 1]; // G
          rgba[o + 2] = bgra[o + 0]; // B ← R position
          rgba[o + 3] = bgra[o + 3]; // A
        }
        return { image: new ImageData(rgba, tn.tileW, tn.tileH), x: tn.pixelX, y: tn.pixelY };
      } catch (e) {
        console.error('[ovp] decompress error:', e);
        return null;
      }
    }
    console.warn('[ovp] unknown codec:', tn.codec);
    return null;
  }));

  for (const d of drawables) {
    if (!d) continue;
    if (d.bitmap) {
      ctx.drawImage(d.bitmap, d.x, d.y);
      if (d.bitmap.close) d.bitmap.close();
    } else {
      ctx.putImageData(d.image, d.x, d.y);
    }
  }
}

// --- UI helpers ---
function setStatus(state, text) {
  const dot = document.getElementById('statusDot');
  if (!dot) return;
  // Map state to dot class: connected→on, connecting→warn, error→err
  dot.className = 'dot';
  if (state === 'connected') dot.classList.add('on');
  else if (state === 'connecting') dot.classList.add('warn');
  else if (state === 'error') dot.classList.add('err');
}

function updateStats() {
  const el = document.getElementById('stats');
  if (!el) return;
  if (!connected) {
    el.textContent = '';
    return;
  }
  const kbps = ((bytesReceived * 8) / 1000).toFixed(0);
  el.textContent = `${fps}fps ${kbps}kbps`;
  bytesReceived = 0;
  tilesReceived = 0;
}
