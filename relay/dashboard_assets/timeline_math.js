/* Pure timeline math for the unified player. No DOM. Loaded in the browser as a
   plain script (functions become window globals) and required by node for tests
   (module.exports at the bottom). Rail orientation: top = past (t0), bottom = now (t1). */
(function (root) {
  // "nice" tick intervals in seconds: 1s..1d.
  var NICE = [1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 21600, 43200, 86400];

  function timeToY(t, t0, t1, railH) {
    if (t1 === t0) return 0;
    return (t - t0) / (t1 - t0) * railH;
  }
  function yToTime(y, t0, t1, railH) {
    if (railH === 0) return t0;
    return t0 + (y / railH) * (t1 - t0);
  }
  function niceTickInterval(spanSec, targetTicks) {
    targetTicks = targetTicks || 6;
    var ideal = spanSec / targetTicks;
    for (var i = 0; i < NICE.length; i++) { if (NICE[i] >= ideal) return NICE[i]; }
    return NICE[NICE.length - 1];
  }
  function clampWindow(t0, t1, now) {
    if (t1 > now) { var span = t1 - t0; return { t0: now - span, t1: now }; }
    return { t0: t0, t1: t1 };
  }
  function firstTickTime(t0, intervalSec) {
    return Math.ceil(t0 / intervalSec) * intervalSec;
  }
  function segmentAt(segs, t) {
    for (var i = 0; i < segs.length; i++) {
      if (t >= segs[i].start && t < segs[i].start + segs[i].dur) return i;
    }
    return -1;
  }

  var api = { timeToY: timeToY, yToTime: yToTime, niceTickInterval: niceTickInterval,
              clampWindow: clampWindow, firstTickTime: firstTickTime, segmentAt: segmentAt };
  if (typeof module !== 'undefined' && module.exports) { module.exports = api; }
  else { for (var k in api) root[k] = api[k]; } // browser: expose as globals
})(typeof window !== 'undefined' ? window : this);
