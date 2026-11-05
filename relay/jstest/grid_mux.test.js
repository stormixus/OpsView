const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../dashboard_assets/wsplayer.js'), 'utf8');
const appSource = fs.readFileSync(path.join(__dirname, '../dashboard_assets/app.js'), 'utf8');
const drain = async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); };

class Events {
  constructor() { this.listeners = new Map(); }
  addEventListener(name, fn) { if (!this.listeners.has(name)) this.listeners.set(name, new Set()); this.listeners.get(name).add(fn); }
  removeEventListener(name, fn) { this.listeners.get(name)?.delete(fn); }
  emit(name) { for (const fn of [...(this.listeners.get(name) || [])]) fn(); }
}

function harness() {
  const sockets = [], mediaSources = [], timers = new Map();
  let now = 100, timerID = 0, failNext = false;
  class Socket {
    constructor(url) {
      if (failNext) { failNext = false; throw new Error('constructor failed'); }
      this.url = url; this.readyState = 0; this.sent = []; this.closeCount = 0;
      sockets.push(this);
    }
    send(data) { assert.equal(this.readyState, 1); this.sent.push(JSON.parse(data)); }
    open() { this.readyState = 1; this.onopen?.(); }
    message(data) { this.onmessage?.({ data }); }
    error() { this.readyState = 3; this.onerror?.(); this.onclose?.(); }
    close() { this.closeCount++; this.readyState = 3; this.onclose?.(); }
  }
  class MediaSource extends Events {
    constructor() { super(); this.readyState = 'closed'; this.buffers = []; mediaSources.push(this); }
    static isTypeSupported() { return true; }
    open() { this.readyState = 'open'; this.emit('sourceopen'); }
    endOfStream() { this.readyState = 'ended'; }
    addSourceBuffer() {
      const sb = new Events(); sb.updating = false; sb.appended = [];
      sb.appendBuffer = data => { sb.appended.push(Array.from(data)); sb.updating = true; };
      sb.complete = () => { sb.updating = false; sb.emit('updateend'); };
      this.buffers.push(sb); return sb;
    }
  }
  const context = vm.createContext({
    location: { protocol: 'https:', host: 'example.test' },
    window: { MediaSource }, navigator: { userAgent: 'desktop', platform: 'Linux' },
    WebSocket: Socket, Uint8Array, DataView, Promise,
    performance: { now: () => now++ },
    URL: { createObjectURL: () => 'blob:test', revokeObjectURL() {} },
    setTimeout(fn, delay) { timers.set(++timerID, { fn, at: now + delay }); return timerID; },
    clearTimeout(id) { timers.delete(id); },
  });
  vm.runInContext(source, context);
  function video() {
    const v = new Events(); v.dataset = {}; v.play = () => Promise.resolve();
    v.removeAttribute = () => {}; v.load = () => {};
    return v;
  }
  function advance(ms) {
    const target = now + ms;
    while (true) {
      const next = [...timers].filter(([, entry]) => entry.at <= target).sort((a, b) => a[1].at - b[1].at)[0];
      if (!next) break;
      const [id, entry] = next; now = entry.at; timers.delete(id); entry.fn();
    }
    now = target;
  }
  return { context, sockets, mediaSources, timers, video, advance, failNext() { failNext = true; } };
}

const streamURL = ch => `wss://example.test/surv/ws/site-a/dvr1_ch${ch}`;
function packet(slot, payload) {
  const data = new Uint8Array(payload.length + 2);
  new DataView(data.buffer).setUint16(0, slot); data.set(payload, 2); return data.buffer;
}
function init() { return [0, 0, 0, 15, 0x61, 0x76, 0x63, 0x43, 1, 0x64, 0, 0x28, 0xff, 0xe0, 0]; }

test('one physical connection sends per-slot controls and preserves each channel byte order', async () => {
  const h = harness(), factory = h.context.createGridWSFactory('site a');
  const a = factory.socket(streamURL(1)), b = factory.socket(streamURL(2));
  let opened = 0; const aData = [], bData = [];
  a.onopen = b.onopen = () => opened++;
  a.onmessage = event => aData.push(Array.from(new Uint8Array(event.data)));
  b.onmessage = event => bData.push(Array.from(new Uint8Array(event.data)));
  assert.equal(h.sockets.length, 1);
  assert.match(h.sockets[0].url, /live-grid-ws\?agent=site%20a$/);
  h.sockets[0].open(); await drain();
  assert.equal(opened, 2);
  assert.deepEqual(h.sockets[0].sent.map(c => [c.op, c.slot, c.stream]), [['sub', a.slot, 'dvr1_ch1'], ['sub', b.slot, 'dvr1_ch2']]);
  h.sockets[0].message(packet(a.slot, [0, 255, 1]));
  h.sockets[0].message(packet(b.slot, [3, 4]));
  h.sockets[0].message(packet(a.slot, [8, 9]));
  assert.deepEqual(aData, [[0, 255, 1], [8, 9]]);
  assert.deepEqual(bData, [[3, 4]]);
  h.sockets[0].message('not binary'); h.sockets[0].message(new Uint8Array([1]).buffer);
  h.sockets[0].message(packet(60000, [6]));
  assert.equal(aData.length, 2);
  factory.close(); assert.equal(h.sockets[0].closeCount, 1); assert.equal(h.timers.size, 0);
});

test('closed slots ignore delayed data and cancelled queued players never subscribe', async () => {
  const h = harness(), factory = h.context.createGridWSFactory('site-a');
  const runs = Array.from({ length: 6 }, (_, i) => {
    const v = h.video(), counts = { fail: 0, end: 0 };
    const player = h.context.playWS(v, streamURL(i + 1), () => counts.fail++, () => counts.end++, factory.socket);
    return { v, player, counts };
  });
  runs[4].player.close(); // Still queued behind the four virtual handshakes.
  runs[0].player.close(); // Virtual socket exists, physical handshake still pending.
  h.sockets[0].open(); await drain();
  assert.equal(h.sockets.length, 1);
  assert.deepEqual(h.sockets[0].sent.filter(c => c.op === 'sub').map(c => c.stream), ['dvr1_ch2', 'dvr1_ch3', 'dvr1_ch4', 'dvr1_ch6']);
  h.sockets[0].message(packet(1, init())); // Original slot was closed before open.
  assert.deepEqual(runs[0].counts, { fail: 0, end: 0 });
  assert.equal(runs[0].v.dataset.wsInitMs, undefined);
  assert.equal(runs[4].v.dataset.wsQueuedMs, undefined);
  for (const run of runs) run.player.close();
  factory.close(); h.advance(20000);
  for (const run of runs) assert.deepEqual(run.counts, { fail: 0, end: 0 });
  assert.equal(h.timers.size, 0);
});

test('virtual close sends unsubscribe and does not reuse a slot on the same physical generation', async () => {
  const h = harness(), factory = h.context.createGridWSFactory('site-a');
  const a = factory.socket(streamURL(1)); h.sockets[0].open(); await drain();
  let delivered = 0; a.onmessage = () => delivered++;
  a.close(); a.close();
  const b = factory.socket(streamURL(2)); b.onmessage = () => delivered++;
  await drain();
  assert.notEqual(a.slot, b.slot);
  assert.equal(h.sockets[0].sent.filter(c => c.op === 'unsub' && c.slot === a.slot).length, 1);
  h.sockets[0].message(packet(a.slot, [1])); assert.equal(delivered, 0);
  h.sockets[0].message(packet(b.slot, [2])); assert.equal(delivered, 1);
  b.close(); h.advance(1001);
  assert.equal(h.sockets[0].closeCount, 1); factory.close();
});

test('physical death fans out once and new subscriptions reconnect without accepting old-socket data', async () => {
  const h = harness(), factory = h.context.createGridWSFactory('site-a');
  const a = factory.socket(streamURL(1)), b = factory.socket(streamURL(2));
  let closed = 0; a.onclose = b.onclose = () => closed++;
  const old = h.sockets[0]; old.open(); await drain(); old.error(); old.onclose();
  assert.equal(closed, 2);
  const next = factory.socket(streamURL(3)); let received = 0; next.onmessage = () => received++;
  assert.equal(h.sockets.length, 2); assert.match(h.sockets[1].url, /live-grid-ws/);
  old.message(packet(next.slot, [9])); old.onopen(); assert.equal(received, 0);
  h.sockets[1].open(); await drain(); h.sockets[1].message(packet(next.slot, [8]));
  assert.equal(received, 1); factory.close();
});

test('physical startup has its own deadline despite virtual subscriber churn and falls back to legacy', () => {
  const h = harness(), factory = h.context.createGridWSFactory('site-a');
  const old = factory.socket(streamURL(1)); h.advance(4000);
  const newer = factory.socket(streamURL(2)); let closed = 0; newer.onclose = () => closed++;
  old.close(); h.advance(2001);
  assert.equal(closed, 1); assert.equal(h.sockets[0].readyState, 3);
  const legacy = factory.socket(streamURL(3));
  assert.equal(h.sockets[1].url, streamURL(3)); assert.equal(legacy.shared, undefined);
  legacy.close(); factory.close(); assert.equal(h.timers.size, 0);
});

test('pre-open physical failure selects legacy routes; synchronous construction failure leaves no stale subscriber', async () => {
  const h = harness(), factory = h.context.createGridWSFactory('site-a');
  h.failNext(); assert.throws(() => factory.socket(streamURL(1)), /constructor failed/);
  const next = factory.socket(streamURL(2)); h.sockets[0].open(); await drain();
  assert.deepEqual(h.sockets[0].sent.map(c => c.stream), ['dvr1_ch2']);
  factory.close();
  const other = harness(), fallback = other.context.createGridWSFactory('site-a');
  const v = fallback.socket(streamURL(1)); let failures = 0; v.onclose = () => failures++;
  other.sockets[0].error(); assert.equal(failures, 1);
  const native = fallback.socket(streamURL(2)); assert.equal(native.url, streamURL(2));
  native.close(); fallback.close();
});

test('uint16 slot rollover replaces physical generation before reusing a wire slot', async () => {
  const h = harness(), factory = h.context.createGridWSFactory('site-a');
  const first = factory.socket(streamURL(1)); let firstClosed = 0; first.onclose = () => firstClosed++;
  const old = h.sockets[0]; old.open(); await drain();
  for (let i = 2; i <= 65535; i++) factory.socket(streamURL(2)).close();
  const wrapped = factory.socket(streamURL(3)); let received = 0; wrapped.onmessage = () => received++;
  assert.equal(wrapped.slot, 1); assert.equal(h.sockets.length, 2); assert.equal(firstClosed, 1);
  old.message(packet(1, [77])); assert.equal(received, 0);
  h.sockets[1].open(); await drain(); h.sockets[1].message(packet(1, [88]));
  assert.equal(received, 1); factory.close();
});

function actualFunction(name) {
  const start = appSource.indexOf(`function ${name}(`); assert.ok(start >= 0);
  let level = 0, end = appSource.indexOf('{', start);
  for (; end < appSource.length; end++) {
    if (appSource[end] === '{') level++;
    if (appSource[end] === '}' && --level === 0) return appSource.slice(start, end + 1);
  }
  throw new Error('function boundary not found');
}

test('actual grid teardown closes shared and legacy players and cancels queued starts without restarting them', async () => {
  for (const legacy of [false, true]) {
    const h = harness(), factory = h.context.createGridWSFactory('site-a');
    if (legacy) { factory.socket(streamURL(9)); h.sockets[0].error(); }
    let restarts = 0;
    const cells = Array.from({ length: 6 }, (_, i) => {
      const v = h.video(), cell = { _wantLive: true, querySelector: () => v };
      cell._player = h.context.playWS(v, streamURL(i + 1), () => { if (h.context.gridSched) restarts++; }, () => { if (h.context.gridSched) restarts++; }, factory.socket);
      return cell;
    });
    const grid = { innerHTML: 'cells', dataset: { agent: 'site-a' } };
    Object.assign(h.context, { gridSched: { mux: factory }, $: () => grid, $$: () => cells, clearInterval() {} });
    for (const name of ['_cellCancelFirstFrame', '_cellStop', 'stopGridSched', 'stopLiveGrid']) vm.runInContext(actualFunction(name), h.context);
    h.context.stopLiveGrid(); await drain(); h.advance(20000);
    assert.equal(h.context.gridSched, null); assert.equal(restarts, 0);
    assert.equal(grid.innerHTML, ''); assert.equal(grid.dataset.agent, undefined);
    assert.ok(cells.every(cell => cell._player === null && cell._wantLive === false));
    assert.ok(h.sockets.every(socket => socket.readyState === 3));
    assert.equal(h.timers.size, 0);
    assert.throws(() => factory.socket(streamURL(1)), /grid closed/);
  }
});

test('shared physical death routes real players to fallback before init and reconnect after init',async()=>{
  for(const phase of ['pre-open','opened','initialized']){
    const h=harness(),factory=h.context.createGridWSFactory('site-a');
    const counts=[{fail:0,end:0},{fail:0,end:0}];
    const players=counts.map((count,i)=>h.context.playWS(h.video(),streamURL(i+1),()=>count.fail++,()=>count.end++,factory.socket));
    const physical=h.sockets[0];
    if(phase!=='pre-open'){physical.open();await drain();}
    if(phase==='initialized'){
      for(const ms of h.mediaSources)ms.open();
      for(const command of physical.sent.filter(c=>c.op==='sub'))physical.message(packet(command.slot,init()));
      assert.ok(players.every(player=>!player.starting()));
    }
    physical.error();physical.onclose();await drain();
    const expected=phase==='initialized'?{fail:0,end:1}:{fail:1,end:0};
    for(const count of counts)assert.deepEqual(count,expected);
    assert.ok(players.every(player=>!player.starting()));
    factory.close();assert.equal(h.timers.size,0);
  }
});
