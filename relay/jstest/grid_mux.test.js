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

test('bounded pool balances subscribers across at most four physical connections',async()=>{
  for(const [requested,expected] of [[4,4],[100,4],[2.9,2],[0,1]]){
    const h=harness(),factory=h.context.createGridWSFactory('site-a',requested);
    const clients=Array.from({length:16},(_,i)=>factory.socket(streamURL(i+1)));
    assert.equal(h.sockets.length,expected);
    for(const socket of h.sockets)socket.open();
    await drain();
    const counts=h.sockets.map(socket=>socket.sent.filter(command=>command.op==='sub').length);
    assert.equal(counts.reduce((sum,count)=>sum+count,0),16);
    assert.ok(Math.max(...counts)-Math.min(...counts)<=1,`uneven pool subscriptions: ${counts}`);
    assert.ok(clients.every(client=>client.shared));
    factory.close();assert.ok(h.sockets.every(socket=>socket.readyState===3));
    assert.equal(h.timers.size,0);
  }
});

test('identical slot numbers on different pooled sockets cannot cross-route channel payloads',async()=>{
  const h=harness(),factory=h.context.createGridWSFactory('site-a',4);
  const clients=Array.from({length:8},(_,i)=>factory.socket(streamURL(i+1)));
  const deliveries=new Map(clients.map(client=>[client.stream,[]]));
  for(const client of clients)client.onmessage=event=>deliveries.get(client.stream).push(Array.from(new Uint8Array(event.data)));
  for(const socket of h.sockets)socket.open();await drain();
  for(let i=0;i<h.sockets.length;i++){
    const socket=h.sockets[i],subs=socket.sent.filter(command=>command.op==='sub');
    assert.equal(subs[0].slot,1,'fixture should reuse slot 1 across independent physical sockets');
    const before=[...deliveries.values()].reduce((sum,messages)=>sum+messages.length,0);
    socket.message(packet(subs[0].slot,[i+1,91]));
    assert.deepEqual(deliveries.get(subs[0].stream),[[i+1,91]]);
    assert.equal([...deliveries.values()].reduce((sum,messages)=>sum+messages.length,0),before+1);
    socket.message(packet(subs[1].slot,[i+1,92]));
    assert.deepEqual(deliveries.get(subs[1].stream),[[i+1,92]]);
  }
  factory.close();
});

test('released subscribers make their least-loaded pool preferred for new channels',async()=>{
  const h=harness(),factory=h.context.createGridWSFactory('site-a',4);
  const clients=Array.from({length:8},(_,i)=>factory.socket(streamURL(i+1)));
  for(const socket of h.sockets)socket.open();await drain();
  const released=h.sockets[2].sent.filter(command=>command.op==='sub');
  for(const command of released)clients.find(client=>client.stream===command.stream).close();
  factory.socket(streamURL(9));factory.socket(streamURL(10));await drain();
  assert.equal(h.sockets.length,4);
  const chosen=h.sockets[2].sent.filter(command=>command.op==='sub').map(command=>command.stream);
  assert.ok(chosen.includes('dvr1_ch9')&&chosen.includes('dvr1_ch10'));
  for(const socket of h.sockets.filter((_,i)=>i!==2)){
    assert.equal(socket.sent.some(command=>command.stream==='dvr1_ch9'||command.stream==='dvr1_ch10'),false);
  }
  factory.close();
});

test('a failed pre-open pool is avoided while another multiplex pool remains healthy',async()=>{
  const h=harness(),factory=h.context.createGridWSFactory('site-a',4);
  const initial=Array.from({length:4},(_,i)=>factory.socket(streamURL(i+1)));
  let firstClosed=0;initial[0].onclose=()=>firstClosed++;
  h.sockets[0].error();
  const next=factory.socket(streamURL(5));
  assert.equal(next.shared,true);
  assert.equal(h.sockets.length,4,'healthy pools should avoid a new raw fallback handshake');
  for(const socket of h.sockets.slice(1))socket.open();await drain();
  assert.equal(firstClosed,1);
  assert.equal(h.sockets[0].sent.length,0);
  assert.ok(h.sockets[1].sent.some(command=>command.op==='sub'&&command.stream==='dvr1_ch5'));
  factory.close();
});

test('all failed pools use caller-owned raw fallback and shared factory disposal prevents new starts',()=>{
  const h=harness(),factory=h.context.createGridWSFactory('site-a',4);
  for(let i=0;i<4;i++)factory.socket(streamURL(i+1));
  for(const socket of h.sockets)socket.error();
  const raw=factory.socket(streamURL(5));
  assert.equal(raw.url,streamURL(5));assert.equal(raw.shared,undefined);
  assert.equal(h.sockets.length,5);
  // Raw fallback keeps the existing playWS ownership contract; the caller closes
  // it, while the factory owns shared transports and prevents any future starts.
  raw.close();
  factory.close();factory.close();
  assert.ok(h.sockets.every(socket=>socket.readyState===3),'caller/factory cleanup left a connection alive');
  assert.throws(()=>factory.socket(streamURL(6)),/grid closed/);
  assert.equal(h.timers.size,0);
});

test('pool disposal closes unopened and opened children and prevents late callbacks from reconnecting',async()=>{
  const h=harness(),factory=h.context.createGridWSFactory('site-a',4);
  const clients=Array.from({length:12},(_,i)=>factory.socket(streamURL(i+1)));
  let closed=0,received=0;
  for(const client of clients){client.onclose=()=>closed++;client.onmessage=()=>received++;}
  h.sockets[0].open();h.sockets[2].open();await drain();
  factory.close();factory.close();
  assert.equal(closed,12);
  assert.ok(h.sockets.every(socket=>socket.readyState===3));
  for(const socket of h.sockets){socket.onopen();socket.message(packet(1,[4]));socket.onclose();}
  assert.equal(received,0);assert.equal(closed,12);
  assert.throws(()=>factory.socket(streamURL(13)),/grid closed/);
  assert.equal(h.sockets.length,4);assert.equal(h.timers.size,0);
});

test('flow acknowledgments require server capability and count complete binary wire packets cumulatively',async()=>{
  const h=harness(),factory=h.context.createGridWSFactory('site-a',1,true);
  const client=factory.socket(streamURL(1)),socket=h.sockets[0];
  const delivered=[];client.onmessage=event=>delivered.push(Array.from(new Uint8Array(event.data)));
  socket.open();await drain();
  socket.message(packet(client.slot,[10]));
  for(const text of ['not JSON','null','{"op":"status","window":8192}','{"op":"ack","received":9}'])socket.message(text);
  socket.message(packet(client.slot,[11,12]));
  assert.equal(socket.sent.filter(command=>command.op==='ack').length,0,'older relays must not receive unknown ACK commands');
  socket.message(JSON.stringify({op:'flow',window:8192}));
  assert.equal(socket.sent.filter(command=>command.op==='ack').length,0,'capability text does not consume binary byte credit');
  socket.message(packet(client.slot,[21,22,23]));
  socket.message(packet(client.slot,[31,32]));
  socket.message(JSON.stringify({op:'flow',window:4096}));
  socket.message(packet(client.slot,[41]));
  assert.deepEqual(socket.sent.filter(command=>command.op==='ack'),[
    {op:'ack',received:5},{op:'ack',received:9},{op:'ack',received:12},
  ]);
  assert.deepEqual(delivered,[[10],[11,12],[21,22,23],[31,32],[41]]);
  factory.close();
});

test('unknown and closed slots still acknowledge receipt without delivering stale media',async()=>{
  const h=harness(),factory=h.context.createGridWSFactory('site-a',1,true);
  const old=factory.socket(streamURL(1)),live=factory.socket(streamURL(2)),socket=h.sockets[0];
  let stale=0;const delivered=[];
  old.onmessage=()=>stale++;
  live.onmessage=event=>{
    assert.deepEqual(socket.sent.at(-1),{op:'ack',received:12},'ACK must not depend on downstream MSE append completion');
    delivered.push(Array.from(new Uint8Array(event.data)));
  };
  socket.open();await drain();socket.message(JSON.stringify({op:'flow',window:8192}));
  old.close();
  socket.message(packet(60000,[1,2]));
  socket.message(packet(old.slot,[3]));
  socket.message(packet(live.slot,[4,5,6]));
  assert.deepEqual(socket.sent.filter(command=>command.op==='ack').map(command=>command.received),[4,7,12]);
  assert.equal(stale,0);assert.deepEqual(delivered,[[4,5,6]]);
  factory.close();
});

test('new physical generation resets negotiation and byte total while late old frames remain ignored',async()=>{
  const h=harness(),factory=h.context.createGridWSFactory('site-a',1,true);
  const first=factory.socket(streamURL(1)),old=h.sockets[0];
  old.open();await drain();old.message(JSON.stringify({op:'flow',window:8192}));
  old.message(packet(first.slot,[1,2,3,4]));
  assert.deepEqual(old.sent.filter(command=>command.op==='ack'),[{op:'ack',received:6}]);
  old.error();
  const current=factory.socket(streamURL(2)),socket=h.sockets[1];
  let delivered=0;current.onmessage=()=>delivered++;
  socket.open();await drain();
  old.message(packet(current.slot,[90,91]));old.message(JSON.stringify({op:'flow',window:8192}));
  assert.equal(delivered,0);
  socket.message(packet(current.slot,[3]));
  assert.equal(socket.sent.filter(command=>command.op==='ack').length,0,'new physical connection inherited old capability');
  socket.message(JSON.stringify({op:'flow',window:8192}));
  socket.message(packet(current.slot,[4,5]));
  assert.deepEqual(socket.sent.filter(command=>command.op==='ack'),[{op:'ack',received:4}]);
  assert.equal(old.sent.filter(command=>command.op==='ack').length,1);
  assert.equal(delivered,2);factory.close();
});

test('bounded physical pools negotiate and accumulate flow credit independently',async()=>{
  const h=harness(),factory=h.context.createGridWSFactory('site-a',4,true);
  const clients=Array.from({length:4},(_,i)=>factory.socket(streamURL(i+1)));
  for(const socket of h.sockets)socket.open();await drain();
  for(const i of [0,1,3])h.sockets[i].message(JSON.stringify({op:'flow',window:8192}));
  h.sockets[0].message(packet(clients[0].slot,[1]));
  h.sockets[1].message(packet(clients[1].slot,[2,3,4]));
  h.sockets[0].message(packet(clients[0].slot,[5,6]));
  h.sockets[2].message(packet(clients[2].slot,[7,8,9,10]));
  h.sockets[3].message(packet(clients[3].slot,[11,12,13,14]));
  assert.deepEqual(h.sockets.map(socket=>socket.sent.filter(command=>command.op==='ack').map(command=>command.received)),[[3,7],[5],[],[6]]);
  h.sockets[2].message(JSON.stringify({op:'flow',window:8192}));
  h.sockets[2].message(packet(clients[2].slot,[15,16]));
  assert.deepEqual(h.sockets[2].sent.filter(command=>command.op==='ack'),[{op:'ack',received:4}]);
  factory.close();
});

test('failed ACK write closes the shared generation once and never dispatches unacknowledged media',async()=>{
  const h=harness(),factory=h.context.createGridWSFactory('site-a',1,true);
  const client=factory.socket(streamURL(1)),socket=h.sockets[0];
  let closed=0,delivered=0;client.onclose=()=>closed++;client.onmessage=()=>delivered++;
  socket.open();await drain();socket.message(JSON.stringify({op:'flow',window:8192}));
  const send=socket.send.bind(socket);
  socket.send=data=>{if(JSON.parse(data).op==='ack')throw new Error('send failed');send(data);};
  socket.message(packet(client.slot,[1]));
  assert.equal(closed,1);assert.equal(delivered,0);assert.equal(socket.readyState,3);
  socket.message(packet(client.slot,[2]));socket.onclose();
  assert.equal(closed,1);assert.equal(delivered,0);
  factory.close();assert.equal(h.timers.size,0);
});

test('default bounded pools do not request receipt flow or ACK ordinary legacy media',async()=>{
  const h=harness(),factory=h.context.createGridWSFactory('site a',4);
  for(let i=0;i<8;i++)factory.socket(streamURL(i+1));
  assert.equal(h.sockets.length,4);
  for(const socket of h.sockets){
    const url=new URL(socket.url);
    assert.equal(url.searchParams.get('agent'),'site a');
    assert.equal(url.searchParams.has('flow'),false);
    socket.open();
  }
  await drain();
  for(const socket of h.sockets){
    const subscription=socket.sent.find(command=>command.op==='sub');
    socket.message(packet(subscription.slot,[1,2,3]));
    assert.equal(socket.sent.some(command=>command.op==='ack'),false);
  }
  factory.close();assert.equal(h.timers.size,0);
});

test('explicit receipt flow opt-in propagates to every child of the bounded pool',async()=>{
  const h=harness(),factory=h.context.createGridWSFactory('site-a',4,true);
  for(let i=0;i<8;i++)factory.socket(streamURL(i+1));
  assert.equal(h.sockets.length,4);
  for(const socket of h.sockets){
    assert.equal(new URL(socket.url).searchParams.get('flow'),'1');
    socket.open();
  }
  await drain();
  for(const socket of h.sockets){
    socket.message(JSON.stringify({op:'flow',window:8192}));
    const subscription=socket.sent.find(command=>command.op==='sub');
    socket.message(packet(subscription.slot,[5,6]));
    assert.deepEqual(socket.sent.filter(command=>command.op==='ack'),[{op:'ack',received:4}]);
  }
  factory.close();assert.equal(h.timers.size,0);
});

test('truthy non-boolean receipt flow options never opt in implicitly',()=>{
  for(const option of [undefined,null,false,0,1,'true','1',{},[],new Boolean(true)]){
    const h=harness(),factory=h.context.createGridWSFactory('site-a',4,option);
    for(let i=0;i<4;i++)factory.socket(streamURL(i+1));
    assert.equal(h.sockets.length,4);
    assert.ok(h.sockets.every(socket=>!new URL(socket.url).searchParams.has('flow')),'only the boolean true may enable receipt flow');
    factory.close();assert.equal(h.timers.size,0);
  }
});
