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
function codecFromInit(d){ for(var i=0;i+8<d.length;i++){ if(d[i]===0x61&&d[i+1]===0x76&&d[i+2]===0x63&&d[i+3]===0x43) return 'avc1.'+_hx(d[i+5])+_hx(d[i+6])+_hx(d[i+7]); } return null; }
// playWS(video, wsUrl, onFail, onEnd): onFail fires when the stream never starts
// (pre-init) so the caller can fall back to HLS; onEnd fires when a live stream
// that HAD started later dies (socket close/error/decode error) so the caller can
// reconnect ("zombie" live). A deliberate close() suppresses both.
function playWS(video, wsUrl, onFail, onEnd){
  var MS=_msCtor();
  if(!MS){ onFail&&onFail(); return null; }
  var managed=(typeof window.ManagedMediaSource!=='undefined' && MS===window.ManagedMediaSource);
  var ms=new MS(), sb=null, ws=null, gotInit=false, closed=false, q=[], srcEl=null, objUrl=URL.createObjectURL(ms);
  function cleanup(){
    try{ws&&ws.close();}catch(e){}
    try{if(ms.readyState==='open')ms.endOfStream();}catch(e){}
    try{if(srcEl&&srcEl.parentNode)srcEl.parentNode.removeChild(srcEl);}catch(e){}
    try{URL.revokeObjectURL(objUrl);}catch(e){}
  }
  function fail(){ if(closed)return; closed=true; clearTimeout(tm); cleanup(); onFail&&onFail(); }
  function end(){ if(closed)return; closed=true; clearTimeout(tm); cleanup(); onEnd&&onEnd(); }
  function die(){ if(gotInit) end(); else fail(); } // post-init death -> reconnect; pre-init -> HLS
  function flush(){ if(!sb||sb.updating||!q.length)return; try{sb.appendBuffer(q.shift());}catch(err){ if(err&&err.name==='QuotaExceededError'){ try{ if(sb.buffered.length){var e=sb.buffered.end(sb.buffered.length-1); if(e>8)sb.remove(0,e-4);} }catch(e2){} } else die(); } }
  // ManagedMediaSource (iOS) must attach via a <source> child + disableRemotePlayback;
  // classic MediaSource attaches via video.src.
  if(managed){ video.disableRemotePlayback=true; srcEl=document.createElement('source'); srcEl.type='video/mp4'; srcEl.src=objUrl; video.appendChild(srcEl); }
  else { video.src=objUrl; }
  var tm=setTimeout(function(){ if(!gotInit) fail(); },6000);
  ms.addEventListener('sourceopen', function(){
    try{ ws=new WebSocket(wsUrl); }catch(e){ fail(); return; }
    ws.binaryType='arraybuffer';
    ws.onmessage=function(ev){ if(closed)return; var data=new Uint8Array(ev.data);
      if(!gotInit){ gotInit=true; clearTimeout(tm); var codec=codecFromInit(data); var mime=codec?'video/mp4; codecs="'+codec+'"':'';
        if(!codec||!MS.isTypeSupported(mime)){ fail(); return; }
        try{ sb=ms.addSourceBuffer(mime); }catch(e){ fail(); return; }
        sb.mode='sequence';
        sb.addEventListener('updateend', function(){ flush(); video.play().catch(function(){}); });
        sb.addEventListener('error', die);
      }
      q.push(data); flush();
    };
    ws.onerror=die; ws.onclose=die;
  });
  video.play().catch(function(){});
  return {close:function(){ if(closed)return; closed=true; clearTimeout(tm); cleanup(); }};
}
