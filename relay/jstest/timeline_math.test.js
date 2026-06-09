const test = require('node:test');
const assert = require('node:assert');
const M = require('../dashboard_assets/timeline_math.js');

test('timeToY maps t0->0 (top) and t1->railH (bottom)', () => {
  assert.strictEqual(M.timeToY(100, 100, 200, 600), 0);
  assert.strictEqual(M.timeToY(200, 100, 200, 600), 600);
  assert.strictEqual(M.timeToY(150, 100, 200, 600), 300);
});

test('yToTime is the inverse of timeToY', () => {
  const t0 = 1780900000, t1 = t0 + 7200, railH = 640;
  for (const t of [t0, t0 + 1000, t0 + 3600, t1]) {
    const y = M.timeToY(t, t0, t1, railH);
    assert.ok(Math.abs(M.yToTime(y, t0, t1, railH) - t) < 0.5);
  }
});

test('niceTickInterval picks a round interval near the target count', () => {
  assert.strictEqual(M.niceTickInterval(7200, 6), 1800);   // 2h -> 30m
  assert.strictEqual(M.niceTickInterval(600, 6), 120);     // 10m -> 2m
  assert.strictEqual(M.niceTickInterval(86400, 6), 21600); // 1d -> 6h
});

test('clampWindow shifts a future window back so t1==now, keeping span', () => {
  const now = 1780900000;
  const r = M.clampWindow(now - 100, now + 50, now);
  assert.strictEqual(r.t1, now);
  assert.strictEqual(r.t0, now - 150);
  const p = M.clampWindow(now - 200, now - 50, now);
  assert.strictEqual(p.t0, now - 200);
  assert.strictEqual(p.t1, now - 50);
});

test('firstTickTime returns the first interval-aligned time >= t0', () => {
  assert.strictEqual(M.firstTickTime(1000, 300), 1200);
  assert.strictEqual(M.firstTickTime(1200, 300), 1200);
});

test('segmentAt finds the segment covering t, else -1', () => {
  const segs = [{ start: 1000, dur: 300 }, { start: 1300, dur: 300 }];
  assert.strictEqual(M.segmentAt(segs, 1100), 0);
  assert.strictEqual(M.segmentAt(segs, 1300), 1);
  assert.strictEqual(M.segmentAt(segs, 1700), -1);
  assert.strictEqual(M.segmentAt(segs, 500), -1);
});
