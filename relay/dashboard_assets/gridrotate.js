// gridrotate.js — pure rotation math for the live grid.
//
// Browsers cap how many H.264 decoders run at once (~16-25). A grid with more cells
// than that can't play every cell live without the losers going black (whoever grabs
// a decoder first wins, so the black set shuffles each reload). The grid instead keeps
// at most `cap` cells decoding at a time and rotates which ones are live, so every cell
// cycles through a live turn over time and the rest show a frozen last frame.
//
// liveWindow picks the cell indices that should be live for the window at `start`;
// nextStart advances the window by one cap-sized step. Both are pure so they're unit
// tested without a DOM (see jstest/gridrotate.test.js).

// liveWindow returns the indices that should be live: `cap` consecutive cells starting
// at `start`, wrapping around the pool. Returns every index when the pool fits under
// the cap, and an empty list when the pool is empty or cap<=0.
function liveWindow(poolLen, cap, start) {
  var out = [];
  if (poolLen <= 0 || cap <= 0) return out;
  if (cap >= poolLen) { for (var i = 0; i < poolLen; i++) out.push(i); return out; }
  start = ((start % poolLen) + poolLen) % poolLen; // normalize, tolerating negatives
  for (var k = 0; k < cap; k++) out.push((start + k) % poolLen);
  return out;
}

// nextStart advances the live window by one cap step, wrapping. When the whole pool
// fits under the cap the window never moves (nothing to rotate), so it stays at 0.
function nextStart(poolLen, cap, start) {
  if (poolLen <= 0 || cap <= 0 || cap >= poolLen) return 0;
  return (((start + cap) % poolLen) + poolLen) % poolLen;
}

if (typeof module !== 'undefined' && module.exports) { module.exports = { liveWindow, nextStart }; }
