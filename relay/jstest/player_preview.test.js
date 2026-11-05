const test=require('node:test'),assert=require('node:assert/strict'),vm=require('node:vm');
const source=require('node:fs').readFileSync(require('node:path').join(__dirname,'../dashboard_assets/app.js'),'utf8');
function harness(){
 const timers=new Map();let clock=1000,id=0;
 const image={hidden:true,currentSrc:'',src:'',removeAttribute(){this.src=''}};
 const ctx=vm.createContext({BASE:'/dashboard',location:{origin:'https://ops.example'},Math,performance:{now:()=>clock},encodeURIComponent,
 up:{open:true,path:'hotel/dvr1_ch1',mode:'rec',cursorT:1000,_seeking:true},$:()=>image,
 setTimeout:fn=>{timers.set(++id,fn);return id},clearTimeout:id=>timers.delete(id),upVideo:{buffered:{length:0}}});
 vm.runInContext(source.slice(source.indexOf('function upClearScrubFrame(){'),source.indexOf('// Every seek owns a generation')),ctx);
 return {ctx,image,timers,run(){const item=timers.entries().next().value;timers.delete(item[0]);item[1]();},advance(){clock+=200}};
}
test('scrub preview requests the exact video time and never exposes a stale image resource',()=>{
 const h=harness();h.ctx.upPreviewAt(1000);h.run();assert.match(h.image.src,/t=1000&frame=1$/);
 h.image.currentSrc='https://ops.example/old-image';h.image.onload();assert.equal(h.image.hidden,true);
 h.image.currentSrc=h.image.src;h.image.onload();assert.equal(h.image.hidden,false);
 h.ctx.up.cursorT=1001;h.ctx.upPreviewAt(1001);h.image.onload();assert.equal(h.image.hidden,true);
});
test('rapid scrub requests coalesce to the latest time and closing cancels scheduled work',()=>{
 const h=harness();h.ctx.upPreviewAt(1000);h.ctx.upPreviewAt(1001);h.ctx.upPreviewAt(1002);
 assert.equal(h.timers.size,1);h.run();assert.match(h.image.src,/t=1002&frame=1$/);
 h.ctx.upPreviewAt(1003);h.ctx.upClearScrubFrame();assert.equal(h.timers.size,0);assert.equal(h.image.src,'');assert.equal(h.image.onload,null);
});
test('live/channel transitions suppress an already-arrived recorded preview',()=>{
 for(const action of ['live','channel','close']){
  const h=harness();h.ctx.upPreviewAt(1000);h.run();h.image.currentSrc=h.image.src;
  if(action==='live')h.ctx.up.mode='live';if(action==='channel')h.ctx.up.path='other/camera';if(action==='close')h.ctx.up.open=false;
  h.image.onload();assert.equal(h.image.hidden,true);
 }
});
test('buffered same-file targets avoid unnecessary server extraction',()=>{
 const h=harness();h.ctx.up._curSeg={path:h.ctx.up.path,start:1000};h.ctx.upVideo.buffered={length:1,start:()=>5,end:()=>20};
 assert.equal(h.ctx.upBufferedAt(1010),true);assert.equal(h.ctx.upBufferedAt(1020),false);assert.equal(h.ctx.upBufferedAt(1000),false);
 h.ctx.up.path='other/camera';assert.equal(h.ctx.upBufferedAt(1010),false);
});
