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
      id:a.id, name:a.name, online:!!a.connected,
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
fetch('/dashboard/api/state').then(function(r){
  if(r.status===200){ showApp(); } else { setTimeout(function(){pwIn.focus();},120); }
}).catch(function(){ setTimeout(function(){pwIn.focus();},120); });

/* ============================================================ NAVIGATION */
var selected=null;   // null = overview ; else agent id
var curTab=LS.getItem('opsview.tab')||'status';

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
  selected = (target==='overview')? null : target;
  selDvr='all'; liveEditing=false; var _eb=$('#liveEditBtn'); if(_eb){ _eb.textContent='편집'; _eb.classList.remove('on'); } // reset device filter + edit mode on switch
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
  $$('.agent-item').forEach(function(b){ b.classList.toggle('active', (b.dataset.nav==='overview' && selected===null) || b.dataset.nav===selected); });
}

/* --- path routing (History API): clicks pushState a real URL
   (/dashboard or /dashboard/agent/<id>) so back/forward and deep links work.
   The relay serves index.html for any /dashboard/* non-asset path. --- */
var BASE='/dashboard';
function pathFor(target){ return target==='overview' ? BASE : BASE+'/agent/'+encodeURIComponent(target); }
function go(target){
  var path=pathFor(target);
  if(location.pathname.replace(/\/+$/,'')!==path.replace(/\/+$/,'')){ history.pushState({}, '', path); }
  navTo(target);
}
function routeFromPath(){
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
}
$$('#agent-view .tab').forEach(function(b){ b.addEventListener('click', function(){ setTab(b.dataset.tab); }); });

// device (DVR) chip selection: filter the status table + live grid to one device
$('#dvrChips').addEventListener('click', function(e){
  var b=e.target.closest('.dvr-chip'); if(!b || !selected) return;
  selDvr = b.dataset.dvr==='all' ? 'all' : +b.dataset.dvr;
  var a=agentById(selected); if(!a) return;
  renderDvrChips(a); renderStreams(a);
  if(curTab==='live') renderGrid(a);
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
  $('#ovSub').textContent='relay v'+state.relay.version+' · '+t.agentsTotal+'개 지점 연결';
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
    .then(function(r){ if(r.ok){ pollState().then(function(){ if(selected!==null && !agentById(selected) && hidden) go('overview'); }); loadHiddenAgents(); } });
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
function closeModal(){ modal.classList.remove('show'); if(modalPlayer){try{modalPlayer.close&&modalPlayer.close();}catch(e){} modalPlayer=null;} modalCell.innerHTML=''; modalCell.classList.remove('bare'); if(opsModalTimer){clearInterval(opsModalTimer);opsModalTimer=null;} }
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
function openDrawer(){ drawer.classList.add('show'); scrim.classList.add('show'); loadTenants(); loadHiddenAgents(); }
function closeDrawer(){ drawer.classList.remove('show'); scrim.classList.remove('show'); }
$('#gearBtn').addEventListener('click', openDrawer);
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
function loadTenants(){
  fetch('/dashboard/api/agents').then(function(r){ return r.ok? r.json() : null; }).then(function(d){
    var list=$('#tenant-list'); if(!list) return;
    if(!d){ list.innerHTML='<div class="mut" style="font-size:12px;">불러오기 실패</div>'; return; }
    var pwSet=$('#pw-set'); if(pwSet) pwSet.style.display = d.editable ? '' : 'none';
    var alSet=$('#alert-set'); if(alSet){ alSet.style.display = d.editable ? '' : 'none'; if(d.editable) loadAlertCfg(); }
    var addUI=$('.tenant-add');
    if(!d.editable){
      if(addUI) addUI.style.display='none';
      list.innerHTML='<div class="mut" style="font-size:12px;">읽기 전용 — relay에 <code>RELAY_DB</code>(영속 볼륨)를 설정하면 여기서 지점을 추가/삭제할 수 있습니다.</div>';
      return;
    }
    if(addUI) addUI.style.display='';
    if(!d.agents.length){ list.innerHTML='<div class="mut" style="font-size:12px;">등록된 지점이 없습니다. 아래에서 추가하세요.</div>'; return; }
    list.innerHTML=d.agents.map(function(a){
      return '<div class="tenant-row">'+
        '<span class="tn-dot'+(a.online?' on':'')+'"></span>'+
        '<div class="tn-main"><div class="tn-name">'+escHtml(a.name)+' <span class="mut mono">'+escHtml(a.id)+'</span></div>'+
        '<div class="tn-tok mono" title="클릭하면 복사">'+escHtml(a.token)+'</div></div>'+
        '<button class="tn-del" data-id="'+escAttr(a.id)+'">삭제</button></div>';
    }).join('');
    $$('#tenant-list .tn-tok').forEach(function(el){ el.addEventListener('click', function(){ navigator.clipboard && navigator.clipboard.writeText(el.textContent); tnMsg('토큰 복사됨'); }); });
    $$('#tenant-list .tn-del').forEach(function(b){ b.addEventListener('click', function(){
      if(!confirm('지점 "'+b.dataset.id+'" 삭제? (그 매장 에이전트는 더 이상 연결 못 함)')) return;
      fetch('/dashboard/api/agents?id='+encodeURIComponent(b.dataset.id),{method:'DELETE'}).then(function(r){ tnMsg(r.ok?'삭제됨':'삭제 실패', !r.ok); loadTenants(); });
    }); });
  });
}
if($('#tn-gen')) $('#tn-gen').addEventListener('click', function(){
  var a=new Uint8Array(16); (crypto||window.crypto).getRandomValues(a);
  $('#tn-token').value=[].map.call(a,function(b){return ('0'+b.toString(16)).slice(-2);}).join('');
});
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
    .then(function(r){ if(r.ok){ $('#tn-id').value='';$('#tn-name').value='';$('#tn-token').value=''; tnMsg('추가됨'); loadTenants(); }
      else r.text().then(function(t){ tnMsg('추가 실패: '+t, true); }); });
});

/* ============================================================ LIVE INLINE EDIT */
function escHtml(s){ return String(s==null?'':s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;'); }
function escAttr(s){ return escHtml(s).replace(/"/g,'&quot;'); }

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
  grid.addEventListener('dragover', function(e){ e.preventDefault(); if(!dragging) return; var after=cellAfter(grid, e.clientX, e.clientY); if(after==null) grid.appendChild(dragging); else grid.insertBefore(dragging, after); });
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
