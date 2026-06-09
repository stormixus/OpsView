(function(){
"use strict";
var $=function(s,r){return (r||document).querySelector(s);};
var $$=function(s,r){return Array.prototype.slice.call((r||document).querySelectorAll(s));};
var LS=window.localStorage, SS=window.sessionStorage;

/* ---------------- prefs ---------------- */
var PREF=JSON.parse(LS.getItem('opsview.pref')||'{}');
function savePref(){ LS.setItem('opsview.pref', JSON.stringify(PREF)); }
function applyPref(){
  document.documentElement.dataset.theme   = PREF.theme   || 'dark';
  document.documentElement.dataset.accent  = PREF.accent  || 'green';
  document.documentElement.dataset.density = PREF.density || 'comfortable';
}
applyPref();

/* ---------------- formatters ---------------- */
function pad(n){return n<10?'0'+n:''+n;}
function fmtUptime(s){var d=Math.floor(s/86400);s%=86400;var h=Math.floor(s/3600);s%=3600;var m=Math.floor(s/60);var ss=s%60;return (d>0?d+'일 ':'')+h+':'+pad(m)+':'+pad(ss);}
function fmtAgo(sec){sec=Math.floor(sec);if(sec<0)sec=0;if(sec<60)return sec+'초';if(sec<3600)return Math.floor(sec/60)+'분';if(sec<86400)return Math.floor(sec/3600)+'시간';return Math.floor(sec/86400)+'일';}
function fmtBytes(b){var u=['B','KB','MB','GB','TB'],i=0;while(b>=1024&&i<u.length-1){b/=1024;i++;}return (i===0?b:b.toFixed(b<10?2:1))+' '+u[i];}
function fmtKbps(k){if(k>=1000)return (k/1000).toFixed(1)+'M';return Math.round(k)+'';}
function fmtClock(d){return pad(d.getHours())+':'+pad(d.getMinutes());}
function fmtTs(d){return d.getFullYear()+'-'+pad(d.getMonth()+1)+'-'+pad(d.getDate())+' '+pad(d.getHours())+':'+pad(d.getMinutes())+':'+pad(d.getSeconds());}

/* ---------------- scene vocab ---------------- */
var SCENES=['로비','주차장','복도 1F','출입구','카운터','후문','엘리베이터','복도 2F','객실 복도','보일러실','옥상','로비 2','주차 B1','계단','창고','매장 외부'];
var HUES=[210,28,150,260,200,12,180,300,220,40,190,160,330,210,90,200];

/* ---------------- agent factory ---------------- */
var AGENT_DEFS=[
  {id:'store-gangnam',  name:'강남점',     online:true,  dvr:'HIKVISION DVR1', chTotal:16, active:8,  watchers:5, sinceMin:74},
  {id:'store-hongdae',  name:'홍대점',     online:true,  dvr:'DAHUA XVR2',     chTotal:16, active:12, watchers:8, sinceMin:213},
  {id:'store-jamsil',   name:'잠실점',     online:true,  dvr:'HIKVISION DVR1', chTotal:8,  active:4,  watchers:2, sinceMin:33},
  {id:'store-seomyeon', name:'부산 서면점', online:true,  dvr:'HIKVISION DVR3', chTotal:16, active:16, watchers:3, sinceMin:540},
  {id:'store-suwon',    name:'수원역점',   online:false, dvr:'DAHUA XVR2',     chTotal:16, active:0,  watchers:0, sinceMin:0, lastSeenMin:12}
];
var WIPS=['192.168.0.','10.0.4.','172.16.2.','192.168.1.'];
function randIp(){return WIPS[Math.floor(Math.random()*WIPS.length)]+(2+Math.floor(Math.random()*250));}

function buildAgent(def, prefix){
  var now=Date.now();
  var streams=[];
  for(var i=0;i<def.chTotal;i++){
    var act=i<def.active && def.online;
    streams.push({id:'dvr_'+def.id+'_ch'+(i+1), name:''+(101+i+prefix*0), scene:SCENES[i%SCENES.length], hue:HUES[i%HUES.length],
      active:act, codec:(i%3===0?'h265':'h264'), transport:(i%4===0?['ws','hls']:['ws']), ws_watchers:act?(i%4):0});
  }
  var watchers=[];
  for(var w=0;w<def.watchers;w++){
    watchers.push({id:w+1, ip:randIp(), since:now-Math.floor(Math.random()*1000*60*25)-1000*8});
  }
  var tpD=def.online? (1500+def.active*420+Math.random()*400):0;
  var tpU=def.online? (3000+def.active*820+Math.random()*600):0;
  return {
    id:def.id, name:def.name, online:def.online, dvr:def.dvr,
    since_ms: def.online? now-def.sinceMin*60000 : 0,
    last_publish_ms: def.online? now : now-(def.lastSeenMin||30)*60000,
    pin_set:true, publish_count: def.online? 800+Math.floor(Math.random()*1500):0,
    bytes_in: def.online? (15e9+Math.random()*40e9):0, bytes_out: def.online? (40e9+Math.random()*120e9):0,
    chTotal:def.chTotal, streams:streams, watchers:watchers, nextWid:def.watchers+1,
    tput:{down:Math.round(tpD), up:Math.round(tpU)}, smooth:{down:tpD, up:tpU},
    nextWEvt: now+8000+Math.random()*12000, lastSeenMin:def.lastSeenMin||0
  };
}

var state={ relay:{version:'…', uptime_sec:0, bytes_in:0, bytes_out:0}, agents:[] };
var demo={relayDown:false, agentCount:0};
function regenAgents(){ /* no-op: agents come from the relay (see pollState) */ }

/* ---------------- real data layer: /dashboard/api/state -> render model ---------------- */
var _prevB={}; // per-agent byte counters for kbps deltas
function _tputFor(id, bin, bout, now){
  var p=_prevB[id], down=0, up=0;
  if(p && now>p.t){ var dt=now-p.t; down=Math.max(0,(bin-p.in)*8/dt); up=Math.max(0,(bout-p.out)*8/dt); }
  _prevB[id]={in:bin, out:bout, t:now};
  return {down:Math.round(down), up:Math.round(up)};
}
function _stripPort(ip){ var i=String(ip).lastIndexOf(':'); return i>0? String(ip).slice(0,i): ip; }
function adaptState(api){
  var now=Date.now();
  state.relay.version=api.relay.version; state.relay.uptime_sec=api.relay.uptime_sec;
  state.agents=(api.agents||[]).map(function(a){
    var chTotal=(a.dvrs||[]).reduce(function(n,d){return n+(d.channels||0);},0) || (a.streams||[]).length;
    var tp=_tputFor(a.id, a.bytes_in||0, a.bytes_out||0, now);
    return {
      id:a.id, name:a.name, online:!!a.connected, version:a.version||'',
      dvr:(a.dvrs&&a.dvrs[0]?a.dvrs[0].name:''),
      dvrs:(a.dvrs||[]).map(function(d){return {id:d.id, name:d.name, channels:d.channels};}),
      chans:(a.channels||[]).map(function(c){return {dvr_id:c.dvr_id, ch_num:c.ch_num, name:c.name, order:c.order, enabled:c.enabled, active:c.active};}),
      since_ms: a.since? Date.parse(a.since): now,
      last_publish_ms: a.last_publish_at? Date.parse(a.last_publish_at): now,
      pin_set:!!a.pin_set, publish_count:a.publish_count||0,
      bytes_in:a.bytes_in||0, bytes_out:a.bytes_out||0,
      chTotal:chTotal, tput:tp, smooth:{down:tp.down,up:tp.up},
      watchers:(a.watchers||[]).map(function(w){return {id:w.id, ip:_stripPort(w.ip), label:w.label||'', since: w.since?Date.parse(w.since):now};}),
      streams:(a.streams||[]).map(function(s,i){
        var dm=String(s.id).match(/dvr(\d+)/), cm=String(s.id).match(/_ch(\d+)/);
        return {
          id:s.id, name:s.name||s.id, hue:(i*47)%360,
          dvrId: dm?+dm[1]:0, ch: cm?+cm[1]:(i+1),
          active:!!s.active, codec:s.codec||'h264',
          transport:(s.codec==='h265'?['hls']:['ws','hls']),
          ws_watchers:s.ws_watchers||0, path:s.path||s.id };}),
      nextWid:0, nextWEvt: now+1e12
    };
  });
}
function pollState(){
  return fetch('/dashboard/api/state', {headers:{'Accept':'application/json'}})
    .then(function(r){ if(r.status===401) throw {auth:1}; if(!r.ok) throw {net:1}; return r.json(); })
    .then(function(api){ demo.relayDown=false; adaptState(api); applyRealRender(); })
    .catch(function(e){ if(e&&e.auth){ logoutToLogin(); } else { demo.relayDown=true; renderConn(); } });
}
// Re-render after a poll without tearing down live <video> elements.
function applyRealRender(){
  renderConn();
  renderSidebar();
  if(selected===null){ renderOverview(); }
  else {
    var a=agentById(selected);
    if(!a){ go('overview'); return; }
    renderAgentHeader(a); renderStatus(a); renderWatchers(a); renderStreams(a);
    if(curTab==='live' && !$('#grid').dataset.agent){ renderGrid(a); }
  }
}

function agentById(id){return state.agents.filter(function(a){return a.id===id;})[0];}
function onlineAgents(){return state.agents.filter(function(a){return a.online;});}
function activeStreams(a){return a.streams.filter(function(s){return s.active;});}

/* --- device (DVR/NVR) grouping: show one device's channels at a time --- */
var selDvr='all'; // 'all' or a dvr id (number)
// The selected DVR filter lives in the URL (?dvr=<id>) so a reload restores it
// instead of snapping back to 전체 (the filter had no route before).
function dvrParam(){ var v=new URLSearchParams(location.search).get('dvr'); return (v==null||v==='all') ? 'all' : (+v); }
function setDvrParam(dvr){ var u=new URL(location.href); if(dvr==='all') u.searchParams.delete('dvr'); else u.searchParams.set('dvr', dvr); history.replaceState(history.state, '', u.pathname+u.search); }
function dvrName(a, id){ var d=(a.dvrs||[]).filter(function(x){return x.id===id;})[0]; return d? d.name : ('DVR '+id); }
function streamsForView(a){
  if(selDvr==='all') return a.streams;
  return a.streams.filter(function(s){return s.dvrId===selDvr;});
}
function activeForView(a){ return streamsForView(a).filter(function(s){return s.active;}); }
function renderDvrChips(a){
  var bar=$('#dvrChips'); if(!bar) return;
  var dvrs=a.dvrs||[];
  // only worth showing when the agent has more than one device
  if(dvrs.length<=1){ bar.style.display='none'; bar.innerHTML=''; selDvr='all'; return; }
  // stale ?dvr= (device removed/renamed away) -> fall back to 전체 and clean the URL
  if(selDvr!=='all' && !dvrs.some(function(d){return d.id===selDvr;})){ selDvr='all'; setDvrParam('all'); }
  bar.style.display='';
  var html='<button class="dvr-chip'+(selDvr==='all'?' on':'')+'" data-dvr="all">전체 <span class="n">'+activeStreams(a).length+'</span></button>';
  dvrs.forEach(function(d){
    var act=a.streams.filter(function(s){return s.dvrId===d.id && s.active;}).length;
    html+='<button class="dvr-chip'+(selDvr===d.id?' on':'')+'" data-dvr="'+d.id+'">'+d.name+' <span class="n">'+act+'</span></button>';
  });
  bar.innerHTML=html;
}

/* ============================================================ LOGIN */
var fails=0, lockUntil=0, lockTimer=null;
var loginEl=$('#login'), appEl=$('#app');
var pwIn=$('#pw'), enterBtn=$('#enterBtn'), loginMsg=$('#loginMsg'), loginCard=$('#loginCard');
function showApp(){ loginEl.style.display='none'; appEl.classList.add('show'); startLoop(); }
function logoutToLogin(){ stopLoop(); appEl.classList.remove('show'); loginEl.style.display='grid'; if(pwIn){pwIn.value='';pwIn.focus();} }
function loginFail(msg, bad){
  fails++; loginCard.classList.add('err','shake'); setTimeout(function(){loginCard.classList.remove('shake');},420);
  if(fails>=4){ lock(); } else { loginMsg.textContent=msg||('비밀번호가 틀렸습니다 ('+fails+'/4)'); loginMsg.className='login-msg '+(bad||'bad'); }
  pwIn.value=''; pwIn.focus();
}
function attempt(){
  if(Date.now()<lockUntil) return;
  var pw=pwIn.value;
  fetch('/dashboard/api/login', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({password:pw})})
    .then(function(r){
      if(r.status===200){ loginMsg.textContent=''; loginMsg.className='login-msg'; fails=0; showApp(); }
      else if(r.status===429){ loginMsg.textContent='시도가 너무 많습니다 · 잠시 후'; loginMsg.className='login-msg warn'; pwIn.value=''; }
      else { loginFail(); }
    })
    .catch(function(){ loginMsg.textContent='relay 연결 실패'; loginMsg.className='login-msg warn'; });
}
function lock(){
  lockUntil=Date.now()+15000; enterBtn.disabled=true; pwIn.disabled=true; loginCard.classList.add('err');
  function t(){var left=Math.ceil((lockUntil-Date.now())/1000);
    if(left<=0){clearInterval(lockTimer);fails=0;enterBtn.disabled=false;pwIn.disabled=false;loginCard.classList.remove('err');loginMsg.textContent='';loginMsg.className='login-msg';pwIn.focus();}
    else{loginMsg.textContent='잠금됨 · 잠시 후 다시 시도 ('+left+'초)';loginMsg.className='login-msg warn';}}
  t(); lockTimer=setInterval(t,1000);
}
enterBtn.addEventListener('click', attempt);
pwIn.addEventListener('keydown', function(e){ if(e.key==='Enter') attempt(); });
pwIn.addEventListener('input', function(){ if(loginCard.classList.contains('err') && Date.now()>=lockUntil){ loginCard.classList.remove('err'); } });
$('#logoutBtn').addEventListener('click', function(){ fetch('/dashboard/api/logout', {method:'POST'}).catch(function(){}); logoutToLogin(); });
// On load, the session cookie decides: a 200 from /state means we're already in.
// #login stays hidden until we know we're NOT authed, so it never flashes on reload.
function showLogin(){ loginEl.style.display='grid'; setTimeout(function(){pwIn.focus();},120); }
fetch('/dashboard/api/state').then(function(r){
  if(r.status===200){ showApp(); } else { showLogin(); }
}).catch(showLogin);

/* ============================================================ NAVIGATION */
var selected=null;   // null = overview ; else agent id
var curTab=LS.getItem('opsview.tab')||'live'; // owners want cameras first, not the engineer status pane

function deskMiniHTML(){
  return '<div class="desk"><div class="wall"></div>'+
    '<div class="win w1"><div class="tb"><i></i><i></i><i></i></div><div class="body"><div class="ln" style="width:80%"></div><div class="ln" style="width:55%"></div><div class="ln" style="width:68%"></div></div></div>'+
    '<div class="win w2"><div class="tb"><i></i><i></i><i></i></div><div class="body"><div class="ln" style="width:90%"></div><div class="ln" style="width:60%"></div></div></div>'+
    '<div class="taskbar"></div></div>';
}

function renderSidebar(){
  var sb=$('#sidebar');
  // solo / empty layout
  if(state.agents.length<=1){ sb.style.display='none'; $('#menuBtn').style.display='none'; }
  else { sb.style.display=''; }
  var html='<button class="agent-item overview'+(selected===null?' active':'')+'" data-nav="overview">'+
      '<span class="ai-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/></svg></span>'+
      '<span class="ai-main"><span class="ai-name">전체 개요</span><span class="ai-sub">'+onlineAgents().length+' / '+state.agents.length+' 온라인</span></span></button>'+
    '<div class="side-div"></div>'+
    '<div class="side-label">에이전트 (지점)</div>';
  state.agents.forEach(function(a){
    var act = a.id+'-'+activeStreams(a).length;
    html+='<button class="agent-item'+(a.online?'':' offline')+(selected===a.id?' active':'')+'" data-nav="'+a.id+'">'+
      '<span class="ai-dot'+(a.online?' online':'')+'"></span>'+
      '<span class="ai-main"><span class="ai-name">'+a.name+'</span>'+
        '<span class="ai-sub" data-aisub="'+a.id+'">'+(a.online? ('시청자 '+a.watchers.length+' · 스트림 '+activeStreams(a).length) : '오프라인 · '+fmtAgo((Date.now()-a.last_publish_ms)/1000)+' 전')+'</span>'+
      '</span></button>';
  });
  sb.innerHTML=html;
}

function navTo(target){
  var _mb=$('#manageBtn'); if(_mb) _mb.classList.toggle('on', target==='manage');
  if(target==='manage'){
    $('#sidebar').classList.remove('open');
    stopLiveGrid(); recStop();
    $('#overview-view').classList.remove('active'); $('#agent-view').classList.remove('active');
    $('#manage-view').classList.add('active');
    loadManage();
    $('#content').scrollTop=0; syncSidebarActive();
    return;
  }
  $('#manage-view').classList.remove('active');
  selected = (target==='overview')? null : target;
  selDvr=dvrParam(); liveEditing=false; var _eb=$('#liveEditBtn'); if(_eb){ _eb.textContent='편집'; _eb.classList.remove('on'); } // restore device filter from URL (?dvr=) on reload; in-app nav has no query -> 'all'
  $('#sidebar').classList.remove('open');
  stopLiveGrid(); // tear down any playing videos before switching context
  if(selected===null){
    $('#overview-view').classList.add('active'); $('#agent-view').classList.remove('active');
    renderOverview();
  } else {
    var a=agentById(selected);
    if(!a){ selected=null; navTo('overview'); return; }
    $('#overview-view').classList.remove('active'); $('#agent-view').classList.add('active');
    renderAgentHeader(a); setTab(curTab); renderAgentAll(a);
  }
  $('#content').scrollTop=0;
  syncSidebarActive();
}
function syncSidebarActive(){
  var inMng=$('#manage-view').classList.contains('active');
  $$('.agent-item').forEach(function(b){ b.classList.toggle('active', !inMng && ((b.dataset.nav==='overview' && selected===null) || b.dataset.nav===selected)); });
}

/* --- path routing (History API): clicks pushState a real URL
   (/dashboard or /dashboard/agent/<id>) so back/forward and deep links work.
   The relay serves index.html for any /dashboard/* non-asset path. --- */
var BASE='/dashboard';
function pathFor(target){ if(target==='manage') return BASE+'/manage'; return target==='overview' ? BASE : BASE+'/agent/'+encodeURIComponent(target); }
function go(target){
  var path=pathFor(target);
  if(location.pathname.replace(/\/+$/,'')!==path.replace(/\/+$/,'')){ history.pushState({}, '', path); }
  navTo(target);
}
function routeFromPath(){
  if(/\/dashboard\/manage\/?$/.test(location.pathname)){ navTo('manage'); return; }
  var m=location.pathname.match(/\/dashboard\/agent\/(.+?)\/?$/);
  navTo(m ? decodeURIComponent(m[1]) : 'overview');
}
window.addEventListener('popstate', routeFromPath);

$('#sidebar').addEventListener('click', function(e){ var b=e.target.closest('.agent-item'); if(b) go(b.dataset.nav); });
$('#backBtn').addEventListener('click', function(){ go('overview'); });
(function(){ var rb=$('#agentReconnect'); if(!rb) return;
  rb.addEventListener('click', function(){
    if(selected===null) return;
    var btn=rb, orig=btn.textContent; btn.disabled=true; btn.textContent='요청 중…';
    fetch(BASE+'/api/agent-control',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({agent_id:selected, action:'reconnect'})})
      .then(function(r){ btn.textContent = r.ok ? '재연결 요청됨 ✓' : (r.status===409?'에이전트 오프라인':'실패'); })
      .catch(function(){ btn.textContent='실패'; })
      .finally(function(){ setTimeout(function(){ btn.disabled=false; btn.textContent=orig; }, 3000); });
  });
})();
(function(){ var bh=$('#brandHome'); if(!bh) return;
  bh.addEventListener('click', function(){ go('overview'); });
  bh.addEventListener('keydown', function(e){ if(e.key==='Enter'||e.key===' '){ e.preventDefault(); go('overview'); } });
})();
$('#menuBtn').addEventListener('click', function(){ $('#sidebar').classList.toggle('open'); });

/* tabs */
function setTab(t){
  curTab=t; LS.setItem('opsview.tab',t);
  $$('#agent-view .tab').forEach(function(b){ b.classList.toggle('active', b.dataset.tab===t); });
  $$('#agent-view .pane').forEach(function(p){ p.classList.remove('active'); });
  $('#'+t+'-pane').classList.add('active');
  if(t==='live' && selected){ renderGrid(agentById(selected)); }
  else { stopLiveGrid(); }
  if(t==='rec' && selected){ openRec(); } else { recStop(); }
}
$$('#agent-view .tab').forEach(function(b){ b.addEventListener('click', function(){ setTab(b.dataset.tab); }); });

// device (DVR) chip selection: filter the status table + live grid to one device
$('#dvrChips').addEventListener('click', function(e){
  var b=e.target.closest('.dvr-chip'); if(!b || !selected) return;
  selDvr = b.dataset.dvr==='all' ? 'all' : +b.dataset.dvr;
  setDvrParam(selDvr);
  var a=agentById(selected); if(!a) return;
  renderDvrChips(a); renderStreams(a);
  if(curTab==='live') renderGrid(a);
  else if(curTab==='rec'){ recCtx.eventFilter='all'; recRenderEventList(); } // re-scope the loaded day to this DVR (no re-fetch/day reset)
});

/* ============================================================ OVERVIEW RENDER */
function relayTotals(){
  var on=onlineAgents(); var w=0,s=0,d=0,u=0;
  on.forEach(function(a){ w+=a.watchers.length; s+=activeStreams(a).length; d+=a.tput.down; u+=a.tput.up; });
  return {agentsOnline:on.length, agentsTotal:state.agents.length, watchers:w, streams:s, down:d, up:u};
}
function renderOverview(){
  var t=relayTotals();
  $('#ovAgents').textContent=t.agentsOnline; $('#ovAgentsTotal').textContent=t.agentsTotal;
  $('#ovWatchers').textContent=t.watchers; $('#ovStreams').textContent=t.streams;
  $('#ovDown').textContent=fmtKbps(t.down); $('#ovUp').textContent=fmtKbps(t.up);
  $('#ovUptime').textContent=fmtUptime(state.relay.uptime_sec);
  if($('#ovVersion')) $('#ovVersion').textContent=state.relay.version||'—';
  $('#ovSub').textContent='relay '+state.relay.version+' · '+t.agentsTotal+'개 지점 연결';
  // agent grid
  var grid=$('#agentGrid');
  $('#noAgents').style.display = state.agents.length===0? 'flex':'none';
  grid.style.display = state.agents.length===0? 'none':'';
  $('#ovGridMeta').textContent = state.agents.length? (state.agents.length+'개 지점'):'';
  var html='';
  state.agents.forEach(function(a){
    var act=activeStreams(a).length;
    var agoTxt = a.online? (fmtAgo((Date.now()-a.last_publish_ms)/1000)+' 전 프레임') : ('마지막 접속 '+fmtAgo((Date.now()-a.last_publish_ms)/1000)+' 전');
    html+='<button class="agent-card'+(a.online?'':' offline')+'" data-nav="'+a.id+'">'+
      '<div class="ac-snap">'+deskMiniHTML()+
        '<span class="ac-badge '+(a.online?'on':'off')+'">'+(a.online?'<span class="dot live"></span>온라인':'오프라인')+'</span>'+
        '<span class="ac-hide" data-hide="'+a.id+'" title="대시보드에서 숨기기"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M9.9 4.24A9.1 9.1 0 0 1 12 4c7 0 10 8 10 8a18.5 18.5 0 0 1-2.16 3.19M6.6 6.6A18.5 18.5 0 0 0 2 12s3 8 10 8a9.1 9.1 0 0 0 5.4-1.6M1 1l22 22"/></svg></span>'+
        (a.online?'<span class="ac-chcount">'+act+'/'+a.chTotal+' CH</span>':'')+
        '<div class="ac-off"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M2 5l20 14M2 5h20v14"/></svg><span>연결 끊김</span></div>'+
      '</div>'+
      '<div class="ac-body"><div class="ac-name">'+a.name+'</div>'+
        '<div class="ac-stats"><div class="s"><span class="v" data-acw="'+a.id+'">'+(a.online?a.watchers.length:'–')+'</span><span class="k">시청자</span></div>'+
          '<div class="s"><span class="v" data-acs="'+a.id+'">'+(a.online?act:'–')+'</span><span class="k">활성 스트림</span></div>'+
          '<div class="s"><span class="v" data-act="'+a.id+'">'+(a.online?fmtKbps(a.tput.up):'–')+'</span><span class="k">송신 kbps</span></div></div>'+
        '<div class="ac-foot"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="13" height="13" style="width:13px;height:13px;"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></svg><span class="mono" data-acago="'+a.id+'">'+agoTxt+'</span></div>'+
      '</div></button>';
  });
  grid.innerHTML=html;
}
$('#agentGrid').addEventListener('click', function(e){
  var hb=e.target.closest('.ac-hide');
  if(hb){ e.stopPropagation(); e.preventDefault(); hideAgent(hb.dataset.hide, true); return; }
  var c=e.target.closest('.agent-card'); if(c) go(c.dataset.nav);
});
// Hide/unhide an agent (e.g. the unused default) from the dashboard.
function hideAgent(id, hidden){
  fetch('/dashboard/api/agent-hide',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({id:id,hidden:hidden})})
    .then(function(r){ if(r.ok){ pollState().then(function(){ if(selected!==null && !agentById(selected) && hidden) go('overview'); }); loadHiddenAgents(); if($('#manage-view').classList.contains('active')) loadBranches(); } });
}

function updateOverviewLive(){
  if(selected!==null) return;
  var t=relayTotals();
  $('#ovAgents').textContent=t.agentsOnline; $('#ovWatchers').textContent=t.watchers;
  $('#ovStreams').textContent=t.streams; $('#ovDown').textContent=fmtKbps(t.down); $('#ovUp').textContent=fmtKbps(t.up);
  $('#ovUptime').textContent=fmtUptime(state.relay.uptime_sec);
  state.agents.forEach(function(a){
    if(!a.online) return;
    var w=$('[data-acw="'+a.id+'"]'); if(w) w.textContent=a.watchers.length;
    var s=$('[data-acs="'+a.id+'"]'); if(s) s.textContent=activeStreams(a).length;
    var u=$('[data-act="'+a.id+'"]'); if(u) u.textContent=fmtKbps(a.tput.up);
    var g=$('[data-acago="'+a.id+'"]'); if(g) g.textContent=fmtAgo((Date.now()-a.last_publish_ms)/1000)+' 전 프레임';
  });
}

/* ============================================================ AGENT RENDER */
function renderAgentHeader(a){
  $('#agName').textContent=a.name;
  var b=$('#agBadge');
  b.className='badge-state '+(a.online?'on':'off');
  b.innerHTML=(a.online?'<span class="dot live"></span>온라인':'오프라인');
  appEl.classList.toggle('agent-offline', !a.online);
  $('#agSince').textContent = a.online? ('연결 시작 '+fmtAgo((Date.now()-a.since_ms)/1000)+' 전 · '+a.dvr)
                                       : ('마지막 접속 '+fmtAgo((Date.now()-a.last_publish_ms)/1000)+' 전 · '+a.dvr);
  renderDvrChips(a);
}
function renderAgentAll(a){ renderStatus(a); renderWatchers(a); renderStreams(a); /* grid handled by setTab */ }

function renderStatus(a){
  var connected=a.online;
  $('#pubPanel').classList.toggle('pub-disc', !connected);
  $('#pubDot').className='dot '+(connected?'live':'bad');
  $('#pubState').textContent=connected?'연결됨':'끊김';
  $('#pubState').style.color=connected?'':'var(--bad)';
  var ago=Math.floor((Date.now()-a.last_publish_ms)/1000);
  $('#pubAgo').textContent=fmtAgo(ago);
  $('#pubOffAgo').textContent='마지막 프레임 '+fmtAgo(ago)+' 전';
  $('#pubCount').textContent=a.publish_count.toLocaleString();
  $('#pubCount2').textContent=a.publish_count.toLocaleString();
  $('#pubBytes').textContent=fmtBytes(a.bytes_out);
  $('#pubUptime').textContent= connected? fmtUptime(Math.floor((Date.now()-a.since_ms)/1000)) : '—';
  $('#pubDvr').textContent=a.dvr;
  if($('#pubVer')) $('#pubVer').textContent = (connected && a.version) ? ('v'+a.version) : '';
  $('#pinBadge').style.display=a.pin_set?'':'none';
  $('#tpDown').textContent=fmtKbps(a.tput.down);
  $('#tpUp').textContent=fmtKbps(a.tput.up);
  $('#watchNum').textContent=a.watchers.length;
  $('#streamNum').textContent=activeStreams(a).length;
  $('#streamTotal').textContent=a.chTotal;
}
function renderWatchers(a){
  var body=$('#watchBody'), panel=$('#watchPanel');
  var list=a.watchers;
  panel.classList.toggle('is-empty', list.length===0);
  $('#watchCount').textContent=list.length+' 접속';
  var existing={}; $$('tr',body).forEach(function(tr){existing[tr.dataset.id]=1;});
  var html='';
  list.forEach(function(w){
    var ago=Math.floor((Date.now()-w.since)/1000);
    var who = w.label ? '<b>'+escHtml(w.label)+'</b> <span class="mono" style="opacity:.5">'+escHtml(w.ip)+'</span>' : '<span class="mono">'+escHtml(w.ip)+'</span>';
    html+='<tr data-id="'+w.id+'" class="'+(existing[w.id]?'':'row-in')+'">'+
      '<td><span class="id">#'+w.id+'</span></td><td>'+who+'</td>'+
      '<td class="num agocell" data-since="'+w.since+'">'+fmtAgo(ago)+' 전</td></tr>';
  });
  body.innerHTML=html;
}
function renderStreams(a){
  var body=$('#streamBody'), panel=$('#streamPanel');
  var pool = a.online? streamsForView(a) : [];
  var list = pool.filter(function(s){return s.active;}).concat(pool.filter(function(s){return !s.active;}).slice(0,2));
  panel.classList.toggle('is-empty', list.length===0);
  $('#streamCount').textContent=activeForView(a).length+' / '+(selDvr==='all'? a.chTotal : pool.length)+' 활성';
  var html='';
  list.forEach(function(s){
    var trans=s.transport.map(function(t){return '<span class="tag '+t+'">'+t.toUpperCase()+'</span>';}).join(' ');
    html+='<tr><td><b class="mono">'+s.name+'</b> <span class="id">CH'+s.ch+'</span></td>'+
      '<td><span class="tag '+s.codec+'">'+s.codec+'</span></td>'+
      '<td><span class="tspan">'+trans+'</span></td>'+
      '<td class="num" data-wsid="'+s.id+'">'+(s.active?s.ws_watchers:'–')+'</td>'+
      '<td>'+(s.active?'<span class="live-cell on"><span class="dot live"></span>live</span>':'<span class="live-cell off"><span class="dot"></span>대기</span>')+'</td></tr>';
  });
  body.innerHTML=html;
}

/* live grid */
function niceCols(n){return n<=1?1:n<=4?2:n<=9?3:4;}
function cellHTML(s){
  var off=!s.active;
  return '<div class="surface" style="--h:'+s.hue+'"></div>'+
    '<div class="scene" style="--h:'+s.hue+'"><div class="glow g1"></div><div class="glow g2"></div><div class="floor"></div></div>'+
    '<div class="scan"></div><div class="ts mono cellts"></div>'+
    '<div class="rec'+(off?'':' on')+'"><i></i>REC</div>'+
    '<span class="dot cstat '+(off?'bad':'live')+'"></span>'+
    '<div class="clabel">'+(liveEditing?'<input class="cell-name" data-ch="'+s.ch+'" data-dvr="'+s.dvrId+'" value="'+escAttr(s.name)+'">':'<span class="ch">'+escHtml(s.name)+'</span>')+'<span class="nm">CH'+s.ch+'</span>'+
      (off?'':'<span class="wn"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="8" r="3"/><path d="M6 20a6 6 0 0 1 12 0"/></svg>'+s.ws_watchers+'</span>')+'</div>'+
    '<div class="noimg"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M2 5l20 14M2 5h20v14"/></svg><span>신호 없음</span></div>'+
    '<div class="expand"><div class="ic"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M9 3H3v6M21 9V3h-6M3 15v6h6M15 21h6v-6"/></svg></div></div>';
}
/* --- real video players for the live grid --- */
var livePlayers=[];
function stopLiveGrid(){
  livePlayers.forEach(function(p){ try{ p&&p.close&&p.close(); }catch(e){} });
  livePlayers=[];
  var g=$('#grid'); if(g){ g.innerHTML=''; delete g.dataset.agent; }
}
function _isIOS(){ return /iPad|iPhone|iPod/.test(navigator.userAgent) || (navigator.platform==='MacIntel' && navigator.maxTouchPoints>1); }
function _wsUsable(){ return ('MediaSource' in window) && !_isIOS(); }
function _hx(n){ return (n<16?'0':'')+n.toString(16); }
function codecFromInit(d){ for(var i=0;i+8<d.length;i++){ if(d[i]===0x61&&d[i+1]===0x76&&d[i+2]===0x63&&d[i+3]===0x43) return 'avc1.'+_hx(d[i+5])+_hx(d[i+6])+_hx(d[i+7]); } return null; }
function playWS(video, wsUrl, onFail){
  if(!_wsUsable()){ onFail&&onFail(); return null; }
  var ms=new MediaSource(), sb=null, ws=null, gotInit=false, failed=false, q=[];
  function cleanup(){ try{ws&&ws.close();}catch(e){} try{if(ms.readyState==='open')ms.endOfStream();}catch(e){} }
  function fail(){ if(failed)return; failed=true; clearTimeout(tm); cleanup(); onFail&&onFail(); }
  function flush(){ if(!sb||sb.updating||!q.length)return; try{sb.appendBuffer(q.shift());}catch(err){ if(err&&err.name==='QuotaExceededError'){ try{ if(sb.buffered.length){var e=sb.buffered.end(sb.buffered.length-1); if(e>8)sb.remove(0,e-4);} }catch(e2){} } else fail(); } }
  video.src=URL.createObjectURL(ms);
  var tm=setTimeout(function(){ if(!gotInit) fail(); },6000);
  ms.addEventListener('sourceopen', function(){
    try{ ws=new WebSocket(wsUrl); }catch(e){ fail(); return; }
    ws.binaryType='arraybuffer';
    ws.onmessage=function(ev){ if(failed)return; var data=new Uint8Array(ev.data);
      if(!gotInit){ gotInit=true; clearTimeout(tm); var codec=codecFromInit(data); var mime=codec?'video/mp4; codecs="'+codec+'"':'';
        if(!codec||!MediaSource.isTypeSupported(mime)){ fail(); return; }
        try{ sb=ms.addSourceBuffer(mime); }catch(e){ fail(); return; }
        sb.mode='sequence';
        sb.addEventListener('updateend', function(){ flush(); video.play().catch(function(){}); });
        sb.addEventListener('error', fail);
      }
      q.push(data); flush();
    };
    ws.onerror=fail; ws.onclose=function(){ if(!gotInit) fail(); };
  });
  video.play().catch(function(){});
  return {close:cleanup};
}
function playHLS(video, hlsUrl){ if(video.canPlayType('application/vnd.apple.mpegurl')){ video.src=hlsUrl; video.play().catch(function(){}); } }
function liveCellHTML(s){
  var off=!s.active;
  return '<video class="cellvid" muted autoplay playsinline></video>'+
    '<div class="scan"></div><div class="ts mono cellts"></div>'+
    '<div class="rec'+(off?'':' on')+'"><i></i>REC</div>'+
    '<span class="dot cstat '+(off?'bad':'live')+'"></span>'+
    '<div class="clabel">'+(liveEditing?'<input class="cell-name" data-ch="'+s.ch+'" data-dvr="'+s.dvrId+'" value="'+escAttr(s.name)+'">':'<span class="ch">'+escHtml(s.name)+'</span>')+'<span class="nm">CH'+s.ch+'</span>'+
      (off?'':'<span class="wn"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="8" r="3"/><path d="M6 20a6 6 0 0 1 12 0"/></svg>'+s.ws_watchers+'</span>')+'</div>'+
    '<div class="expand"><div class="ic"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M9 3H3v6M21 9V3h-6M3 15v6h6M15 21h6v-6"/></svg></div></div>';
}
function renderGrid(a){
  stopLiveGrid();
  var grid=$('#grid'), pane=$('#live-pane');
  var list = a.online? activeForView(a) : [];
  pane.classList.toggle('empty', list.length===0);
  grid.style.setProperty('--cols', niceCols(Math.max(list.length,1)));
  $('#liveMeta').textContent = list.length? ((selDvr==='all'?'':dvrName(a,selDvr)+' · ')+list.length+' 채널') : '';
  var html=''; list.forEach(function(s){ html+='<button class="cell" data-id="'+s.id+'" data-ch="'+s.ch+'" data-dvr="'+s.dvrId+'"'+(liveEditing?' draggable="true"':'')+'>'+liveCellHTML(s)+'</button>'; });
  grid.innerHTML=html;
  grid.classList.toggle('editing', liveEditing);
  grid.dataset.agent=a.id;
  var cells=$$('#grid .cell');
  list.forEach(function(s, i){
    var video=cells[i] && cells[i].querySelector('video'); if(!video) return;
    var wsUrl=(location.protocol==='https:'?'wss':'ws')+'://'+location.host+'/surv/ws/'+s.path;
    var hlsUrl=location.origin+'/surv/'+s.path+'/index.m3u8';
    var p=playWS(video, wsUrl, function(){ playHLS(video, hlsUrl); });
    if(p) livePlayers.push(p);
  });
  if(liveEditing) wireLiveEdit(a);
  updateCellClocks();
}
$('#grid').addEventListener('click', function(e){ if(liveEditing) return; var c=e.target.closest('.cell'); if(c) openModal(c.dataset.id); });
function updateCellClocks(){ var ts=fmtTs(new Date()); $$('.cellts').forEach(function(e){ e.textContent=ts; }); }

/* modal */
var modal=$('#modal'), modalCell=$('#modalCell');
var modalPlayer=null;
function openModal(id){
  if(!selected) return; var a=agentById(selected); if(!a) return;
  var s=a.streams.filter(function(x){return x.id===id;})[0]; if(!s) return;
  // Render the real video cell (not the demo placeholder) and attach the live
  // WS/HLS player — same path as the grid cells, so the enlarged view streams.
  modalCell.innerHTML=liveCellHTML(s); var ex=modalCell.querySelector('.expand'); if(ex) ex.style.display='none';
  modal.classList.add('show'); updateCellClocks();
  if(s.active){
    var video=modalCell.querySelector('video');
    if(video){
      var wsUrl=(location.protocol==='https:'?'wss':'ws')+'://'+location.host+'/surv/ws/'+s.path;
      var hlsUrl=location.origin+'/surv/'+s.path+'/index.m3u8';
      modalPlayer=playWS(video, wsUrl, function(){ playHLS(video, hlsUrl); });
    }
  }
}
function closeModal(){ modal.classList.remove('show'); if(modalPlayer){try{modalPlayer.close&&modalPlayer.close();}catch(e){} modalPlayer=null;} if(typeof recStop==='function') recStop(); modalCell.innerHTML=''; modalCell.classList.remove('bare'); if(opsModalTimer){clearInterval(opsModalTimer);opsModalTimer=null;} }
$('#modalClose').addEventListener('click', closeModal);
modal.addEventListener('click', function(e){ if(e.target===modal) closeModal(); });
document.addEventListener('keydown', function(e){ if(e.key==='Escape'){ closeModal(); closeDrawer(); } });

/* ============================================================ OPS SNAPSHOT */
function opsSnapURL(a){ return '/dashboard/api/ops-snapshot?agent='+encodeURIComponent(a.id)+'&t='+Date.now(); }
// Refresh the inline Ops snapshot for the selected agent. The relay renders the
// latest screen frame to PNG on demand; a 204 (offline / no frame) trips onerror
// and we fall back to the .desk placeholder.
function refreshOpsSnap(){
  var img=$('#opsSnap'), desk=$('#pubSnap .desk');
  var a = selected!==null ? agentById(selected) : null;
  if(!img) return;
  if(!a || !a.online || demo.relayDown){ img.style.display='none'; img.removeAttribute('src'); if(desk) desk.style.display=''; return; }
  img.onload=function(){ img.style.display=''; if(desk) desk.style.display='none'; };
  img.onerror=function(){ img.style.display='none'; if(desk) desk.style.display=''; };
  img.src=opsSnapURL(a);
}
// Click the Ops panel (agent detail) to enlarge — bigger snapshot, faster refresh.
var opsModalTimer=null;
function openOpsModal(){
  var a = selected!==null ? agentById(selected) : null;
  if(!a || !a.online) return;
  modalCell.classList.add('bare');
  modalCell.innerHTML='<img id="opsModalImg" class="ops-modal-img" alt="Ops 화면">';
  modal.classList.add('show');
  var img=$('#opsModalImg');
  function r(){ if(!modal.classList.contains('show')||!img) return; img.src=opsSnapURL(a); }
  r(); opsModalTimer=setInterval(r, 600);
}
$('#pubSnap').addEventListener('click', openOpsModal);

/* ============================================================ WATCHERS DETAIL */
// Click the Watchers stat to pop the full watcher list. The operator names each
// IP here (이름 지정) — the name is keyed by IP and persists for that address.
function openWatchersModal(){
  var a = selected!==null ? agentById(selected) : null; if(!a) return;
  var list=a.watchers||[];
  var rows = list.length ? list.map(function(w){
    var ago=Math.floor((Date.now()-w.since)/1000);
    return '<tr><td><span class="id">#'+w.id+'</span></td>'+
      '<td><input class="ipname-in" data-ip="'+escAttr(w.ip)+'" value="'+escAttr(w.label||'')+'" placeholder="이름 지정" autocomplete="off"></td>'+
      '<td class="mono">'+escHtml(w.ip)+'</td>'+
      '<td class="num">'+fmtAgo(ago)+' 전</td></tr>';
  }).join('') : '<tr><td colspan="4" style="text-align:center;opacity:.55;padding:22px;">접속 중인 시청자 없음</td></tr>';
  modalCell.classList.add('bare');
  modalCell.innerHTML='<div class="watchers-detail"><h3>Watchers · '+list.length+'명</h3>'+
    '<p class="wd-hint">IP별로 이름을 지정하면 다음에도 그 IP는 이 이름으로 표시됩니다.</p>'+
    '<table class="tbl"><thead><tr><th>ID</th><th>이름</th><th>IP</th><th>접속 시간</th></tr></thead><tbody>'+rows+'</tbody></table></div>';
  modal.classList.add('show');
}
$('#statWatch').addEventListener('click', openWatchersModal);
// Save an IP -> name mapping when its input loses focus or Enter is pressed.
function saveIPName(inp){
  var ip=inp.dataset.ip, label=inp.value.trim();
  if(inp._saved===label) return; inp._saved=label;
  inp.classList.add('saving');
  fetch(BASE+'/api/ip-label',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({ip:ip,label:label})})
    .then(function(r){ inp.classList.remove('saving'); inp.classList.add(r.ok?'saved':'savefail'); setTimeout(function(){inp.classList.remove('saved','savefail');},1200); if(r.ok) pollState(); })
    .catch(function(){ inp.classList.remove('saving'); inp.classList.add('savefail'); });
}
modalCell.addEventListener('change', function(e){ if(e.target.classList.contains('ipname-in')) saveIPName(e.target); });
modalCell.addEventListener('keydown', function(e){ if(e.target.classList.contains('ipname-in') && e.key==='Enter'){ e.preventDefault(); e.target.blur(); } });

/* ============================================================ EVENTS (이벤트) — Protect-style thumbnail grid */
// recCtx keeps the day + the agent-wide event list (newest-first from the API).
// segCache memoizes per-stream segment lists for the day (used for modal playback).
var REC_EV_PAGE = 80; // number of event cards to load per page
var recCtx = { day:null, selDay:'', availDays:[], eventList:[], eventFilter:'all', segCache:{}, evShown:0 };
function pad2(n){ return (n<10?'0':'')+n; }
function recDayStr(d){ return ''+d.getFullYear()+pad2(d.getMonth()+1)+pad2(d.getDate()); }
// Date model for the custom calendar: a single YYYYMMDD string (recSelDay)
// replaces the old native <input type=date>. recSetDateInput / recDateInputVal
// keep their names so the rest of the rec code is unchanged.
var REC_WD=['일','월','화','수','목','금','토'];
function recSetDateInput(ymd){
  recCtx.selDay=ymd||'';
  var lbl=$('#recDateLabel'); if(lbl){
    if(ymd){ var d=new Date(+ymd.slice(0,4),+ymd.slice(4,6)-1,+ymd.slice(6,8));
      lbl.textContent=ymd.slice(0,4)+'.'+ymd.slice(4,6)+'.'+ymd.slice(6,8)+' ('+REC_WD[d.getDay()]+')'; }
    else lbl.textContent='날짜';
  }
  if(recCalOpen) recRenderCal();
}
function recDateInputVal(){ return recCtx.selDay||''; }
function recSegAtIn(segs, sec){ for(var i=0;i<segs.length;i++){ var s=segs[i]; if(sec>=s.start && sec<s.start+s.dur+2) return s; } return null; }

function openRec(){
  var a = selected!==null ? agentById(selected) : null; if(!a) return;
  if(!recDateInputVal()) recSetDateInput(recDayStr(new Date()));
  recLoadDays();
}
// Pick a day that has events/recordings: probe the first stream's available days;
// if today has nothing recorded, fall back to the newest recorded day.
function recLoadDays(){
  var a = selected!==null ? agentById(selected) : null;
  var probe = a && a.streams && a.streams[0] ? a.streams[0].path : null;
  if(!probe){ recLoadDay(); return; }
  fetch(BASE+'/api/rec?stream='+encodeURIComponent(probe)).then(function(r){return r.ok?r.json():null;}).then(function(d){
    var days=(d&&d.days)||[], want=recDateInputVal();
    recCtx.availDays=days; // recorded days (probe stream) -> dotted in the calendar
    if((!want || days.indexOf(want)<0) && days.length) want=days[0];
    if(want) recSetDateInput(want);
    if(recCalOpen) recRenderCal();
    recLoadDay();
  }).catch(function(){ recLoadDay(); });
}
function recLoadDay(){
  var day=recDateInputVal(); if(!day) return;
  recCtx.day=day; recCtx.segCache={};
  loadRecEventList(day).then(function(list){
    if(day!==recCtx.day) return; // a newer day was selected mid-flight
    recCtx.eventList=list||[];
    recRenderEventList();
  });
}
function loadRecEventList(day){
  if(!selected) return Promise.resolve([]);
  return fetch(BASE+'/api/rec-events-list?agent='+encodeURIComponent(selected)+'&day='+day, {credentials:'same-origin'})
    .then(function(r){ return r.ok? r.json() : null; })
    .then(function(j){ return (j&&Array.isArray(j.events)) ? j.events : []; })
    .catch(function(){ return []; });
}
var REC_KIND_NAMES={person:'사람',vehicle:'차량',motion:'모션',linecross:'라인',intrusion:'침입'};
// Inline SVG glyphs for the event-kind filter chips (전체/사람/차량/…).
var REC_KIND_ICONS={
  all:'<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/></svg>',
  person:'<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="7" r="3.4"/><path d="M5.5 21a6.5 6.5 0 0 1 13 0"/></svg>',
  vehicle:'<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 13l1.6-4.2A2 2 0 0 1 6.5 7.5h11a2 2 0 0 1 1.9 1.3L21 13v5h-2.5M3 18v-5m0 5h2.5M21 18v-5"/><circle cx="7" cy="18" r="1.6"/><circle cx="17" cy="18" r="1.6"/></svg>',
  motion:'<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 8a13 13 0 0 1 0 8M8 6a18 18 0 0 1 0 12"/><circle cx="15" cy="12" r="2.4"/><path d="M19 9a5 5 0 0 1 0 6"/></svg>',
  linecross:'<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 17h18"/><path d="M14 6l4 4-4 4"/><path d="M18 10H7"/></svg>',
  intrusion:'<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"/><path d="M12 9v4M12 17h.01"/></svg>'
};
function recFilterChip(kind, label){
  return '<button class="ev-filter-chip'+(recCtx.eventFilter===kind?' on':'')+'" data-kind="'+kind+'">'+
    '<span class="ev-fi-ic">'+(REC_KIND_ICONS[kind]||'')+'</span>'+escHtml(label)+'</button>';
}
var recIntersectObs = null; // IntersectionObserver for infinite scroll sentinel
// Helper: generate HTML for a single event card
function evCardHTML(ev){
  var d=new Date(ev.start*1000), kind=ev.kind||'motion', kindLabel=REC_KIND_NAMES[kind]||kind;
  var when=(d.getMonth()+1)+'월 '+pad2(d.getDate())+', '+pad2(d.getHours())+':'+pad2(d.getMinutes());
  var thumbUrl=BASE+'/api/rec-thumb?stream='+encodeURIComponent(ev.stream)+'&t='+ev.start;
  return '<button class="ev-cell ev-k-'+escAttr(kind)+'" data-stream="'+escAttr(ev.stream)+'" data-start="'+ev.start+'" data-ch="'+escAttr(''+ev.ch)+'">'+
    '<img class="ev-cellimg" loading="lazy" src="'+escAttr(thumbUrl)+'" alt="'+escAttr(kindLabel)+'">'+
    '<div class="ev-cellbar"><span class="ev-celltxt"><span class="ev-celltime">'+escHtml(when)+'</span>'+
    '<span class="ev-cellcam">'+escHtml(ev.name)+' · CH'+escHtml(''+ev.ch)+'</span></span>'+
    '<span class="ev-kicon ev-'+escAttr(kind)+'"></span></div></button>';
}
// Render the Protect-style event thumbnail grid (newest-first, filtered by kind).
// Parse the DVR id out of an event's stream path (".../dvr<N>_ch<M>").
function evDvrId(ev){ var m=String(ev&&ev.stream).match(/dvr(\d+)/); return m?+m[1]:0; }
function recRenderEventList(){
  var grid=$('#recEventGrid'), filtersEl=$('#recEventFilters'); if(!grid || !filtersEl) return;
  // Scope to the selected DVR chip (shared with the live grid); 'all' = every device.
  var events=recCtx.eventList||[];
  if(selDvr!=='all') events=events.filter(function(ev){ return evDvrId(ev)===selDvr; });
  // filter chips: 전체 + every kind present in the day's events
  var kinds={}; events.forEach(function(ev){ kinds[ev.kind||'motion']=true; });
  var filters=[recFilterChip('all','전체')];
  ['person','vehicle','motion','linecross','intrusion'].forEach(function(k){
    if(kinds[k]) filters.push(recFilterChip(k, REC_KIND_NAMES[k]||k));
  });
  filtersEl.innerHTML=filters.join('');
  $('#recMeta').textContent = events.length ? (events.length+'건') : '';
  if(!events.length){ grid.innerHTML='<div class="ev-grid-empty"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2" stroke-linecap="round"/></svg><b>이벤트 없음</b></div>'; recCleanupIntersect(); return; }
  var filtered = recCtx.eventFilter==='all' ? events : events.filter(function(ev){ return (ev.kind||'motion')===recCtx.eventFilter; });
  if(!filtered.length){ grid.innerHTML='<div class="ev-grid-empty"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2" stroke-linecap="round"/></svg><b>선택한 종류 이벤트 없음</b></div>'; recCleanupIntersect(); return; }
  // Reset for fresh render (day/filter change)
  recCtx.evShown = 0;
  grid.innerHTML = '';
  recAppendEvents(grid, filtered);
}
// Append the next page of event cards to the grid
function recAppendEvents(grid, filtered){
  if(!grid || !filtered) return;
  var start = recCtx.evShown;
  var end = Math.min(start + REC_EV_PAGE, filtered.length);
  var slice = filtered.slice(start, end);
  if(!slice.length) return;
  var html = slice.map(evCardHTML).join('');
  grid.insertAdjacentHTML('beforeend', html);
  recCtx.evShown = end;
  // Attach onerror handler to newly appended thumbnails
  var newImgs = grid.querySelectorAll('.ev-cellimg');
  for(var i = newImgs.length - slice.length; i < newImgs.length; i++){
    var img = newImgs[i];
    if(img && !img.onerror){
      img.addEventListener('error', function(){ this.onerror=null; this.classList.add('ev-thumb-na'); this.removeAttribute('src'); });
    }
  }
  // Setup or update the sentinel for infinite scroll
  if(recCtx.evShown < filtered.length){
    recSetupIntersect(grid, filtered);
  } else {
    recCleanupIntersect();
  }
}
// Setup IntersectionObserver on a sentinel div at the end of the grid
function recSetupIntersect(grid, filtered){
  recCleanupIntersect();
  var sentinel = document.createElement('div');
  sentinel.id = 'recSentinel';
  sentinel.style.cssText = 'height:1px;pointer-events:none;';
  grid.appendChild(sentinel);
  recIntersectObs = new IntersectionObserver(function(entries){
    if(entries[0] && entries[0].isIntersecting){
      recAppendEvents(grid, filtered);
    }
  }, { rootMargin: '600px' });
  recIntersectObs.observe(sentinel);
}
// Cleanup the IntersectionObserver and sentinel
function recCleanupIntersect(){
  if(recIntersectObs){ recIntersectObs.disconnect(); recIntersectObs = null; }
  var sentinel = document.getElementById('recSentinel');
  if(sentinel && sentinel.parentNode){ sentinel.parentNode.removeChild(sentinel); }
}
$('#recEventFilters').addEventListener('click', function(e){
  var b=e.target.closest('[data-kind]'); if(!b) return;
  recCtx.eventFilter=b.dataset.kind;
  recCtx.evShown = 0; // reset on filter change
  recRenderEventList();
});
// Click a card -> open the event in the modal video player at its timestamp.
// The grid spans all cameras, so fetch the card's stream segments for the day
// (memoized), find the segment containing the event time, and seek into it.
$('#recEventGrid').addEventListener('click', function(e){
  var card=e.target.closest('.ev-cell'); if(!card) return;
  openEventModal(card.dataset.stream, +card.dataset.start, card.dataset.ch, card.querySelector('.ev-celltime'));
});
function recSegsForDay(stream, day){
  var key=stream+'|'+day;
  if(recCtx.segCache[key]) return Promise.resolve(recCtx.segCache[key]);
  return fetch(BASE+'/api/rec?stream='+encodeURIComponent(stream)+'&day='+day).then(function(r){return r.ok?r.json():null;})
    .then(function(d){ var segs=(d&&d.segments)||[]; recCtx.segCache[key]=segs; return segs; })
    .catch(function(){ return []; });
}
function openEventModal(stream, t, ch, timeEl){
  if(!stream) return;
  var day=recCtx.day;
  modalCell.classList.add('bare');
  modalCell.innerHTML='<div class="ev-modal-wrap"><div class="ev-modal-na">불러오는 중…</div></div>';
  modal.classList.add('show');
  recSegsForDay(stream, day).then(function(segs){
    if(!modal.classList.contains('show')) return; // closed while loading
    var s=recSegAtIn(segs, t);
    var wrap=modalCell.querySelector('.ev-modal-wrap'); if(!wrap) return;
    if(!s){ wrap.innerHTML='<div class="ev-modal-na">해당 시각 녹화 구간 없음</div>'; return; }
    var offset=Math.max(0, t-s.start);
    var url=BASE+'/api/rec-file?stream='+encodeURIComponent(stream)+'&name='+encodeURIComponent(s.name);
    wrap.innerHTML='<video controls autoplay playsinline preload="auto"></video>';
    var v=wrap.querySelector('video'); recModalVideo=v;
    v.src=url; v.load();
    v.onloadedmetadata=function(){ try{ v.currentTime=offset; }catch(e){} v.play().catch(function(){}); };
  });
}
function recShiftDay(delta){
  var day=recDateInputVal(); if(!day) return;
  var d=new Date(+day.slice(0,4), +day.slice(4,6)-1, +day.slice(6,8));
  d.setDate(d.getDate()+delta); recSetDateInput(recDayStr(d)); recLoadDay();
}
$('#recPrevDay').addEventListener('click', function(){ recShiftDay(-1); });
$('#recNextDay').addEventListener('click', function(){ recShiftDay(1); });

/* ---- custom calendar popover (replaces native date picker) ---- */
var recCalOpen=false, recCalView=null; // recCalView = {y, m} month being shown (m: 0-11)
function recCalToggle(){ recCalOpen ? recCalClose() : recCalShow(); }
function recCalShow(){
  var cal=$('#recCal'); if(!cal) return;
  var sel=recDateInputVal();
  var base = sel ? new Date(+sel.slice(0,4), +sel.slice(4,6)-1, +sel.slice(6,8)) : new Date();
  recCalView={ y:base.getFullYear(), m:base.getMonth() };
  recCalOpen=true; cal.hidden=false;
  $('#recDate').setAttribute('aria-expanded','true');
  recRenderCal();
}
function recCalClose(){
  var cal=$('#recCal'); if(!cal) return;
  recCalOpen=false; cal.hidden=true;
  $('#recDate').setAttribute('aria-expanded','false');
}
function recCalShiftMonth(delta){
  if(!recCalView) return;
  var d=new Date(recCalView.y, recCalView.m+delta, 1);
  recCalView={ y:d.getFullYear(), m:d.getMonth() };
  recRenderCal();
}
function recRenderCal(){
  var cal=$('#recCal'); if(!cal || !recCalView) return;
  var y=recCalView.y, m=recCalView.m;
  var first=new Date(y, m, 1), startWd=first.getDay(), ndays=new Date(y, m+1, 0).getDate();
  var sel=recDateInputVal(), today=recDayStr(new Date());
  var avail={}; (recCtx.availDays||[]).forEach(function(d){ avail[d]=true; });
  var head='<div class="rc-head">'+
    '<button type="button" class="rc-nav" data-mo="-1" title="이전 달">‹</button>'+
    '<span class="rc-title">'+y+'년 '+(m+1)+'월</span>'+
    '<button type="button" class="rc-nav" data-mo="1" title="다음 달">›</button></div>';
  var wd='<div class="rc-wd">'+REC_WD.map(function(w){return '<span>'+w+'</span>';}).join('')+'</div>';
  var cells='';
  for(var i=0;i<startWd;i++) cells+='<span class="rc-d rc-pad"></span>';
  for(var day=1;day<=ndays;day++){
    var ymd=''+y+pad2(m+1)+pad2(day);
    var cls='rc-d';
    if(ymd===sel) cls+=' on';
    if(ymd===today) cls+=' today';
    if(avail[ymd]) cls+=' has';
    cells+='<button type="button" class="'+cls+'" data-ymd="'+ymd+'">'+day+'</button>';
  }
  cal.innerHTML=head+wd+'<div class="rc-grid">'+cells+'</div>';
}
$('#recDate').addEventListener('click', function(e){ e.stopPropagation(); recCalToggle(); });
$('#recCal').addEventListener('click', function(e){
  e.stopPropagation();
  var nav=e.target.closest('.rc-nav'); if(nav){ recCalShiftMonth(+nav.dataset.mo); return; }
  var d=e.target.closest('.rc-d[data-ymd]'); if(!d) return;
  recSetDateInput(d.dataset.ymd); recCalClose(); recLoadDay();
});
document.addEventListener('click', function(){ if(recCalOpen) recCalClose(); });
document.addEventListener('keydown', function(e){ if(e.key==='Escape' && recCalOpen) recCalClose(); });
// No live playback on this tab; recStop tears down only the event modal video.
var recModalVideo=null;
function recStop(){ if(recModalVideo){ try{ recModalVideo.pause(); recModalVideo.removeAttribute('src'); recModalVideo.load(); }catch(e){} recModalVideo=null; } }
/* ============================================================ RELAY / CONN */
var relayDownAt=Date.now();
function renderConn(){
  appEl.classList.toggle('relay-down', demo.relayDown);
  $('#banner').classList.toggle('show', demo.relayDown);
  $('#connDot').className='dot '+(demo.relayDown?'bad':'live');
  $('#connTxt').textContent=demo.relayDown?'relay 연결 끊김':'relay 연결됨';
  if(demo.relayDown){ $('#bannerSub').textContent='· 마지막 응답 '+fmtAgo((Date.now()-relayDownAt)/1000)+' 전 · 재연결 시도 중…'; }
}

/* ============================================================ SIM LOOP */
function simAgent(a, now){
  if(!a.online) return;
  if(Math.random()<0.82){ a.last_publish_ms=now; a.publish_count++; }
  a.bytes_out += 700000 + a.streams.length*30000 + Math.random()*400000;
  a.bytes_in  += 280000 + Math.random()*200000;
  var nAct=activeStreams(a).length;
  var dMax=1200+nAct*430, uMax=2600+nAct*840;
  a.smooth.down += (Math.random()-0.5)*700; a.smooth.up += (Math.random()-0.5)*1100;
  a.smooth.down=Math.max(400, Math.min(dMax, a.smooth.down));
  a.smooth.up  =Math.max(800, Math.min(uMax, a.smooth.up));
  a.tput.down=Math.round(a.smooth.down); a.tput.up=Math.round(a.smooth.up);
  a.streams.forEach(function(s){ if(s.active && Math.random()<0.1){ s.ws_watchers=Math.max(0,Math.min(6, s.ws_watchers+(Math.random()<0.5?-1:1))); } });
  // watcher join/leave
  if(now>a.nextWEvt){
    a.nextWEvt=now+8000+Math.random()*14000;
    var isSel = (selected===a.id && curTab==='status');
    if(a.watchers.length>0 && Math.random()<0.42){
      var idx=Math.floor(Math.random()*a.watchers.length);
      var removed=a.watchers[idx];
      if(isSel){ var tr=$('#watchBody tr[data-id="'+removed.id+'"]'); if(tr){ tr.classList.add('row-out'); a.watchers.splice(idx,1); setTimeout(function(){renderWatchers(a);},460); } else { a.watchers.splice(idx,1); renderWatchers(a);} }
      else a.watchers.splice(idx,1);
    } else if(a.watchers.length<10){
      a.watchers.push({id:a.nextWid++, ip:randIp(), since:now});
      if(isSel) renderWatchers(a);
    }
  }
}

function tick(){
  var now=Date.now();
  if(!demo.relayDown){ state.relay.uptime_sec++; } // cosmetic between 2s polls; real data via pollState
  renderConn();
  // live UI refresh
  if(state.agents.length>1) updateSidebarLive();
  if($('#manage-view').classList.contains('active')){ patchBranchCards(); return; }
  if(selected===null){ if(!demo.relayDown) updateOverviewLive(); }
  else {
    var a=agentById(selected); if(!a) return;
    renderStatus(a);
    if(curTab==='status'){
      $$('#watchBody .agocell').forEach(function(c){ c.textContent=fmtAgo(Math.floor((now-(+c.dataset.since))/1000))+' 전'; });
      $$('#streamBody [data-wsid]').forEach(function(c){ var s=a.streams.filter(function(x){return x.id===c.dataset.wsid;})[0]; if(s&&s.active) c.textContent=s.ws_watchers; });
    } else {
      updateCellClocks();
      $$('#grid .cell').forEach(function(c){ var s=a.streams.filter(function(x){return x.id===c.dataset.id;})[0]; var wn=c.querySelector('.wn'); if(s&&s.active&&wn) wn.lastChild.textContent=s.ws_watchers; });
    }
    $('#agSince').textContent = a.online? ('연결 시작 '+fmtAgo((Date.now()-a.since_ms)/1000)+' 전 · '+a.dvr) : ('마지막 접속 '+fmtAgo((Date.now()-a.last_publish_ms)/1000)+' 전 · '+a.dvr);
  }
  $('#deskClock').textContent=fmtClock(new Date());
}
function updateSidebarLive(){
  state.agents.forEach(function(a){
    var el=$('[data-aisub="'+a.id+'"]'); if(!el) return;
    el.textContent = a.online? ('시청자 '+a.watchers.length+' · 스트림 '+activeStreams(a).length) : ('오프라인 · '+fmtAgo((Date.now()-a.last_publish_ms)/1000)+' 전');
  });
}

var loopId=null, snapId=null, pollId=null, opsSnapId=null;
function startLoop(){
  if(loopId) return;
  renderSidebar(); routeFromPath();
  // first fetch; once data is in, honor a deep link to an agent that wasn't loaded yet
  pollState().then(function(){ if(selected===null) routeFromPath(); });
  pollId=setInterval(function(){ if(!document.hidden) pollState(); }, 2000);
  loopId=setInterval(tick,1000);
  opsSnapId=setInterval(function(){ if(!document.hidden && curTab==='status') refreshOpsSnap(); }, 1500);
  snapId=setInterval(function(){
    if(demo.relayDown) return;
    var f=$('#snapFlash'); if(f && selected && (agentById(selected)||{}).online){ f.classList.remove('on'); void f.offsetWidth; f.classList.add('on'); }
  },4500);
}
function stopLoop(){
  if(loopId){clearInterval(loopId);loopId=null;}
  if(snapId){clearInterval(snapId);snapId=null;}
  if(pollId){clearInterval(pollId);pollId=null;}
  if(opsSnapId){clearInterval(opsSnapId);opsSnapId=null;}
  stopLiveGrid();
}

/* ============================================================ DRAWER */
var drawer=$('#drawer'), scrim=$('#scrim');
function openDrawer(){ drawer.classList.add('show'); scrim.classList.add('show'); }
function closeDrawer(){ drawer.classList.remove('show'); scrim.classList.remove('show'); }
$('#gearBtn').addEventListener('click', openDrawer);

/* ===== 관리 페이지 (지점 / 알림 / 보안) ===== */
if($('#manageBtn')) $('#manageBtn').addEventListener('click', function(){ go('manage'); });
function setManageTab(name){
  if(['branches','alerts','security'].indexOf(name)<0) name='branches';
  LS.setItem('opsview.mgtab', name);
  $$('#manageNav .mg-tab').forEach(function(b){ b.classList.toggle('active', b.dataset.mgtab===name); });
  $$('#manage-view .mg-pane').forEach(function(p){ p.classList.toggle('active', p.id==='mg-'+name); });
}
if($('#manageNav')) $('#manageNav').addEventListener('click', function(e){ var b=e.target.closest('.mg-tab'); if(b) setManageTab(b.dataset.mgtab); });
function loadManage(){
  setManageTab(LS.getItem('opsview.mgtab')||'branches');
  loadBranches();
  loadHiddenAgents();
}
$('#drawerClose').addEventListener('click', closeDrawer);
scrim.addEventListener('click', closeDrawer);

function syncSegs(){
  $$('.seg[data-group]').forEach(function(seg){
    var g=seg.dataset.group, cur = g==='agentcount'? String(demo.agentCount) : document.documentElement.dataset[g];
    $$('button',seg).forEach(function(b){ b.classList.toggle('on', b.dataset.val===cur); });
  });
  $$('.swatches[data-group="accent"] .sw').forEach(function(sw){ sw.classList.toggle('on', sw.dataset.val===document.documentElement.dataset.accent); });
}
$$('.seg[data-group]').forEach(function(seg){
  seg.addEventListener('click', function(e){
    var b=e.target.closest('button'); if(!b) return;
    var g=seg.dataset.group;
    if(g==='agentcount'){
      demo.agentCount=+b.dataset.val; PREF.agentCount=demo.agentCount; savePref();
      regenAgents(demo.agentCount);
      if(selected && !agentById(selected)) selected=null;
      renderSidebar(); routeFromPath();
    } else {
      PREF[g]=b.dataset.val; savePref(); applyPref();
    }
    syncSegs();
  });
});
$$('.swatches[data-group="accent"] .sw').forEach(function(sw){ sw.addEventListener('click', function(){ PREF.accent=sw.dataset.val; savePref(); applyPref(); syncSegs(); }); });
syncSegs();

/* ===== 지점(tenant) 관리 ===== */
function tnMsg(t, bad){ var m=$('#tenant-msg'); if(m){ m.textContent=t||''; m.className='tenant-msg'+(bad?' bad':''); } }
// Branch tokens are kept in a JS map, NOT a DOM attribute, so a casual DOM dump /
// screen-share doesn't leak every branch token (reveal/copy read from here).
var branchTokens={};
function loadBranches(){
  var l0=$('#tenant-list');
  if(l0 && !l0.querySelector('.mg-card')) l0.innerHTML='<div class="mg-empty mg-loading">불러오는 중…</div>';
  var fail=function(){ var list=$('#tenant-list'); if(list && !list.querySelector('.mg-card')){ list.innerHTML='<div class="mg-empty">불러오기 실패 · <button class="tn-restore" data-retry>다시 시도</button></div>'; var rb=list.querySelector('[data-retry]'); if(rb) rb.onclick=loadBranches; } };
  fetch('/dashboard/api/agents').then(function(r){ return r.ok? r.json() : null; }).then(function(d){
    var list=$('#tenant-list'); if(!list) return;
    if(!d){ fail(); return; }
    var editable=!!d.editable;
    var ro=$('#manage-ro'); if(ro) ro.style.display = editable ? 'none' : '';
    var pwSet=$('#pw-set'); if(pwSet) pwSet.style.display = editable ? '' : 'none';
    var alSet=$('#alert-set'); if(alSet){ alSet.style.display = editable ? '' : 'none'; if(editable) loadAlertCfg(); }
    var addUI=$('#tenant-add'); if(addUI) addUI.style.display = editable ? '' : 'none';
    // registry agents (id,name,token,online) joined with any live-only agents (e.g. the
    // legacy "default", which has no registry row) — the latter render as limited cards.
    var reg={}; branchTokens={};
    (d.agents||[]).forEach(function(a){ reg[a.id]=true; if(a.token) branchTokens[a.id]=a.token; });
    var extra=(state.agents||[]).filter(function(a){ return !reg[a.id]; })
      .map(function(a){ return {id:a.id, name:a.name, token:'', online:a.online, legacy:true}; });
    var all=(d.agents||[]).concat(extra);
    if(!all.length){ list.innerHTML='<div class="mg-empty">등록된 지점이 없습니다.'+(editable?' 아래에서 추가하세요.':'')+'</div>'; return; }
    list.innerHTML=all.map(function(a){ return branchCardHTML(a, editable); }).join('');
    wireBranchCards();
  }).catch(fail);
}
// Volatile (live) fields of a branch card, derived from polled state.
function branchCardData(a){
  var live=agentById(a.id);
  var online=!!((a&&a.online) || (live&&live.online));
  var dvrs=(live&&live.dvrs)||[];
  var chTotal=dvrs.reduce(function(n,x){return n+(x.channels||0);},0) || (live?(live.streams||[]).length:0);
  return { online:online, ndvr:dvrs.length, chTotal:chTotal, chAct:live?activeStreams(live).length:0,
    watchers:live?(live.watchers||[]).length:0,
    status: online ? '실시간' : (live ? '마지막 접속 '+fmtAgo((Date.now()-live.last_publish_ms)/1000)+' 전' : '접속 기록 없음') };
}
function branchCardHTML(a, editable){
  var s=branchCardData(a), canEdit = editable && !a.legacy && !!a.token;
  return '<div class="mg-card'+(s.online?'':' off')+'" data-id="'+escAttr(a.id)+'">'+
    '<div class="mgc-head"><span class="mgc-dot'+(s.online?' on':'')+'"></span>'+
      '<div class="mgc-id"><span class="mgc-name">'+escHtml(a.name||a.id)+'</span><span class="mgc-code mono">'+escHtml(a.id)+(a.legacy?' · 기본':'')+'</span></div>'+
      '<span class="mgc-badge'+(s.online?' on':'')+'">'+(s.online?'온라인':'오프라인')+'</span></div>'+
    '<div class="mgc-stats">'+
      '<div class="mgc-stat"><span class="mgc-k">DVR·채널</span><span class="mgc-v" data-mgc="ch">'+s.ndvr+'·'+s.chAct+'/'+s.chTotal+'</span></div>'+
      '<div class="mgc-stat"><span class="mgc-k">시청자</span><span class="mgc-v" data-mgc="watchers">'+s.watchers+'</span></div>'+
      '<div class="mgc-stat"><span class="mgc-k">상태</span><span class="mgc-v" data-mgc="status">'+s.status+'</span></div>'+
    '</div>'+
    (a.token?('<div class="mgc-token"><span class="mgc-tok mono">••••••••••••</span>'+
      '<button class="mgc-ic" data-act="reveal" title="토큰 보기">보기</button>'+
      '<button class="mgc-ic" data-act="copy" title="토큰 복사">복사</button></div>'):'')+
    '<div class="mgc-actions">'+
      (canEdit?'<button data-act="rename">이름변경</button><button data-act="regen">토큰 재발급</button>':'')+
      '<button data-act="reconnect"'+(s.online?'':' disabled')+'>재접속</button>'+
      '<button data-act="hide">숨기기</button>'+
      (canEdit?'<button class="danger" data-act="delete">삭제</button>':'')+
    '</div></div>';
}
// Live, non-destructive refresh: update only the volatile fields on existing cards
// so online/watcher/last-seen don't freeze while the 관리 page is open — without
// clobbering an in-flight rename input or a revealed token.
function patchBranchCards(){
  var list=$('#tenant-list'); if(!list) return;
  list.querySelectorAll('.mg-card[data-id]').forEach(function(card){
    if(card.querySelector('.mgc-rename')) return; // mid-edit
    var s=branchCardData({id:card.dataset.id});
    card.classList.toggle('off', !s.online);
    var dot=card.querySelector('.mgc-dot'); if(dot) dot.classList.toggle('on', s.online);
    var badge=card.querySelector('.mgc-badge'); if(badge){ badge.classList.toggle('on', s.online); badge.textContent=s.online?'온라인':'오프라인'; }
    var setV=function(k,v){ var el=card.querySelector('.mgc-v[data-mgc="'+k+'"]'); if(el && el.textContent!==v) el.textContent=v; };
    setV('ch', s.ndvr+'·'+s.chAct+'/'+s.chTotal); setV('watchers', String(s.watchers)); setV('status', s.status);
    var rb=card.querySelector('[data-act="reconnect"]'); if(rb && rb.textContent==='재접속') rb.disabled=!s.online;
  });
}
function genTokenStr(){ var a=new Uint8Array(16); (crypto||window.crypto).getRandomValues(a); return [].map.call(a,function(b){return ('0'+b.toString(16)).slice(-2);}).join(''); }
function upsertTenant(id,name,token){
  fetch('/dashboard/api/agents',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({id:id,name:name,token:token})})
    .then(function(r){ if(r.ok){ tnMsg('저장됨'); } else { r.text().then(function(t){ tnMsg('실패: '+t,true); }); } loadBranches(); });
}
function startRename(card,id,token,curName){
  var nameEl=card.querySelector('.mgc-name'); if(!nameEl) return;
  var inp=document.createElement('input'); inp.className='mgc-rename'; inp.value=curName;
  nameEl.replaceWith(inp); inp.focus(); inp.select();
  inp.addEventListener('keydown',function(e){ if(e.key==='Enter') inp.blur(); else if(e.key==='Escape'){ inp.value=curName; inp.blur(); } });
  inp.addEventListener('blur',function(){ var nn=inp.value.trim(); if(nn && nn!==curName) upsertTenant(id,nn,token); else loadBranches(); },{once:true});
}
function wireBranchCards(){
  var list=$('#tenant-list'); if(!list) return;
  list.onclick=function(e){
    var btn=e.target.closest('[data-act]'); if(!btn) return;
    var card=e.target.closest('.mg-card'); if(!card) return;
    var id=card.dataset.id, act=btn.dataset.act;
    var tokEl=card.querySelector('.mgc-tok'); var token=branchTokens[id]||'';
    var nameEl=card.querySelector('.mgc-name'); var name=nameEl?nameEl.textContent:id;
    if(act==='reveal'){ var shown=tokEl.dataset.shown==='1';
      if(shown){ tokEl.textContent='••••••••••••'; tokEl.dataset.shown=''; btn.textContent='보기'; if(tokEl._mask){ clearTimeout(tokEl._mask); tokEl._mask=0; } }
      else { tokEl.textContent=token; tokEl.dataset.shown='1'; btn.textContent='숨김'; tokEl._mask=setTimeout(function(){ tokEl.textContent='••••••••••••'; tokEl.dataset.shown=''; btn.textContent='보기'; }, 20000); } }
    else if(act==='copy'){ if(navigator.clipboard) navigator.clipboard.writeText(token); tnMsg('토큰 복사됨'); }
    else if(act==='reconnect'){ btn.disabled=true; var o=btn.textContent; btn.textContent='요청 중…';
      fetch('/dashboard/api/agent-control',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({agent_id:id,action:'reconnect'})})
        .then(function(r){ btn.textContent=r.ok?'요청됨 ✓':(r.status===409?'오프라인':'실패'); })
        .catch(function(){ btn.textContent='실패'; })
        .finally(function(){ setTimeout(function(){ btn.disabled=false; btn.textContent=o; }, 3000); }); }
    else if(act==='hide'){ hideAgent(id,true); }
    else if(act==='rename'){ startRename(card,id,token,name); }
    else if(act==='regen'){ uiConfirm({title:'새 연결 코드 발급', message:'"'+(name||id)+'"의 연결 코드를 새로 발급합니다. 이 매장 에이전트 설정에도 새 코드를 넣어야 다시 연결됩니다.', okLabel:'재발급', danger:true}).then(function(ok){ if(ok) upsertTenant(id,name,genTokenStr()); }); }
    else if(act==='delete'){ uiConfirm({title:'지점 삭제', message:'"'+(name||id)+'"을(를) 삭제합니다. 이 매장 에이전트는 더 이상 연결할 수 없습니다. (대신 "숨기기"는 되돌릴 수 있어요.)', okLabel:'삭제', danger:true}).then(function(ok){ if(ok) fetch('/dashboard/api/agents?id='+encodeURIComponent(id),{method:'DELETE'}).then(function(r){ tnMsg(r.ok?'삭제됨':'삭제 실패',!r.ok); loadBranches(); }); }); }
  };
}
if($('#tn-gen')) $('#tn-gen').addEventListener('click', function(){ $('#tn-token').value=genTokenStr(); });
if($('#pw-save')) $('#pw-save').addEventListener('click', function(){
  var pw=$('#pw-new').value;
  var m=$('#pw-msg'); var set=function(t,bad){ m.textContent=t; m.className='tenant-msg'+(bad?' bad':''); };
  if(pw.length<4){ set('비밀번호는 4자 이상이어야 합니다.', true); return; }
  fetch('/dashboard/api/password',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({password:pw})})
    .then(function(r){ if(r.ok){ $('#pw-new').value=''; set('비밀번호가 변경되었습니다.'); }
      else r.text().then(function(t){ set('변경 실패: '+t, true); }); });
});
/* --- hidden agents (지점 관리: 숨긴 지점 복원) --- */
function loadHiddenAgents(){
  var block=$('#hidden-block'), list=$('#hidden-list');
  if(!block||!list) return;
  fetch('/dashboard/api/state').then(function(r){ return r.ok?r.json():null; }).then(function(s){
    var hidden=(s&&s.hidden_agents)||[];
    if(!hidden.length){ block.style.display='none'; list.innerHTML=''; return; }
    block.style.display='';
    list.innerHTML=hidden.map(function(a){
      return '<div class="hidden-row"><span>'+escHtml(a.name||a.id)+' <span class="mono" style="opacity:.45;">'+escHtml(a.id)+'</span></span>'+
        '<button class="tn-restore" data-restore="'+escAttr(a.id)+'">복원</button></div>';
    }).join('');
  }).catch(function(){});
}
if($('#hidden-list')) $('#hidden-list').addEventListener('click', function(e){
  var b=e.target.closest('[data-restore]'); if(b) hideAgent(b.dataset.restore, false);
});

/* --- fault alerts (telegram / webhook) --- */
function alMsg(t,bad){ var m=$('#al-msg'); if(m){ m.textContent=t; m.className='tenant-msg'+(bad?' bad':''); } }
function loadAlertCfg(){
  fetch('/dashboard/api/alert-config').then(function(r){ return r.ok?r.json():null; }).then(function(c){
    if(!c) return;
    if($('#al-enabled')) $('#al-enabled').checked=!!c.enabled;
    if($('#al-tg-token')) $('#al-tg-token').value=c.telegram_token||'';
    if($('#al-tg-chat')) $('#al-tg-chat').value=c.telegram_chat||'';
    if($('#al-webhook')) $('#al-webhook').value=c.webhook_url||'';
  }).catch(function(){});
}
if($('#al-save')) $('#al-save').addEventListener('click', function(){
  var body={ enabled:$('#al-enabled').checked, telegram_token:$('#al-tg-token').value.trim(),
    telegram_chat:$('#al-tg-chat').value.trim(), webhook_url:$('#al-webhook').value.trim() };
  alMsg('저장 중…');
  fetch('/dashboard/api/alert-config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)})
    .then(function(r){ if(r.ok) alMsg('저장됨'); else r.text().then(function(t){ alMsg('저장 실패: '+t, true); }); });
});
if($('#al-test')) $('#al-test').addEventListener('click', function(){
  alMsg('테스트 전송 중… (먼저 저장돼 있어야 함)');
  fetch('/dashboard/api/alert-test',{method:'POST'})
    .then(function(r){ if(r.ok) alMsg('테스트 전송됨 — 텔레그램/웹훅 확인'); else alMsg(r.status===409?'채널 미설정 — 저장 먼저':'전송 실패 ('+r.status+')', true); });
});

if($('#tn-add')) $('#tn-add').addEventListener('click', function(){
  var id=$('#tn-id').value.trim(), name=$('#tn-name').value.trim(), token=$('#tn-token').value.trim();
  if(!id || !token){ tnMsg('지점 ID와 토큰은 필수입니다.', true); return; }
  fetch('/dashboard/api/agents',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({id:id,name:name,token:token})})
    .then(function(r){ if(r.ok){ $('#tn-id').value='';$('#tn-name').value='';$('#tn-token').value=''; tnMsg('추가됨'); loadBranches(); }
      else r.text().then(function(t){ tnMsg('추가 실패: '+t, true); }); });
});

/* ============================================================ LIVE INLINE EDIT */
function escHtml(s){ return String(s==null?'':s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;'); }
function escAttr(s){ return escHtml(s).replace(/"/g,'&quot;'); }
// In-app confirm dialog → Promise<bool>. Replaces native confirm() for destructive
// actions so they're dark-themed, show the friendly name, and aren't a jarring OS popup.
function uiConfirm(opts){
  opts=opts||{};
  return new Promise(function(resolve){
    var m=$('#confirmModal'); if(!m){ resolve(window.confirm(opts.message||'')); return; }
    var ok=$('#confirmOk'), cancel=$('#confirmCancel');
    $('#confirmTitle').textContent=opts.title||'확인';
    $('#confirmMsg').textContent=opts.message||'';
    ok.textContent=opts.okLabel||'확인'; ok.classList.toggle('danger', !!opts.danger);
    function done(v){ m.classList.remove('show'); ok.onclick=null; cancel.onclick=null; m.onclick=null; document.removeEventListener('keydown', esc); resolve(v); }
    function esc(e){ if(e.key==='Escape') done(false); else if(e.key==='Enter') done(true); }
    ok.onclick=function(){ done(true); };
    cancel.onclick=function(){ done(false); };
    m.onclick=function(e){ if(e.target===m) done(false); };
    document.addEventListener('keydown', esc);
    m.classList.add('show'); ok.focus();
  });
}

var liveEditing = false;
if ($('#liveEditBtn')) $('#liveEditBtn').addEventListener('click', function(){
  if (!selected) return;
  var a = agentById(selected); if (!a) return;
  liveEditing = !liveEditing;
  var b = $('#liveEditBtn'); b.textContent = liveEditing ? '완료' : '편집'; b.classList.toggle('on', liveEditing);
  renderGrid(a);
});
// wired by renderGrid after cells are built (only when liveEditing)
function wireLiveEdit(a){
  var grid = $('#grid');
  grid.classList.add('editing');
  $$('#grid .cell-name').forEach(function(inp){
    inp.addEventListener('change', function(){ postMeta(selected, parseInt(inp.dataset.dvr), null, [{ ch_num: parseInt(inp.dataset.ch), name: inp.value }]); });
    inp.addEventListener('keydown', function(e){ if (e.key==='Enter') inp.blur(); });
    inp.addEventListener('pointerdown', function(e){ e.stopPropagation(); });
  });
  var dragging = null;
  grid.addEventListener('dragstart', function(e){ var c=e.target.closest('.cell'); if(c){ dragging=c; c.classList.add('drag'); } });
  grid.addEventListener('dragend', function(){ if(dragging){ dragging.classList.remove('drag'); dragging=null; saveLiveOrder(a); } });
  grid.addEventListener('dragover', function(e){ e.preventDefault(); if(!dragging) return;
    var after=cellAfter(grid, e.clientX, e.clientY);
    // only reflow when the insertion point actually changes (avoids restarting the
    // slide animation on every mousemove)
    if(after===dragging) return;
    if(after==null){ if(grid.lastElementChild===dragging) return; }
    else if(after===dragging.nextElementSibling) return;
    flipMove(grid, dragging, function(){ if(after==null) grid.appendChild(dragging); else grid.insertBefore(dragging, after); });
  });
}
// FLIP: animate the non-dragged cells sliding to their new grid slots when the
// drag reorders the DOM, instead of snapping. Preserves the live <video>s (we move
// nodes, never rebuild).
function flipMove(grid, dragging, mutate){
  var first=[];
  [].slice.call(grid.querySelectorAll('.cell')).forEach(function(c){ if(c!==dragging) first.push([c, c.getBoundingClientRect()]); });
  mutate();
  first.forEach(function(p){
    var c=p[0], f=p[1], l=c.getBoundingClientRect(), dx=f.left-l.left, dy=f.top-l.top;
    if(dx||dy){
      c.style.transition='none';
      c.style.transform='translate('+dx+'px,'+dy+'px)';
      c.getBoundingClientRect(); // force reflow so the inverted start is applied
      c.style.transition='transform .2s cubic-bezier(.2,.8,.2,1)';
      c.style.transform='';
    }
  });
}
function cellAfter(grid, x, y){
  var cells=[].slice.call(grid.querySelectorAll('.cell:not(.drag)'));
  for(var i=0;i<cells.length;i++){ var r=cells[i].getBoundingClientRect(); var cy=r.top+r.height/2; if(y<cy) return cells[i]; if(Math.abs(y-cy)<=r.height/2 && x<r.left+r.width/2) return cells[i]; }
  return null;
}
function saveLiveOrder(a){
  var cells=[].slice.call($('#grid').querySelectorAll('.cell'));
  var byDvr={};
  cells.forEach(function(c){ var d=parseInt(c.dataset.dvr); (byDvr[d]=byDvr[d]||[]).push(parseInt(c.dataset.ch)); });
  Object.keys(byDvr).forEach(function(dk){
    var dvrId=parseInt(dk), active=byDvr[dvrId];
    var inactive=(a.chans||[]).filter(function(ch){return ch.dvr_id===dvrId && active.indexOf(ch.ch_num)<0;})
      .sort(function(x,y){return (x.order||0)-(y.order||0);}).map(function(ch){return ch.ch_num;});
    postMeta(selected, dvrId, active.concat(inactive), null);
  });
}

function postMeta(agentId, dvrId, order, renames){
  var body = { agent_id: agentId, dvr_id: dvrId };
  if (order) body.order = order;
  if (renames) body.renames = renames;
  fetch('/dashboard/api/channel-meta', { method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body) })
    .then(function(r){ if (r.status===409) { alert('에이전트 오프라인 — 편집을 적용할 수 없습니다.'); } });
}

})();
