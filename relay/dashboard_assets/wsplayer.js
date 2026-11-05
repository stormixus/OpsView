function _isIOS(){ return /iPad|iPhone|iPod/.test(navigator.userAgent) || (navigator.platform==='MacIntel' && navigator.maxTouchPoints>1); }
// The smooth low-latency path needs a MediaSource. iOS historically had none (HLS
// only); iOS/iPadOS 17.1+ exposes ManagedMediaSource, which lets iPhones use the WS
// path too — much smoother than segmented HLS. Returns the constructor to use, or null.
function _msCtor(){
  if(window.MediaSource && !_isIOS()) return window.MediaSource;       // desktop/android
  if('ManagedMediaSource' in window) return window.ManagedMediaSource;  // modern iOS/iPadOS
  if(window.MediaSource) return window.MediaSource;                     // older iPadOS with real MSE
  return null;
}
function _wsUsable(){ return !!_msCtor(); }
function _hx(n){ return (n<16?'0':'')+n.toString(16); }
function codecFromInit(d){
  var hevcEntry='hev1';
  for(var p=0;p+4<d.length;p++){
    if(d[p]===0x68&&d[p+1]===0x76&&d[p+2]===0x63&&d[p+3]===0x31)hevcEntry='hvc1';
    if(d[p]===0x68&&d[p+1]===0x65&&d[p+2]===0x76&&d[p+3]===0x31)hevcEntry='hev1';
  }
  for(var i=0;i+8<d.length;i++){
    if(d[i]===0x61&&d[i+1]===0x76&&d[i+2]===0x63&&d[i+3]===0x43) return 'avc1.'+_hx(d[i+5])+_hx(d[i+6])+_hx(d[i+7]);
    if(d[i]===0x68&&d[i+1]===0x76&&d[i+2]===0x63&&d[i+3]===0x43&&i+17<d.length){
      var profile=d[i+5],compat=0;
      for(var bit=0;bit<32;bit++){if(d[i+6+(bit>>3)]&(1<<(7-(bit%8))))compat=(compat+Math.pow(2,bit))>>>0;}
      var codec=hevcEntry+'.'+(['','A','B','C'][profile>>6])+(profile&31)+'.'+compat.toString(16).toUpperCase()+'.'+(profile&32?'H':'L')+d[i+16];
      var last=15;while(last>=10&&!d[i+last])last--;
      for(var j=10;j<=last;j++)codec+='.'+_hx(d[i+j]).toUpperCase();
      return codec;
    }
  }
  return null;
}
// Bound simultaneous handshakes, independently of the live decoder cap. A grid
// burst must not time out sockets which are still waiting behind other starts.
var _wsConnectActive=0, _wsConnectQueue=[];
function _pumpWSConnects(){
  while(_wsConnectActive<4 && _wsConnectQueue.length){
    var entry=_wsConnectQueue.shift();
    if(entry.done)continue;
    entry.active=true;_wsConnectActive++;entry.start(entry.release);
  }
}
function _queueWSConnect(start){
  var entry={start:start,active:false,done:false};
  entry.release=function(){
    if(entry.done)return;
    entry.done=true;
    if(entry.active){entry.active=false;_wsConnectActive--;}
    else {var i=_wsConnectQueue.indexOf(entry);if(i>=0)_wsConnectQueue.splice(i,1);}
    _pumpWSConnects();
  };
  _wsConnectQueue.push(entry);_pumpWSConnects();return entry.release;
}
// playWS(video, wsUrl, onFail, onEnd): onFail fires when the stream never starts
// (pre-init) so the caller can fall back to HLS; onEnd fires when a live stream
// that HAD started later dies (socket close/error/decode error) so the caller can
// reconnect ("zombie" live). A deliberate close() suppresses both.
function playWS(video, wsUrl, onFail, onEnd){
  var MS=_msCtor();
  if(!MS){ onFail&&onFail(); return null; }
  var managed=(typeof window.ManagedMediaSource!=='undefined' && MS===window.ManagedMediaSource);
  var ms=new MS(), sb=null, ws=null, gotInit=false, closed=false, initData=null, q=[], srcEl=null, cancelConnect=null, tm=null, objUrl=URL.createObjectURL(ms);
  var started=performance.now();
  if(video.dataset){video.dataset.wsStartedAt=started;delete video.dataset.wsOpenMs;delete video.dataset.wsInitMs;delete video.dataset.wsQueuedMs;}
  function cleanup(){
    video.removeEventListener('error',die);
    try{ws&&ws.close();}catch(e){}
    if(cancelConnect){cancelConnect();cancelConnect=null;}
    try{if(ms.readyState==='open')ms.endOfStream();}catch(e){}
    try{if(srcEl&&srcEl.parentNode)srcEl.parentNode.removeChild(srcEl);}catch(e){}
    try{URL.revokeObjectURL(objUrl);}catch(e){}
  }
  function fail(){ if(closed)return; closed=true; clearTimeout(tm); cleanup(); onFail&&onFail(); }
  function end(){ if(closed)return; closed=true; clearTimeout(tm); cleanup(); onEnd&&onEnd(); }
  function die(){ if(gotInit) end(); else fail(); } // post-init death -> reconnect; pre-init -> HLS
  function flush(){ if(!sb||sb.updating||!q.length)return; var next=q.shift();try{sb.appendBuffer(next);}catch(err){ if(err&&err.name==='QuotaExceededError'){ q.unshift(next);try{ if(sb.buffered.length){var e=sb.buffered.end(sb.buffered.length-1); if(e>8)sb.remove(0,e-4);} }catch(e2){} } else die(); } }
  // ManagedMediaSource (iOS) must attach via a <source> child + disableRemotePlayback;
  // classic MediaSource attaches via video.src.
  if(managed){ video.disableRemotePlayback=true; srcEl=document.createElement('source'); srcEl.type='video/mp4'; srcEl.src=objUrl; video.appendChild(srcEl); }
  else { video.src=objUrl; }
  function prepare(){
    if(closed||sb||!initData||ms.readyState!=='open')return;
    var codec=codecFromInit(initData),mime=codec?'video/mp4; codecs="'+codec+'"':'';
    if(!codec||!MS.isTypeSupported(mime)){fail();return;}
    try{sb=ms.addSourceBuffer(mime);}catch(e){fail();return;}
    gotInit=true;clearTimeout(tm);sb.mode='sequence';
    sb.addEventListener('updateend',function(){flush();video.play().catch(function(){});});
    sb.addEventListener('error',die);flush();
  }
  ms.addEventListener('sourceopen',prepare);
  video.addEventListener('error',die);
  // Establish the socket while MSE opens instead of serializing both handshakes.
  cancelConnect=_queueWSConnect(function(release){
    cancelConnect=release;
    if(closed){release();return;}
    if(video.dataset)video.dataset.wsQueuedMs=Math.round(performance.now()-started);
    tm=setTimeout(function(){ if(!gotInit) fail(); },6000);
    try{ws=new WebSocket(wsUrl);}catch(e){fail();return;}
    ws.binaryType='arraybuffer';
    ws.onopen=function(){release();if(video.dataset)video.dataset.wsOpenMs=Math.round(performance.now()-started);};
    ws.onmessage=function(ev){
      if(closed)return;
      var data=new Uint8Array(ev.data);
      if(!initData){initData=data;if(video.dataset)video.dataset.wsInitMs=Math.round(performance.now()-started);}
      if(q.length>=512){die();return;}
      q.push(data);prepare();flush();
    };
    ws.onerror=die;ws.onclose=die;
  });

  video.play().catch(function(){});
  if(closed)return null; // synchronous constructor failure must not block caller retry
  return {starting:function(){return !closed&&!gotInit;},close:function(){ if(closed)return; closed=true; clearTimeout(tm); cleanup(); }};
}
