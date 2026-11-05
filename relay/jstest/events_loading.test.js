const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const src = fs.readFileSync(require('node:path').join(__dirname, '../dashboard_assets/app.js'), 'utf8');
const tick = () => new Promise(resolve => setImmediate(resolve));
function harness() {
 const requests=[], renders=[];
 const ctx = vm.createContext({Promise,Map,Date,AbortController,encodeURIComponent,JSON,
  selected:'a',BASE:'/dashboard',recRangeToken:0,recCalOpen:false,
  recCtx:{selDay:'20260608',eventList:[],range:null},
  agentById:()=>({streams:[{path:'a/cam'}]}),
  recDateInputVal:()=>ctx.recCtx.selDay,
  recSetDateInput:d=>{ctx.recCtx.selDay=d},
  recSetActivePreset:()=>{},recDayStr:()=> '20260608',
  recRenderEventList:()=>renders.push(ctx.recCtx.eventList),
  recRenderCal:()=>{},$:()=>null,
  fetch:(url,options={})=>new Promise(resolve=>requests.push({url,options,resolve})),
 });
 vm.runInContext(src.slice(src.indexOf('function openRec(){'),src.indexOf('/* ---- date presets')),ctx);
 vm.runInContext(src.slice(src.indexOf('function loadRecEventList('),src.indexOf('var REC_KIND_NAMES')),ctx);
 vm.runInContext(src.slice(src.indexOf('function recLoadRange('),src.indexOf('(function(){ var p=',src.indexOf('function recLoadRange('))),ctx);
 ctx.REC_PRESET_LABELS={};
 const reply=(n,events)=>requests[n].resolve({ok:true,json:async()=>({events})});
 return {ctx,requests,renders,reply};
}
test('entry starts list before calendar metadata; a slow calendar cannot delay cards',async()=>{
 const h=harness();h.ctx.openRec();
 assert.match(h.requests[0].url,/rec-events-list/);assert.match(h.requests[1].url,/api\/rec\?/);
 h.reply(0,[{stream:'a/cam',start:1000}]);await tick();
 assert.equal(h.ctx.recCtx.eventList.length,1);
 assert.equal(h.renders.at(-1)[0].start,1000);
});
test('same-day reload aborts and ignores old response, including A -> B -> A',async()=>{
 const h=harness();h.ctx.recLoadDay();h.ctx.selected='b';h.ctx.recLoadDay();h.ctx.selected='a';h.ctx.recLoadDay();
 assert.equal(h.requests[0].options.signal.aborted,true);assert.equal(h.requests[1].options.signal.aborted,true);
 h.reply(2,[{start:3000}]);await tick();h.reply(0,[{start:1000}]);h.reply(1,[{start:2000}]);await tick();
 assert.equal(h.ctx.recCtx.eventList[0].start,3000);
});
test('warm revisit paints synchronously and identical revalidation preserves DOM',async()=>{
 const h=harness();h.ctx.recLoadDay();h.reply(0,[{start:1000}]);await tick();
 h.ctx.recLoadDay();assert.equal(h.ctx.recCtx.eventList[0].start,1000);
 const count=h.renders.length;h.reply(1,[{start:1000}]);await tick();assert.equal(h.renders.length,count);
 h.ctx.selected='b';h.ctx.recLoadDay();assert.equal(h.ctx.recCtx.eventList.length,0);
});
test('date range wins over stale single-day response',async()=>{
 const h=harness();h.ctx.recLoadDay();h.ctx.recLoadRange('20260601','20260608','week');
 assert.equal(h.requests[0].options.signal.aborted,true);
 h.reply(1,[{start:2000}]);await tick();h.reply(0,[{start:1000}]);await tick();
 assert.equal(h.ctx.recCtx.eventList[0].start,2000);
});
test('empty successful day preserves latest-day fallback; errors do not navigate dates',async()=>{
 for(const failed of [false,true]) {
  const h=harness();h.ctx.openRec();
  h.requests[1].resolve({ok:true,json:async()=>({days:['20260607']})});
  if(failed) h.requests[0].resolve({ok:false});else h.reply(0,[]);
  await tick();assert.equal(h.ctx.recCtx.selDay,failed?'20260608':'20260607');
 }
});
test('thumbnail priority applies only to first six cards, including appended pages',()=>{
 const ctx=vm.createContext({Date,BASE:'/dashboard',REC_KIND_NAMES:{motion:'motion'},pad2:n=>String(n).padStart(2,'0'),escAttr:String,escHtml:String});
 vm.runInContext(src.slice(src.indexOf('function evCardHTML('),src.indexOf('// Render the Protect-style')),ctx);
 const ev={start:1000,stream:'cam',ch:1,name:'camera'};
 assert.match(ctx.evCardHTML(ev,0),/loading="eager" fetchpriority="high"/);
 assert.match(ctx.evCardHTML(ev,6),/loading="lazy" fetchpriority="low"/);
 assert.match(ctx.evCardHTML(ev,24),/loading="lazy" fetchpriority="low"/);
});

test('leaving events cancels pending load and ignores a response after navigation', async()=>{
 const h=harness();h.ctx.recModalVideo=null;h.ctx.recCleanupIntersect=()=>{};h.ctx.recStopVideo=()=>{};
 vm.runInContext(src.slice(src.indexOf('function recStop(){'),src.indexOf('\n',src.indexOf('function recStop(){'))),h.ctx);
 h.ctx.recLoadDay();h.ctx.recStop();
 assert.equal(h.requests[0].options.signal.aborted,true);
 h.reply(0,[{start:1000}]);await tick();assert.equal(h.ctx.recCtx.eventList.length,0);
});
test('warm list cache is bounded and oversized days are not retained', async()=>{
 const h=harness();
 for(let n=0;n<8;n++){h.ctx.recCtx.selDay='2026060'+n;h.ctx.recLoadDay();h.reply(n,[{start:n}]);await tick();}
 assert.equal(h.ctx.recEventCache.size,6);
 h.ctx.recCtx.selDay='20260620';h.ctx.recLoadDay();h.reply(8,Array.from({length:2001},(_,start)=>({start})));await tick();
 assert.equal(h.ctx.recEventCache.has('a|20260620'),false);
});

test('dismissing a modal does not cancel event loading or infinite scroll',async()=>{
 const h=harness();let cleaned=0;
 const modal={classList:{remove:()=>{}}},cell={innerHTML:'',classList:{remove:()=>{}}};
 Object.assign(h.ctx,{modal,modalCell:cell,modalPlayer:null,opsModalTimer:null,recModalVideo:null,recCleanupIntersect:()=>{cleaned++}});
 vm.runInContext(src.slice(src.indexOf('function recStopVideo(){'),src.indexOf('\n',src.indexOf('function recStopVideo(){'))),h.ctx);
 vm.runInContext(src.slice(src.indexOf('function closeModal(){'),src.indexOf('\n',src.indexOf('function closeModal(){'))),h.ctx);
 h.ctx.recLoadDay();h.ctx.closeModal();
 assert.equal(h.requests[0].options.signal.aborted,false);assert.equal(cleaned,0);
 h.reply(0,[{start:1000}]);await tick();assert.equal(h.ctx.recCtx.eventList[0].start,1000);
});
