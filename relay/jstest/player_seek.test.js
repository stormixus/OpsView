const test=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const src=fs.readFileSync(require('node:path').join(__dirname,'../dashboard_assets/app.js'),'utf8');
const {parsePlayerDateTime}=require('../dashboard_assets/player_time.js');
const tick=()=>new Promise(r=>setImmediate(r));
function harness(){
 const nodes={},requests=[],timers=new Map();let timer=0,currentTime=0;
 const node=()=>({style:{},classList:{add(){},remove(){}},textContent:'',open:false});
 const video={...node(),readyState:0,seeking:false,src:'',pause(){},play:()=>Promise.resolve(),removeAttribute(){this.src=''},load(){this.loads=(this.loads||0)+1}};
 Object.defineProperty(video,'currentTime',{get:()=>currentTime,set:v=>{currentTime=v;video.seeking=true}});
 const ctx=vm.createContext({Promise,Date,Number,Math,AbortController,encodeURIComponent,BASE:'/dashboard',
  up:{open:true,path:'a/cam',stream:'cam',mode:'rec',segs:[],events:[],t0:700,t1:1300,cursorT:1000,_seekSeq:0},upPaused:false,upVideo:video,upEl:node(),upRail:node(),
  $:s=>nodes[s]||(nodes[s]=node()),
  setTimeout:(f)=>{timers.set(++timer,f);return timer},clearTimeout:id=>timers.delete(id),
  cancelAnimationFrame(){},upSyncRecWindow(){},upRenderRail(){},upPaintScrub(){},syncPlayerURL(){},upUpdatePauseBtn(){},
  upStartRecLoop(){ctx.loops=(ctx.loops||0)+1},
  upRailH:()=>600,upPreviewAt(){},upClearScrubFrame(){},upBufferedAt:()=>false,
  segmentAt:(segs,t)=>segs.findIndex(s=>s.start<=t&&s.start+s.dur>t),
  upLoadTimeline:(from,to,path,signal)=>new Promise(resolve=>requests.push({from,to,path,signal,resolve})),
  upResolveSegName:()=>{throw Error('unexpected serial day lookup')},
 });
 vm.runInContext(src.slice(src.indexOf('function upCancelSeek(){'),src.indexOf('function upDayOf(')),ctx);
 vm.runInContext(src.slice(src.indexOf('function upStopVideo(){'),src.indexOf('// Keep the visible window')),ctx);
 vm.runInContext(src.slice(src.indexOf('function upGap(t){'),src.indexOf('// Delta scrub')),ctx);
 vm.runInContext(src.slice(src.indexOf('function upOnWheel(e){'),src.indexOf("$('#upStage').addEventListener('wheel'")),ctx);
 return {ctx,video,nodes,requests,timers};
}
test('same-file seek keeps decoder/source and preserves target until a decoded seek completes',async()=>{
 const h=harness(),seg={name:'clip.mp4',start:1000,dur:300};h.ctx.up.segs=[seg];h.ctx.up._curSeg={...seg,path:'a/cam'};h.video.readyState=3;h.video.src='existing';
 h.ctx.upSeekTo(1080);await tick();
 assert.equal(h.video.src,'existing');assert.equal(h.video.loads,undefined);assert.equal(h.video.currentTime,80);assert.equal(h.ctx.up.cursorT,1080);assert.equal(h.ctx.loops,undefined);
 h.video.seeking=false;h.video.onseeked();assert.equal(h.video.style.visibility,'');assert.equal(h.ctx.loops,1);
});
test('timeline filename removes day-list roundtrip and new media seeks only after metadata',async()=>{
 const h=harness();h.ctx.up.segs=[{name:'clip.mp4',start:1000,dur:300}];h.ctx.upSeekTo(1075);await tick();
 assert.match(h.video.src,/name=clip.mp4#t=75$/);assert.equal(h.requests.length,0);assert.equal(h.ctx.up.cursorT,1075);assert.equal(h.ctx.loops,undefined);
 h.video.readyState=1;h.video.onloadedmetadata();assert.equal(h.video.currentTime,75);assert.equal(h.ctx.loops,undefined);
 h.video.readyState=3;h.video.seeking=false;h.video.onseeked();assert.equal(h.ctx.loops,1);
});
test('a superseded missing-band response cannot replace latest target',async()=>{
 const h=harness();h.ctx.upSeekTo(1000);h.ctx.upSeekTo(2000);
 assert.equal(h.requests[0].signal.aborted,true);
 h.requests[1].resolve({segments:[{name:'new.mp4',start:2000,dur:300}],events:[]});await tick();
 h.requests[0].resolve({segments:[{name:'old.mp4',start:1000,dur:300}],events:[]});await tick();
 assert.match(h.video.src,/new.mp4/);assert.equal(h.ctx.up.cursorT,2000);
});
test('closing or switching channel invalidates pending name and timeline work',async()=>{
 for(const action of ['close','channel','live']){
  const h=harness();h.ctx.upSeekTo(1000);h.ctx.upCancelSeek();
  if(action==='close')h.ctx.up.open=false;if(action==='channel')h.ctx.up.path='b/cam';if(action==='live')h.ctx.up.mode='live';
  h.requests[0].resolve({segments:[{name:'old.mp4',start:1000,dur:300}],events:[]});await tick();assert.equal(h.video.src,'');
 }
});
test('empty requested time stays empty and cannot silently jump to neighbouring footage',async()=>{
 const h=harness();h.ctx.up._loaded={from:0,to:3000,at:Date.now()};h.ctx.up.segs=[{name:'other.mp4',start:1500,dur:300}];h.ctx.upSeekTo(1000);await tick();
 assert.equal(h.ctx.up.mode,'gap');assert.equal(h.ctx.up.cursorT,1000);assert.equal(h.video.src,'');assert.equal(h.timers.size,0);assert.equal(h.nodes['#upState'].textContent,'이 시각 녹화 없음');
});
test('wheel updates the requested clock immediately and coalesces video loads',()=>{
 const h=harness();const e={deltaY:20,preventDefault(){}};
 h.ctx.upOnWheel(e);h.ctx.upOnWheel(e);h.ctx.upOnWheel(e);
 assert.equal(h.ctx.up.cursorT,940);assert.equal(h.requests.length,0);assert.equal(h.timers.size,1);
 [...h.timers.values()][0]();assert.equal(h.requests.length,1);
});
test('calendar parsing uses local time and rejects invalid/overflow input',()=>{
 const t=parsePlayerDateTime('2026-11-01','13:24:35');const d=new Date(t*1000);
 assert.deepEqual([d.getFullYear(),d.getMonth()+1,d.getDate(),d.getHours(),d.getMinutes(),d.getSeconds()],[2026,11,1,13,24,35]);
 for(const [day,time] of [['2026-02-30','12:00'],['2026-11-01','24:00'],['2026-11-01','12:60'],['garbage','12:00'],['2026-11-01','1:00']])assert.equal(parsePlayerDateTime(day,time),null);
});

test('an old empty band refreshes before declaring a newly arrived recording absent',async()=>{
 const h=harness();h.ctx.up._loaded={from:0,to:3000,at:Date.now()-10000};h.ctx.upSeekTo(1000);
 assert.equal(h.requests.length,1);h.requests[0].resolve({segments:[{name:'arrived.mp4',start:1000,dur:300}],events:[]});await tick();
 assert.match(h.video.src,/arrived.mp4/);assert.equal(h.ctx.up.mode,'rec');
});

test('a growing file reloads its new bytes instead of showing an earlier frame',async()=>{
 const h=harness();h.ctx.up.segs=[{name:'active.mp4',start:1000,dur:300,active:true}];h.ctx.upSeekTo(1002);await tick();
 h.video.duration=1;h.video.readyState=1;h.video.onloadedmetadata();
 assert.equal(h.video.style.visibility,'hidden');assert.equal(h.video.currentTime,0);
 // The retry is distinct from the 15-second overall loading deadline.
 const retry=[...h.timers.entries()].at(-1);h.timers.delete(retry[0]);retry[1]();assert.match(h.video.src,/refresh=1#t=2$/);
 h.video.duration=3;h.video.onloadedmetadata();h.video.readyState=3;h.video.seeking=false;h.video.onseeked();
 assert.equal(h.video.currentTime,2);assert.equal(h.video.style.visibility,'');
});
test('short finalized footage does not silently substitute its last available frame',async()=>{
 const h=harness();h.ctx.up.segs=[{name:'short.mp4',start:1000,dur:300}];h.ctx.upSeekTo(1020);await tick();
 h.video.duration=10;h.video.readyState=1;h.video.onloadedmetadata();
 assert.equal(h.ctx.up.mode,'gap');assert.equal(h.ctx.up.cursorT,1020);assert.equal(h.video.style.visibility,'hidden');
});
test('media loading has a deadline and new seek cancels a growing-file retry',async()=>{
 const h=harness();h.ctx.up.segs=[{name:'active.mp4',start:1000,dur:300,active:true}];h.ctx.upSeekTo(1002);await tick();
 h.video.duration=1;h.video.readyState=1;h.video.onloadedmetadata();const retry=[...h.timers.values()].at(-1);
 h.ctx.upCancelSeek();h.ctx.up.mode='live';const src=h.video.src;retry();assert.equal(h.video.src,src);
 const m=harness();m.ctx.upSeekTo(1000);[...m.timers.values()][0]();
 assert.equal(m.requests[0].signal.aborted,true);assert.equal(m.ctx.up._seeking,false);assert.match(m.nodes['#upState'].textContent,/遅延|지연/);
});

test('active EOF awaits a slow catalogue response before retrying or advancing',async()=>{
 const h=harness();h.ctx.up.segs=[{name:'active.mp4',start:1000,dur:300,active:true}];h.ctx.upSeekTo(1000);await tick();
 h.video.duration=300;h.video.readyState=3;h.video.onloadedmetadata();h.video.seeking=false;h.video.onseeked();
 h.video.currentTime=300;h.video.seeking=false;h.video.onended();
 assert.equal(h.requests.length,1);assert.equal(h.ctx.up._tailFetching,true);
 // No tail-reload timer runs while this request is outstanding, however slow it is.
 assert.equal(h.timers.size,1);assert.equal(h.requests[0].signal.aborted,false);
 h.requests[0].resolve({segments:[{name:'next.mp4',start:1300,dur:300}],events:[]});await tick();
 assert.match(h.video.src,/next.mp4/);assert.equal(h.ctx.up.cursorT,1300);
});
