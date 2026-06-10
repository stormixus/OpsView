const test = require('node:test');
const assert = require('node:assert');
const { liveWsPath } = require('../dashboard_assets/livestream.js');

test('liveWsPath: sub by default', () => {
  assert.strictEqual(liveWsPath('a1/dvr3_ch1', true, false), 'a1/dvr3_ch1');
  assert.strictEqual(liveWsPath('a1/dvr3_ch1', false, false), 'a1/dvr3_ch1');
});

test('liveWsPath: main only when hires AND hd toggled on', () => {
  assert.strictEqual(liveWsPath('a1/dvr3_ch1', true, true), 'a1/dvr3_ch1@main');
  assert.strictEqual(liveWsPath('a1/dvr3_ch1', false, true), 'a1/dvr3_ch1');
});
