const test = require('node:test');
const assert = require('node:assert');
const { liveWindow, nextStart } = require('../dashboard_assets/gridrotate.js');

test('liveWindow: whole pool when it fits under the cap', () => {
  assert.deepStrictEqual(liveWindow(5, 12, 0), [0, 1, 2, 3, 4]);
  assert.deepStrictEqual(liveWindow(12, 12, 0), [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11]);
  // start is ignored when everything fits
  assert.deepStrictEqual(liveWindow(3, 12, 7), [0, 1, 2]);
});

test('liveWindow: cap consecutive cells from start, wrapping', () => {
  assert.deepStrictEqual(liveWindow(36, 12, 0), [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11]);
  assert.deepStrictEqual(liveWindow(36, 12, 12), [12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23]);
  // wraps past the end
  assert.deepStrictEqual(liveWindow(10, 4, 8), [8, 9, 0, 1]);
});

test('liveWindow: empty / degenerate inputs', () => {
  assert.deepStrictEqual(liveWindow(0, 12, 0), []);
  assert.deepStrictEqual(liveWindow(5, 0, 0), []);
  assert.deepStrictEqual(liveWindow(5, -3, 0), []);
});

test('liveWindow: normalizes out-of-range / negative start', () => {
  assert.deepStrictEqual(liveWindow(36, 12, 36), [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11]);
  assert.deepStrictEqual(liveWindow(36, 12, -12), [24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35]);
});

test('nextStart: advances by cap and wraps cleanly for a divisible pool', () => {
  // 36 / 12 = 3 disjoint windows that cover the whole pool then repeat
  let s = 0;
  s = nextStart(36, 12, s); assert.strictEqual(s, 12);
  s = nextStart(36, 12, s); assert.strictEqual(s, 24);
  s = nextStart(36, 12, s); assert.strictEqual(s, 0);
});

test('nextStart: no rotation when the pool fits under the cap', () => {
  assert.strictEqual(nextStart(8, 12, 0), 0);
  assert.strictEqual(nextStart(12, 12, 0), 0);
});

test('rotation eventually covers every cell in a non-divisible pool', () => {
  const poolLen = 10, cap = 4;
  const seen = new Set();
  let s = 0;
  for (let tick = 0; tick < 10 && seen.size < poolLen; tick++) {
    liveWindow(poolLen, cap, s).forEach((i) => seen.add(i));
    s = nextStart(poolLen, cap, s);
  }
  assert.strictEqual(seen.size, poolLen, 'every cell got a live turn');
});
