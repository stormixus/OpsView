const test=require('node:test'),assert=require('node:assert/strict'),vm=require('node:vm');
const src=require('node:fs').readFileSync(require('node:path').join(__dirname,'../dashboard_assets/app.js'),'utf8');
const tick=()=>new Promise(r=>setImmediate(r));
test('authenticated initial state starts the routed view without a second state request',()=>{
 const order=[];let fetched=0;
 const ctx=vm.createContext({demo:{},adaptState:a=>order.push(['adapt',a]),renderSidebar:()=>order.push(['sidebar']),routeFromPath:()=>order.push(['route']),applyRealRender:()=>order.push(['render']),pollState:()=>{fetched++},setInterval:()=>1,document:{hidden:false},tick(){},refreshOpsSnap(){}});
 vm.runInContext(src.slice(src.indexOf('var loopId=null'),src.indexOf('function stopLoop(){')),ctx);
 const seed={agents:[{id:'hotel'}]};ctx.startLoop(seed);
 assert.equal(fetched,0);assert.equal(order[0][0],'adapt');assert.equal(order[2][0],'route');assert.equal(order[0][1],seed);
});
test('slow state polling coalesces concurrent refreshes and permits next refresh after completion',async()=>{
 let resolve,count=0;const ctx=vm.createContext({AbortController,fetch:()=>{count++;return new Promise(r=>resolve=r)},demo:{},adaptState(){},applyRealRender(){},selected:'hotel',location:{pathname:'/dashboard/agent/hotel'},logoutToLogin(){},renderConn(){}});
 vm.runInContext(src.slice(src.indexOf('var statePollPending=null'),src.indexOf('// Re-render after a poll')),ctx);
 const a=ctx.pollState(),b=ctx.pollState();assert.equal(a,b);assert.equal(count,1);
 resolve({ok:true,status:200,json:async()=>({agents:[]})});await a;
 ctx.pollState();assert.equal(count,2);
});
test('authentication check passes the already-read JSON to app startup',async()=>{
 let started=null;const api={relay:{version:'qa'},agents:[]};const ctx=vm.createContext({fetch:async()=>({status:200,json:async()=>api}),safeNext:()=>null,showApp:a=>started=a,showLogin:()=>{throw Error('unexpected login')},location:{}});
 const a=src.indexOf("fetch('/dashboard/api/state').then");
 vm.runInContext(src.slice(a,src.indexOf('/* ============================================================ NAVIGATION',a)),ctx);
 await tick();assert.equal(started,api);
});

test('a cancelled state response cannot restart hidden players after logout',async()=>{
 let resolve,adapted=0;const ctx=vm.createContext({AbortController,fetch:()=>new Promise(r=>resolve=r),demo:{},adaptState:()=>adapted++,applyRealRender(){},selected:'hotel',location:{pathname:'/dashboard'},logoutToLogin(){},renderConn(){}});
 vm.runInContext(src.slice(src.indexOf('var statePollPending=null'),src.indexOf('// Re-render after a poll')),ctx);
 const pending=ctx.pollState();ctx.statePollAbort.abort();resolve({ok:true,status:200,json:async()=>({agents:[]})});await pending;assert.equal(adapted,0);
});
