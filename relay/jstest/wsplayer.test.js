const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../dashboard_assets/wsplayer.js'), 'utf8');

function avcInit() {
  // avcC decoder configuration: version, High profile, compatibility, level 4.0,
  // lengthSizeMinusOne and an empty SPS/PPS list. Prefix with its MP4 box size.
  return Uint8Array.from([0, 0, 0, 15, 0x61, 0x76, 0x63, 0x43,
    1, 0x64, 0, 0x28, 0xff, 0xe0, 0]);
}

function hevcInit(profile, compatibility, constraints, level, entry="hvc1") {
  const config = [1, profile, ...compatibility, ...constraints, level,
    0xf0, 0, 0xfc, 0xfd, 0xf8, 0xf8, 0, 0, 0x0f, 0];
  return Uint8Array.from([0,0,0,8,...[...entry].map(c=>c.charCodeAt(0)),0, 0, 0, config.length + 8, 0x68, 0x76, 0x63, 0x43, ...config]);
}

class FakeEvents {
  constructor() { this.listeners = new Map(); }
  addEventListener(name, callback) {
    if (!this.listeners.has(name)) this.listeners.set(name, new Set());
    this.listeners.get(name).add(callback);
  }
  removeEventListener(name, callback) { this.listeners.get(name)?.delete(callback); }
  emit(name) {
    for (const callback of [...(this.listeners.get(name) || [])]) callback({ type: name });
  }
}

function playerHarness(options = {}) {
  const mediaSources = [], sockets = [], order = [], revoked = [], timers = new Map();
  let timerID = 0, now = 100;
  class FakeSourceBuffer extends FakeEvents {
    constructor() {
      super();
      this.updating = false;
      this.appended = [];
      this.attempted = [];
      this.removed = [];
      this.buffered = { length: 0, end: () => 0 };
      this.quotaOnNextAppend = false;
    }
    appendBuffer(data) {
      const bytes = Array.from(data);
      this.attempted.push(bytes);
      if (this.updating) throw new Error('appendBuffer called during an update');
      if (this.quotaOnNextAppend) {
        this.quotaOnNextAppend = false;
        const error = new Error('buffer full');
        error.name = 'QuotaExceededError';
        throw error;
      }
      this.appended.push(bytes);
      this.updating = true;
    }
    remove(start, end) {
      if (this.updating) throw new Error('remove called during an update');
      this.removed.push([start, end]);
      this.updating = true;
    }
    complete() { this.updating = false; this.emit('updateend'); }
  }
  class FakeMediaSource extends FakeEvents {
    constructor() {
      super();
      this.readyState = 'closed';
      this.buffers = [];
      mediaSources.push(this);
    }
    static isTypeSupported(mime) {
      return options.supported !== false && /^video\/mp4; codecs="(?:avc1|hvc1|hev1)\./.test(mime);
    }
    open() { this.readyState = 'open'; order.push('sourceopen'); this.emit('sourceopen'); }
    addSourceBuffer(mime) {
      if (this.readyState !== 'open') throw new Error('MSE is not open');
      this.mime = mime;
      const buffer = new FakeSourceBuffer();
      this.buffers.push(buffer);
      return buffer;
    }
    endOfStream() { this.readyState = 'ended'; }
  }
  class FakeWebSocket {
    constructor(url) {
      this.url = url;
      this.closed = false;
      this.closeCount = 0;
      sockets.push(this);
      order.push('socket-created');
    }
    open() { this.onopen?.(); }
    sendFrame(bytes) { this.onmessage?.({ data: Uint8Array.from(bytes).buffer }); }
    close() {
      this.closeCount++;
      this.closed = true;
      this.onclose?.();
    }
  }
  const video = new FakeEvents();
  video.dataset = {};
  video.playCount = 0;
  video.play = () => { video.playCount++; return Promise.resolve(); };
  const callbacks = { failed: 0, ended: 0 };
  const context = vm.createContext({
    window: options.noMediaSource ? {} : { MediaSource: FakeMediaSource },
    navigator: { userAgent: 'desktop test browser', platform: 'Linux', maxTouchPoints: 0 },
    WebSocket: FakeWebSocket,
    Uint8Array,
    performance: { now: () => now++ },
    URL: { createObjectURL: () => 'blob:test', revokeObjectURL: url => revoked.push(url) },
    setTimeout: callback => { timers.set(++timerID, callback); return timerID; },
    clearTimeout: id => timers.delete(id),
  });
  vm.runInContext(source, context);
  const start = () => context.playWS(video, 'wss://example.test/stream',
    () => callbacks.failed++, () => callbacks.ended++);
  return { context, start, video, mediaSources, sockets, order, revoked, timers, callbacks };
}

test('codecFromInit reads AVC profile/compatibility/level from avcC', () => {
  const { context } = playerHarness();
  assert.equal(context.codecFromInit(avcInit()), 'avc1.640028');
  const baseline = avcInit();
  baseline[9] = 0x42;
  baseline[10] = 0xc0;
  baseline[11] = 0x1e;
  assert.equal(context.codecFromInit(baseline), 'avc1.42c01e');
  assert.equal(context.codecFromInit(Uint8Array.from([0, 1, 2, 3])), null);
});

test('codecFromInit reads HEVC compatibility bits, tier, level and trimmed constraints', () => {
  const { context } = playerHarness();
  // Common Main-profile HEVC RFC 6381 codec; compatibility 0x60000000 maps to 6.
  assert.equal(context.codecFromInit(hevcInit(1, [0x60, 0, 0, 0], [0xb0, 0, 0, 0, 0, 0], 93)), 'hvc1.1.6.L93.B0');
  // Profile space A, Main 10, high tier, internal zero constraint byte retained.
  assert.equal(context.codecFromInit(hevcInit(0x62, [0x10, 0, 0, 0], [0x80, 0, 2, 0, 0, 0], 153)), 'hvc1.A2.8.H153.80.00.02');
  assert.equal(context.codecFromInit(hevcInit(1, [0, 0, 0, 1], [0, 0, 0, 0, 0, 0], 120)), 'hvc1.1.80000000.L120');
});

test('socket opens before sourceopen and early init/media frames append in arrival order', () => {
  const h = playerHarness();
  const player = h.start();
  assert.equal(h.sockets.length, 1, 'socket handshake should not wait for MSE');
  const socket = h.sockets[0], ms = h.mediaSources[0];
  socket.open();
  socket.sendFrame(avcInit());
  socket.sendFrame([11, 12]);
  socket.sendFrame([21, 22]);
  assert.equal(ms.buffers.length, 0, 'no SourceBuffer can exist before sourceopen');
  assert.equal(typeof h.video.dataset.wsOpenMs, 'number');
  assert.equal(typeof h.video.dataset.wsInitMs, 'number');
  ms.open();
  const sb = ms.buffers[0];
  assert.deepEqual(h.order, ['socket-created', 'sourceopen']);
  assert.equal(sb.mode, 'sequence');
  assert.deepEqual(sb.appended, [Array.from(avcInit())]);
  sb.complete();
  assert.deepEqual(sb.appended[1], [11, 12]);
  sb.complete();
  assert.deepEqual(sb.appended[2], [21, 22]);
  assert.deepEqual(h.callbacks, { failed: 0, ended: 0 });
  player.close();
});

test('unsupported codec before initialization falls back once and closes the socket', () => {
  for (const options of [{ supported: false }, {}]) {
    const h = playerHarness(options);
    h.start();
    h.mediaSources[0].open();
    h.sockets[0].sendFrame(options.supported === false ? avcInit() : [0, 1, 2, 3]);
    h.sockets[0].onerror();
    assert.deepEqual(h.callbacks, { failed: 1, ended: 0 });
    assert.equal(h.sockets[0].closed, true);
    assert.equal(h.mediaSources[0].buffers.length, 0);
    assert.equal(h.timers.size, 0);
    assert.deepEqual(h.revoked, ['blob:test']);
  }
});

test('video decode error after initialization requests reconnect, not fallback', () => {
  const h = playerHarness();
  h.start();
  h.mediaSources[0].open();
  h.sockets[0].sendFrame(avcInit());
  h.video.emit('error');
  h.sockets[0].onerror();
  h.video.emit('error');
  assert.deepEqual(h.callbacks, { failed: 0, ended: 1 });
  assert.equal(h.sockets[0].closed, true);
  assert.equal(h.timers.size, 0);
});

test('deliberate close suppresses failure/reconnect and ignores late socket/media events', () => {
  for (const initialized of [false, true]) {
    const h = playerHarness();
    const player = h.start();
    if (initialized) {
      h.mediaSources[0].open();
      h.sockets[0].sendFrame(avcInit());
    }
    player.close();
    player.close();
    h.video.emit('error');
    h.sockets[0].onerror();
    h.sockets[0].onclose();
    h.sockets[0].sendFrame(avcInit());
    h.mediaSources[0].open();
    assert.deepEqual(h.callbacks, { failed: 0, ended: 0 });
    assert.equal(h.sockets[0].closeCount, 1);
    assert.equal(h.timers.size, 0);
    assert.deepEqual(h.revoked, ['blob:test']);
  }
});

test('pending queue has a fixed limit before sourceopen and after playback init', () => {
  for (const initialized of [false, true]) {
    const h = playerHarness();
    h.start();
    const ms = h.mediaSources[0], socket = h.sockets[0];
    if (initialized) ms.open();
    socket.sendFrame(avcInit());
    // Post-init the init frame is already in the updating SourceBuffer; pre-init
    // it occupies the first queue slot. Hold MSE progress while frames arrive.
    const remaining = initialized ? 512 : 511;
    for (let i = 0; i < remaining; i++) socket.sendFrame([1]);
    assert.deepEqual(h.callbacks, { failed: 0, ended: 0 });
    socket.sendFrame([2]);
    assert.deepEqual(h.callbacks, initialized ? { failed: 0, ended: 1 } : { failed: 1, ended: 0 });
    assert.equal(socket.closed, true);
    for (let i = 0; i < 20; i++) socket.sendFrame([3]);
    assert.equal(h.callbacks.failed + h.callbacks.ended, 1, 'overflow terminates once');
  }
});

test('quota exhaustion removes old media and retries the same queued fragment after updateend', () => {
  const h = playerHarness();
  const player = h.start();
  h.mediaSources[0].open();
  h.sockets[0].sendFrame(avcInit());
  const sb = h.mediaSources[0].buffers[0];
  sb.complete();
  sb.buffered = { length: 1, end: () => 20 };
  sb.quotaOnNextAppend = true;
  h.sockets[0].sendFrame([41, 42, 43]);
  assert.deepEqual(sb.removed, [[0, 16]]);
  assert.deepEqual(sb.appended, [Array.from(avcInit())], 'failed append must not consume the fragment');
  assert.deepEqual(h.callbacks, { failed: 0, ended: 0 });
  sb.complete();
  assert.deepEqual(sb.attempted.slice(1), [[41, 42, 43], [41, 42, 43]]);
  assert.deepEqual(sb.appended, [Array.from(avcInit()), [41, 42, 43]]);
  player.close();
});

test('missing MSE support and pre-init deadline both invoke fallback', () => {
  const unsupported = playerHarness({ noMediaSource: true });
  assert.equal(unsupported.start(), null);
  assert.deepEqual(unsupported.callbacks, { failed: 1, ended: 0 });
  assert.equal(unsupported.sockets.length, 0);
  const delayed = playerHarness();
  delayed.start();
  for (const callback of [...delayed.timers.values()]) callback();
  assert.deepEqual(delayed.callbacks, { failed: 1, ended: 0 });
  assert.equal(delayed.sockets[0].closed, true);
});

test('HEVC MIME agrees with the actual hev1 sample entry used by the relay',()=>{
 const {context}=playerHarness();
 assert.equal(context.codecFromInit(hevcInit(1,[0x60,0,0,0],[0xb0,0,0,0,0,0],93,'hev1')),'hev1.1.6.L93.B0');
});
