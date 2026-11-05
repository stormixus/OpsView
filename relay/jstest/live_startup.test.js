const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { liveWindow, nextStart } = require('../dashboard_assets/gridrotate.js');
const source = fs.readFileSync(path.join(__dirname, '../dashboard_assets/app.js'), 'utf8');
const wsSource = fs.readFileSync(path.join(__dirname, '../dashboard_assets/wsplayer.js'), 'utf8');

function liveHarness({ mobile = false, frameCallbacks = true, failingRealWebSocket = false } = {}) {
  const timers = new Map(), intervals = new Map(), observers = [], players = [];
  const active = new Set();
  let nextTimer = 0, clock = 100, maxActive = 0, socketAttempts = 0;
  class Video {
    constructor(id) {
      this.id = id;
      this.dataset = {};
      this.frames = new Map();
      this.listeners = new Map();
      this.cancelled = [];
      this.nextFrame = 0;
      this.videoWidth = 320;
      this.videoHeight = 180;
      this.readyState = 2;
      this.loadCount = 0;
      this.currentTime=0;this.buffered={length:0};
      this.canPlayType=()=> 'maybe';
      this.play=()=>Promise.resolve();
      if (!frameCallbacks) this.requestVideoFrameCallback = undefined;
    }
    requestVideoFrameCallback(callback) {
      this.frames.set(++this.nextFrame, callback);
      return this.nextFrame;
    }
    cancelVideoFrameCallback(id) { this.cancelled.push(id); this.frames.delete(id); }
    addEventListener(name, callback, options = {}) {
      if (!this.listeners.has(name)) this.listeners.set(name, new Map());
      this.listeners.get(name).set(callback, options);
    }
    removeEventListener(name, callback) { this.listeners.get(name)?.delete(callback); }
    emit(name) {
      this[`on${name}`]?.();
      for (const [callback, options] of [...(this.listeners.get(name) || [])]) {
        if (options.once) this.listeners.get(name).delete(callback);
        callback();
      }
    }
    decodeFrame() {
      clock += 20;
      const entry = this.frames.entries().next().value;
      if (entry) { this.frames.delete(entry[0]); entry[1](clock, {}); }
      else this.emit('loadeddata');
    }
    removeAttribute(name) { if (name === 'src') this.src = ''; }
    load() { this.loadCount++; }
  }
  const makeCell = id => {
    const video = new Video(id);
    return {
      id, video, dataset: { path: `site/dvr1_ch${id}` }, canvas: null,
      querySelector(selector) {
        if (selector === 'video') return video;
        if (selector === 'canvas.cellfreeze') return this.canvas;
        return null;
      },
      insertBefore(canvas) { this.canvas = canvas; },
    };
  };
  class Observer {
    constructor(callback) { this.callback = callback; this.cells = []; observers.push(this); }
    observe(cell) { this.cells.push(cell); }
    disconnect() { this.cells = []; }
    enter(cells) { this.callback(cells.map(target => ({ target, isIntersecting: true }))); }
  }
  const context = vm.createContext({
    liveWindow, nextStart,
    _isMobile: () => mobile,
    up: { open: false },
    location: { protocol: 'https:', host: 'example.test', origin: 'https://example.test' },
    performance: { now: () => clock++ },
    Date:class extends Date {static now(){return clock;}},
    document: {
      hidden: false, addEventListener() {},
      createElement: () => ({ style: {}, getContext: () => ({ drawImage() {} }) }),
    },
    window: { addEventListener() {} },
    IntersectionObserver: Observer,
    setTimeout: (callback, delay) => { timers.set(++nextTimer, { callback, delay }); return nextTimer; },
    clearTimeout: id => timers.delete(id),
    setInterval: (callback, delay) => { intervals.set(++nextTimer, { callback, delay }); return nextTimer; },
    clearInterval: id => intervals.delete(id),
    playHLS: () => false,
    playWS(video, url, onFail, onEnd) {
      const player = { video, url, onFail, onEnd, closed: false, isStarting:true,
        starting(){return this.isStarting&&!this.closed;},
        close() { this.closed = true; active.delete(this); },
      };
      players.push(player);
      active.add(player);
      maxActive = Math.max(maxActive, active.size);
      return player;
    },
  });
  const start = source.indexOf('var LIVE_CAP_DESKTOP=');
  const end = source.indexOf('// Mobile browsers allow', start);
  assert.ok(start >= 0 && end > start, 'live scheduler source boundaries');
  vm.runInContext(source.slice(start, end), context);
  const hlsStart=source.indexOf('function playHLS(video, hlsUrl');
  vm.runInContext(source.slice(hlsStart,source.indexOf('\n',hlsStart)),context);
  if(failingRealWebSocket){
    context.window.MediaSource=class {
      constructor(){this.readyState='closed';}
      addEventListener(){}
    };
    context.navigator={userAgent:'desktop test browser',platform:'Linux',maxTouchPoints:0};
    context.URL={createObjectURL:()=> 'blob:test',revokeObjectURL(){}};
    context.WebSocket=class {constructor(){socketAttempts++;throw new Error('blocked socket constructor');}};
    vm.runInContext(wsSource,context);
  }

  return {
    context, makeCell, timers, intervals, observers, players, active,
    get maxActive() { return maxActive; },
    get socketAttempts(){return socketAttempts;},
    advanceClock(ms){clock+=ms;},
    runReconnect() {
      const [id, entry] = timers.entries().next().value;
      timers.delete(id);
      entry.callback();
    },
  };
}

test('first-picture startup and subsequent rotation preserve desktop/mobile decoder caps', () => {
  for (const mobile of [false, true]) {
    const h = liveHarness({ mobile });
    const cap = mobile ? 4 : 12;
    const cells = Array.from({ length: 30 }, (_, i) => h.makeCell(i + 1));
    h.context.startLiveGrid(cells);
    h.observers[0].enter(cells);
    assert.equal(h.context.liveCap(), cap);
    assert.equal(h.active.size, cap);
    cells[0].video.decodeFrame();
    assert.equal(h.active.size, cap);
    h.context.gridRotate(true);
    assert.equal(h.active.size, cap);
    assert.ok(h.maxActive <= cap, `startup briefly exceeded ${cap} decoders`);
  }
});

test('newly visible unseen cells get a decoder before cells with a seeded picture', () => {
  const h = liveHarness({ mobile: true });
  const seeded = [1, 2, 3, 4].map(h.makeCell);
  seeded.forEach(cell => { cell._hasFrame = true; });
  const unseen = h.makeCell(5);
  h.context.startLiveGrid([...seeded, unseen]);
  h.observers[0].enter(seeded);
  assert.equal(h.active.size, 4);
  h.observers[0].enter([unseen]);
  assert.equal(unseen._wantLive, true);
  assert.ok(unseen._player && !unseen._player.closed);
  assert.equal(seeded[3]._player, null);
  assert.equal(h.active.size, 4);
  assert.equal(h.maxActive, 4);
});

test('first actual decoded frame starts another unseen cell without waiting for dwell timer', () => {
  const h = liveHarness({ mobile: true });
  const cells = [1, 2, 3, 4, 5].map(h.makeCell);
  h.context.startLiveGrid(cells);
  h.observers[0].enter(cells);
  assert.equal(cells[4]._player, undefined);
  const firstPlayer = cells[0]._player;
  cells[0].video.decodeFrame();
  assert.equal(cells[0]._hasFrame, true);
  assert.ok(cells[0]._firstFrameAt > 0);
  assert.equal(typeof cells[0].video.dataset.viewportToFrameMs, 'number');
  assert.equal(firstPlayer.closed, true);
  assert.ok(cells[4]._player && !cells[4]._player.closed);
  assert.equal(h.active.size, 4);
  assert.equal(h.timers.size, 0, 'warming should not wait on a startup timeout');
  // The fake interval callbacks are deliberately never run in this test.
  assert.ok([...h.intervals.values()].some(interval => interval.delay === 5000));
});

test('reconnecting before first frame cancels native callback and ignores stale generation', () => {
  const h = liveHarness();
  const cell = h.makeCell(1);
  h.context.startLiveGrid([cell]);
  h.observers[0].enter([cell]);
  const [id, staleFrame] = cell.video.frames.entries().next().value;
  const oldPlayer = cell._player;
  h.context._cellReconnect(cell);
  assert.equal(oldPlayer.closed, true);
  assert.ok(cell.video.cancelled.includes(id));
  assert.equal(cell.video.frames.size, 0);
  staleFrame(); // A callback already posted to the event loop may still arrive.
  assert.notEqual(cell._hasFrame, true);
  assert.equal(cell.video.dataset.viewportToFrameMs, undefined);
  h.runReconnect();
  assert.ok(cell._player && cell._player !== oldPlayer);
  assert.equal(cell.video.frames.size, 1);
  staleFrame();
  assert.notEqual(cell._hasFrame, true);
  cell.video.decodeFrame();
  const firstAt = cell._firstFrameAt;
  assert.equal(cell._hasFrame, true);
  staleFrame();
  assert.equal(cell._firstFrameAt, firstAt);
});

test('fallback loadeddata listeners are removed across reconnects and stale callbacks are ignored', () => {
  const h = liveHarness({ frameCallbacks: false });
  const cell = h.makeCell(1);
  h.context.startLiveGrid([cell]);
  h.observers[0].enter([cell]);
  const staleListener = cell._firstData;
  assert.equal(cell.video.listeners.get('loadeddata').size, 1);
  h.context._cellReconnect(cell);
  assert.equal(cell.video.listeners.get('loadeddata').size, 0);
  staleListener();
  assert.notEqual(cell._hasFrame, true);
  h.runReconnect();
  assert.equal(cell.video.listeners.get('loadeddata').size, 1);
  cell.video.decodeFrame();
  assert.equal(cell._hasFrame, true);
  assert.equal(cell.video.listeners.get('loadeddata').size, 0);
});

test('_cellStop cancels first-frame work and reconnect timers for both video callback APIs', () => {
  for (const frameCallbacks of [false, true]) {
    const h = liveHarness({ frameCallbacks });
    const cell = h.makeCell(1);
    h.context.startLiveGrid([cell]);
    h.observers[0].enter([cell]);
    const staleFrame = frameCallbacks ? cell.video.frames.values().next().value : cell._firstData;
    h.context._cellReconnect(cell);
    assert.equal(h.timers.size, 1);
    h.context._cellStop(cell);
    assert.equal(h.timers.size, 0);
    assert.equal(cell._wantLive, false);
    assert.equal(cell._player, null);
    assert.equal(cell._frameCallback, null);
    assert.equal(cell._firstData, null);
    assert.equal(cell.video.frames.size, 0);
    assert.equal(cell.video.listeners.get('loadeddata')?.size || 0, 0);
    assert.ok(cell.video.loadCount > 0);
    staleFrame();
    assert.notEqual(cell._hasFrame, true);
  }
});

test('native HLS failure schedules reconnect immediately rather than waiting for watchdog',()=>{
  const h=liveHarness(),cell=h.makeCell(1);
  h.context.startLiveGrid([cell]);h.observers[0].enter([cell]);
  const wsPlayer=cell._player;
  wsPlayer.close();wsPlayer.onFail();
  assert.match(cell.video.src,/\/surv\/site\/dvr1_ch1\/index.m3u8$/);
  assert.equal(typeof cell.video.onerror,'function');
  assert.equal(h.timers.size,0);
  cell.video.emit('error');
  assert.equal(h.timers.size,1,'native HLS errors must schedule retry without a watchdog tick');
  assert.equal(cell.video.onerror,null,'native error handler should be one-shot');
  assert.equal(cell._player,null);
  cell.video.emit('error');
  assert.equal(h.timers.size,1);
  h.runReconnect();
  assert.ok(cell._player && cell._player!==wsPlayer);
});

test('starting and stopping a cell remove stale native HLS error handlers',()=>{
  const h=liveHarness(),cell=h.makeCell(1);
  let stale=0;
  cell.video.onerror=()=>{stale++;};
  h.context.startLiveGrid([cell]);h.observers[0].enter([cell]);
  assert.equal(cell.video.onerror,null);
  cell._player.close();cell._player.onFail();
  assert.equal(typeof cell.video.onerror,'function');
  h.context._cellStop(cell);
  assert.equal(cell.video.onerror,null);
  cell.video.emit('error');
  assert.equal(h.timers.size,0);
  h.context.gridRotate(false);
  assert.equal(cell.video.onerror,null);
  assert.equal(stale,0);
});

test('grid watchdog preserves starting players beyond eight seconds, then still detects established stalls',()=>{
  const h=liveHarness(),cell=h.makeCell(1);
  h.context.startLiveGrid([cell]);h.observers[0].enter([cell]);
  const player=cell._player;
  cell._liveProg={v:0,at:0};
  h.advanceClock(9000);h.context.gridWatchdog();
  assert.equal(player.closed,false);
  assert.equal(cell._player,player);
  assert.equal(h.timers.size,0);
  assert.ok(cell._liveProg.at>=9000);
  player.isStarting=false;
  h.advanceClock(9000);h.context.gridWatchdog();
  assert.equal(player.closed,true,'started streams must retain ordinary stall recovery');
  assert.equal(h.timers.size,1);
});

test('fullscreen watchdog preserves queued/starting playback and keeps its next tick scheduled',()=>{
  const h=liveHarness();let reconnects=0,isStarting=true;
  Object.assign(h.context,{
    up:{open:true,mode:'live',player:{starting:()=>isStarting},_liveProg:{v:0,at:0}},
    upVideo:{currentTime:0,buffered:{length:0}},
    upSyncLiveWindow(){},upEnsureTimeline(){},upRenderRail(){},
    upLiveReconnect(){reconnects++;},
  });
  const start=source.indexOf('function upLiveTick(){'),end=source.indexOf('\nfunction closePlayer(){',start);
  vm.runInContext(source.slice(start,end),h.context);
  h.advanceClock(9000);h.context.upLiveTick();
  assert.equal(reconnects,0);
  assert.ok(h.context.up._liveProg.at>=9000);
  assert.ok([...h.timers.values()].some(timer=>timer.delay===1000),'starting playback must keep ticking');
  isStarting=false;h.advanceClock(9000);h.context.upLiveTick();
  assert.equal(reconnects,1,'established fullscreen streams must still recover on a stall');
});

test('real playWS synchronous constructor failure leaves no closed handle blocking cell retries',()=>{
  const h=liveHarness({failingRealWebSocket:true}),cell=h.makeCell(1);
  cell.video.canPlayType=()=>''; // Native HLS unavailable; actual failure callback must retry.
  h.context.startLiveGrid([cell]);h.observers[0].enter([cell]);
  assert.equal(h.socketAttempts,1);
  assert.equal(cell._player,null,'a synchronously failed socket must not return a truthy closed player');
  assert.equal(h.timers.size,1);
  h.runReconnect();
  assert.equal(h.socketAttempts,2,'retry timer was blocked by a closed player handle');
  assert.equal(cell._player,null);
  assert.equal(h.timers.size,1,'second failure should schedule the next retry');
  h.runReconnect();
  assert.equal(h.socketAttempts,3);
  assert.equal(cell._player,null);
  h.context._cellStop(cell);
  assert.equal(h.timers.size,0);
});
