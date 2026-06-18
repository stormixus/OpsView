// Live Wall: one composited <video> + a transparent grid overlay (names + click
// targets). Clicking a cell opens that channel's single live stream. No rec/UI.
(function(){
  var qs=new URLSearchParams(location.search);
  var agent=qs.get('agent')||'default';
  var video=document.getElementById('wallvid');
  var overlay=document.getElementById('wallgrid');
  var wsBase=(location.protocol==='https:'?'wss':'ws')+'://'+location.host+'/surv/ws/';
  var wallPath=(agent==='default'?'wall':agent+'/wall');
  var player=null, backoff=0, reconn=null;

  function playWall(){
    if(player){ try{player.close&&player.close();}catch(e){} player=null; }
    player=playWS(video, wsBase+wallPath, function(){}, function(){ reconnect(); });
  }
  function reconnect(){
    backoff=backoff?Math.min(backoff*2,15000):1000;
    clearTimeout(reconn); reconn=setTimeout(playWall, backoff);
  }
  // reset backoff while frames advance
  setInterval(function(){ if(video.currentTime>0 && !video.paused) backoff=0; }, 3000);

  function buildOverlay(layout){
    overlay.style.gridTemplateColumns='repeat('+layout.cols+',1fr)';
    overlay.style.gridTemplateRows='repeat('+layout.rows+',1fr)';
    overlay.innerHTML='';
    (layout.cells||[]).forEach(function(c){
      var d=document.createElement('button');
      d.className='wcell'; d.title=c.name||c.id;
      d.innerHTML='<span class="wname"></span>';
      d.querySelector('.wname').textContent=c.name||c.id;
      d.addEventListener('click', function(){ enlarge(c); });
      overlay.appendChild(d);
    });
  }
  function enlarge(c){
    var big=document.getElementById('wallbig'), bv=document.getElementById('wallbigvid');
    big.classList.add('show');
    var p=playWS(bv, wsBase+(agent==='default'?'':agent+'/')+c.id, function(){}, function(){});
    big._p=p;
    big.querySelector('.wbig-name').textContent=c.name||c.id;
    function close(){ big.classList.remove('show'); if(big._p){try{big._p.close&&big._p.close();}catch(e){}} big._p=null; bv.removeAttribute('src'); document.removeEventListener('keydown', onKey); }
    function onKey(e){ if(e.key==='Escape') close(); }
    document.addEventListener('keydown', onKey);
    big.querySelector('.wbig-close').onclick=close;
    big.onclick=function(e){ if(e.target===big) close(); };
  }

  function loadLayout(){
    fetch('/dashboard/api/wall-layout?agent='+encodeURIComponent(agent),{credentials:'same-origin'})
      .then(function(r){ return r.json(); })
      .then(function(l){
        if(!l.enabled){ document.getElementById('wallmsg').textContent='Live Wall 비활성 (RELAY_WALL=1 필요)'; return; }
        if(!l.cells || !l.cells.length){ document.getElementById('wallmsg').textContent='활성 채널 없음'; return; }
        document.getElementById('wallmsg').textContent='';
        buildOverlay(l);
        playWall();
      })
      .catch(function(){ document.getElementById('wallmsg').textContent='레이아웃 로드 실패'; });
  }
  // re-fetch layout on tab re-show (channel set may have changed)
  document.addEventListener('visibilitychange', function(){ if(!document.hidden){ backoff=0; loadLayout(); } });
  loadLayout();
})();
