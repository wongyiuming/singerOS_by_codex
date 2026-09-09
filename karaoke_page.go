package main

const karaokePage = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1,user-scalable=no">
<title>singerOS · K歌</title>
<style>
:root{--bg:#07090d;--panel:#11151d;--line:#2a3240;--text:#f6f8fb;--muted:#929bad;--accent:#785cff;--good:#6ee7a3;--bad:#ff7c88}
*{box-sizing:border-box}body{margin:0;background:radial-gradient(900px 500px at 0 -10%,#1a1640 0,transparent 60%),var(--bg);color:var(--text);font-family:Inter,system-ui,-apple-system,"Segoe UI",sans-serif}
button,select,input{font:inherit}.shell{max-width:1500px;margin:auto;padding:22px}.top{display:flex;align-items:center;justify-content:space-between;gap:14px;margin-bottom:16px}
.brand{display:flex;align-items:center;gap:12px}.mark{width:44px;height:44px;border-radius:14px;background:linear-gradient(135deg,#7c5cff,#4ed8c8);display:grid;place-items:center;font-weight:900}
h1,h2,h3,p{margin:0}.brand p,.muted{color:var(--muted)}a{color:#c4baff;text-decoration:none}
.layout{display:grid;grid-template-columns:330px minmax(0,1fr);gap:16px}.panel{background:rgba(17,21,29,.9);border:1px solid var(--line);border-radius:20px;overflow:hidden}
.pad{padding:18px}.panel-head{display:flex;justify-content:space-between;align-items:center;gap:10px;margin-bottom:14px}
.btn{border:1px solid #394354;background:#202733;color:#fff;border-radius:12px;min-height:44px;padding:0 14px;cursor:pointer}.btn.primary{background:#6e52ef;border-color:#8e79ff}.btn.good{background:#17653f;border-color:#258a59}.btn:disabled{opacity:.4;cursor:not-allowed}
.song-list{display:flex;flex-direction:column;gap:8px}.song{padding:13px;border:1px solid var(--line);border-radius:14px;background:#0d1118;cursor:pointer}.song.active{border-color:#8069ff;background:#17142b}.song strong{display:block}.song small{display:block;color:var(--muted);margin-top:4px}
.main{display:grid;grid-template-rows:auto auto 1fr;gap:16px}.hero{padding:22px}.hero-top{display:flex;justify-content:space-between;gap:15px;align-items:flex-start}.hero h2{font-size:30px;margin-top:4px}.mode{display:flex;gap:8px}.mode .btn.active{background:#6e52ef;border-color:#9b8aff}
.meta{display:flex;gap:8px;flex-wrap:wrap;margin-top:10px}.chip{border:1px solid var(--line);border-radius:999px;padding:6px 9px;font-size:12px;color:#bec6d5}
audio{width:100%;margin-top:18px}.transport{display:flex;gap:9px;flex-wrap:wrap;margin-top:14px}.transport .btn{min-height:50px}
.settings{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-top:15px}.setting{border:1px solid var(--line);background:#0c1016;border-radius:14px;padding:12px}
.setting label{display:block;color:var(--muted);font-size:12px;margin-bottom:7px}select{width:100%;min-height:42px;border:1px solid #3a4455;border-radius:10px;background:#111722;color:white;padding:0 10px}
.monitor-row{display:flex;align-items:center;gap:10px}.monitor-row input[type=range]{width:100%}
.meter{height:7px;border-radius:99px;background:#090c11;overflow:hidden;margin-top:12px}.meter>div{height:100%;width:0;background:linear-gradient(90deg,#53dea0,#ffd065,#ff7782)}
.lyric-grid{display:grid;grid-template-columns:minmax(0,1.25fr) minmax(300px,.75fr);gap:16px}.live{min-height:310px;display:flex;flex-direction:column;justify-content:center;text-align:center;padding:28px}
.live .label{color:#a997ff;text-transform:uppercase;letter-spacing:.14em;font-size:11px;font-weight:800}.current{font-size:36px;line-height:1.3;font-weight:760;margin:20px 0 13px}.next{font-size:19px;color:#8e98aa}
.overview{padding:18px;max-height:410px;overflow:auto}.cue{padding:9px 10px;border-radius:10px;color:#7f899b;font-size:14px;line-height:1.4}.cue.active{background:#27203f;color:#fff}.cue.past{color:#535d6d}
.recordings{margin-top:16px}.rec{display:grid;grid-template-columns:minmax(180px,1fr) auto auto auto;gap:9px;align-items:center;padding:10px;border-top:1px solid #242c39;font-size:13px}.rec small{color:var(--muted)}
.status{font-size:12px;color:var(--muted);margin-top:9px;line-height:1.6}.ok{color:var(--good)}.bad{color:var(--bad)}
.lyrics-full{position:fixed;inset:0;z-index:9999;background:linear-gradient(180deg,#07090d 0%,#0b0d14 100%);display:none;overflow:hidden}
.lyrics-full.open{display:grid;grid-template-rows:auto minmax(0,1fr)}
.lyrics-full-head{display:flex;align-items:center;justify-content:space-between;gap:16px;padding:18px 24px;border-bottom:1px solid var(--line);background:rgba(7,9,13,.96)}
.lyrics-full-head .song-meta{min-width:0}.lyrics-full-head h2{font-size:22px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.lyrics-full-head p{color:var(--muted);margin-top:4px}
.lyrics-full-body{display:grid;grid-template-columns:minmax(0,1.4fr) minmax(320px,.6fr);min-height:0}
.lyrics-stage{display:flex;flex-direction:column;align-items:center;justify-content:center;text-align:center;padding:40px 6vw;min-width:0}
.lyrics-stage .fs-current{font-size:clamp(38px,5vw,76px);line-height:1.28;font-weight:760;max-width:1100px}.lyrics-stage .fs-next{font-size:clamp(20px,2.2vw,34px);line-height:1.35;color:var(--muted);margin-top:28px;max-width:1000px}
.lyrics-full-list{overflow:auto;padding:28px;border-left:1px solid var(--line);scroll-behavior:smooth}.fs-cue{padding:12px 14px;border-radius:12px;color:#667083;font-size:18px;line-height:1.45}.fs-cue.active{background:#2c2448;color:#fff}.fs-cue.past{color:#424a58}
body.lyrics-open{overflow:hidden}
@media(max-width:980px){.layout{grid-template-columns:1fr}.lyric-grid,.settings{grid-template-columns:1fr}.song-list{display:grid;grid-template-columns:repeat(2,1fr)}}
@media(max-width:620px){.shell{padding:12px}.top{align-items:flex-start}.hero-top{flex-direction:column}.song-list{grid-template-columns:1fr}.current{font-size:28px}.rec{grid-template-columns:1fr 1fr}.rec .wide{grid-column:1/-1}}
</style>
</head>
<body>
<main class="shell">
<header class="top">
  <div class="brand"><div class="mark">K</div><div><h1>singerOS K歌</h1><p>Win11 业务验证 · 独立单曲页</p></div></div>
  <a href="/singeros/">返回录音控制台</a>
</header>
<div class="layout">
  <aside class="panel pad">
    <div class="panel-head"><div><h3>歌曲目录</h3><p class="muted">粤语 · RN 本地定向资源</p></div></div>
    <div id="songs" class="song-list"></div>
    <div id="resourceStatus" class="status">目录载入中</div>
  </aside>
  <section class="main">
    <div class="panel hero">
      <div class="hero-top">
        <div><div class="muted">当前点唱</div><h2 id="title">未选择</h2><div id="artist" class="muted"></div><div id="meta" class="meta"></div></div>
        <div class="mode"><button id="original" class="btn">原唱</button><button id="accompaniment" class="btn">伴奏</button></div>
      </div>
      <audio id="track" controls preload="metadata"></audio>
      <div class="transport">
        <button id="start" class="btn primary">开始K歌</button>
        <button id="stop" class="btn" disabled>停止并保存</button>
        <button id="play" class="btn">只播放歌曲</button>
        <button id="lyricsFull" class="btn">歌词全屏</button>
      </div>
      <div class="settings">
        <div class="setting"><label>麦克风输入</label><div style="display:flex;gap:8px"><select id="device"><option value="">自动选择</option></select><button id="devices" class="btn">刷新</button></div></div>
        <div class="setting"><label>实时返听</label><div class="monitor-row"><button id="monitor" class="btn" disabled>打开返听</button><span id="gainText">85%</span><input id="gain" type="range" min="0" max="150" value="85"></div></div>
      </div>
      <div class="meter"><div id="meterBar"></div></div>
      <div id="captureStatus" class="status">未采集麦克风</div>
    </div>
    <div class="lyric-grid">
      <div class="panel live"><div class="label">Realtime lyrics</div><div id="current" class="current">选择歌曲后开始</div><div id="next" class="next"></div></div>
      <div id="overview" class="panel overview"></div>
    </div>
    <div class="panel pad recordings">
      <div class="panel-head"><div><h3>K歌录音</h3><p class="muted">文件名：点唱歌曲名 + 上海时间戳</p></div><button id="refreshRec" class="btn">刷新</button></div>
      <div id="recordings"></div>
      <audio id="recordPlayer" controls></audio>
    </div>
  </section>
</div>
</main>
<div id="lyricsFullView" class="lyrics-full" aria-hidden="true">
  <div class="lyrics-full-head">
    <div class="song-meta"><h2 id="fsTitle">歌词</h2><p id="fsArtist"></p></div>
    <button id="lyricsBack" class="btn">返回</button>
  </div>
  <div class="lyrics-full-body">
    <div class="lyrics-stage"><div id="fsCurrent" class="fs-current">选择歌曲后开始</div><div id="fsNext" class="fs-next"></div></div>
    <div id="fsOverview" class="lyrics-full-list"></div>
  </div>
</div>
<script>
(function(){
'use strict';
var catalog=null,song=null,mode='accompaniment',cues=[],activeCue=-1;
var mic=null,recorder=null,recSession=null,seq=0,queue=Promise.resolve(),startedPerf=0,recording=false;
var AudioCtx=window.AudioContext||window.webkitAudioContext;
var meterCtx=null,meterSource=null,analyser=null,meterRAF=0;
var monitorCtx=null,monitorSource=null,monitorGain=null,monitorLimiter=null,monitoring=false;
var lyricsFullscreen=false;
var $=function(id){return document.getElementById(id)};
function clientLog(event,details){fetch('/singeros/api/client-log',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({event:event,details:details,clientTime:new Date().toISOString(),href:location.href,userAgent:navigator.userAgent}),keepalive:true}).catch(function(){})}
function esc(s){return String(s==null?'':s).replace(/[&<>"']/g,function(c){return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]})}
function fmt(sec){sec=Math.max(0,Math.round(Number(sec)||0));return Math.floor(sec/60)+':'+String(sec%60).padStart(2,'0')}
function trackOf(){return song&&song.tracks? song.tracks[mode]:null}
async function loadCatalog(){
  var r=await fetch('/singeros/api/karaoke/catalog',{cache:'no-store'});catalog=await r.json();
  renderSongs();renderResourceStatus();
  if(!song&&catalog.songs&&catalog.songs.length) selectSong(catalog.songs[0].id);
}
function renderResourceStatus(){
  var el=$('resourceStatus');if(!catalog){el.textContent='目录不可用';return}
  el.innerHTML='模式：RN 本地资源<br>语言：'+esc(catalog.language||'粵語')+'<br>曲目：'+((catalog.songs||[]).length)+' 首'+(catalog.updated_at_shanghai?'<br>更新：'+esc(catalog.updated_at_shanghai):'');
}

function renderSongs(){
  var items=(catalog&&catalog.songs)||[];
  $('songs').innerHTML=items.map(function(x){var sub=[x.artist,x.album,x.year].filter(Boolean).join(' · ');return '<div class="song '+(song&&song.id===x.id?'active':'')+'" data-id="'+esc(x.id)+'"><strong>'+esc(x.title)+'</strong><small>'+esc(sub||x.version)+'</small></div>'}).join('')||'<div class="muted">等待定向录入粤语曲目</div>';
  Array.prototype.forEach.call(document.querySelectorAll('.song'),function(el){el.onclick=function(){if(!recording)selectSong(el.dataset.id)}})
}
function selectSong(id){
  var found=(catalog.songs||[]).find(function(x){return x.id===id});if(!found)return;
  song=found;mode=song.tracks.accompaniment?'accompaniment':'original';renderSongs();applyTrack();
}
function applyTrack(){
  var t=trackOf();if(!t)return;
  $('title').textContent=song.title;$('artist').textContent=song.artist+' · '+t.version;
  $('fsTitle').textContent=song.title;$('fsArtist').textContent=[song.artist,song.album,song.year].filter(Boolean).join(' · ');
  $('meta').innerHTML='<span class="chip">'+esc(mode==='original'?'原唱':'伴奏')+'</span><span class="chip">歌词 '+esc(song.lyrics_language||t.lyrics_language||'zh-Hant-HK')+'</span><span class="chip">'+esc(song.lyrics_version||t.version)+'</span>';
  $('track').src=t.url;$('track').load();cues=(song.lyrics&&song.lyrics.length?song.lyrics:t.lyrics)||[];activeCue=-1;renderOverview();updateLyrics();
  $('original').className='btn '+(mode==='original'?'active':'');$('accompaniment').className='btn '+(mode==='accompaniment'?'active':'');
  clientLog('K歌切换音轨',{song:song.id,mode:mode,version:t.version,lyricsLanguage:song.lyrics_language||t.lyrics_language,lyricsOffset:Number(t.lyrics_offset_seconds)||0});
}
function renderOverview(){
  $('overview').innerHTML=cues.map(function(c,i){return '<div class="cue" id="cue-'+i+'"><small>'+fmt(c.start)+'</small> '+esc(c.text)+'</div>'}).join('')||'<div class="muted">无歌词</div>';
  $('fsOverview').innerHTML=cues.map(function(c,i){return '<div class="fs-cue" id="fs-cue-'+i+'"><small>'+fmt(c.start)+'</small> '+esc(c.text)+'</div>'}).join('')||'<div class="muted">无歌词</div>';
}
function cueIndexAt(t){
  for(var i=0;i<cues.length;i++){if(t>=cues[i].start&&t<cues[i].end)return i}
  for(var j=cues.length-1;j>=0;j--){if(t>=cues[j].start)return j}
  return -1;
}
function updateLyrics(){
  var tr=trackOf(),offset=Number(tr&&tr.lyrics_offset_seconds)||0,t=Math.max(0,($('track').currentTime||0)-offset),idx=cueIndexAt(t);
  if(idx!==activeCue){
    activeCue=idx;
    $('current').textContent=idx>=0?cues[idx].text:'♪';
    $('next').textContent=(idx+1<cues.length)?cues[idx+1].text:'';
    $('fsCurrent').textContent=idx>=0?cues[idx].text:'♪';
    $('fsNext').textContent=(idx+1<cues.length)?cues[idx+1].text:'';
    Array.prototype.forEach.call(document.querySelectorAll('.cue'),function(el,k){el.className='cue '+(k===idx?'active':k<idx?'past':'')});
    Array.prototype.forEach.call(document.querySelectorAll('.fs-cue'),function(el,k){el.className='fs-cue '+(k===idx?'active':k<idx?'past':'')});
    var a=$('cue-'+idx);if(a)a.scrollIntoView({block:'center',behavior:'smooth'});
    var fa=$('fs-cue-'+idx);if(fa&&lyricsFullscreen)fa.scrollIntoView({block:'center',behavior:'smooth'});
  }
}
function openLyricsFullscreen(){
  lyricsFullscreen=true;$('lyricsFullView').classList.add('open');$('lyricsFullView').setAttribute('aria-hidden','false');document.body.classList.add('lyrics-open');
  updateLyrics();clientLog('歌词全屏开启',{song:song&&song.id,mode:mode});
}
function closeLyricsFullscreen(){
  lyricsFullscreen=false;$('lyricsFullView').classList.remove('open');$('lyricsFullView').setAttribute('aria-hidden','true');document.body.classList.remove('lyrics-open');
  clientLog('歌词全屏关闭',{song:song&&song.id,mode:mode});
}
async function listDevices(){
  try{
    var ds=await navigator.mediaDevices.enumerateDevices(),audio=ds.filter(function(x){return x.kind==='audioinput'});
    var old=$('device').value;$('device').innerHTML='<option value="">自动选择</option>'+audio.map(function(d){return '<option value="'+esc(d.deviceId)+'">'+esc(d.label||'未命名麦克风')+'</option>'}).join('');
    if(audio.some(function(d){return d.deviceId===old}))$('device').value=old;
    clientLog('K歌麦克风枚举',{count:audio.length,labels:audio.map(function(d){return d.label})});
  }catch(e){$('captureStatus').innerHTML='<span class="bad">'+esc(e.name+': '+e.message)+'</span>';clientLog('K歌设备枚举失败',{name:e.name,message:e.message})}
}
async function setupMeter(){
  if(!AudioCtx||!mic)return;
  meterCtx=new AudioCtx({latencyHint:'interactive'});
  if(meterCtx.state==='suspended')await meterCtx.resume();
  meterSource=meterCtx.createMediaStreamSource(mic);analyser=meterCtx.createAnalyser();analyser.fftSize=1024;meterSource.connect(analyser);
  var data=new Uint8Array(analyser.fftSize);
  function tick(){if(!analyser)return;analyser.getByteTimeDomainData(data);var sum=0;for(var i=0;i<data.length;i++){var v=(data[i]-128)/128;sum+=v*v}var rms=Math.sqrt(sum/data.length),db=rms?20*Math.log10(rms):-80;$('meterBar').style.width=Math.max(0,Math.min(100,(db+60)*1.67))+'%';meterRAF=requestAnimationFrame(tick)}tick();
}
async function startMonitor(){
  if(!mic||!AudioCtx)return;
  var tr=mic.getAudioTracks()[0],settings=tr.getSettings?tr.getSettings():{};
  var opts={latencyHint:0.02};if(settings.sampleRate)opts.sampleRate=settings.sampleRate;
  try{monitorCtx=new AudioCtx(opts)}catch(_){monitorCtx=new AudioCtx({latencyHint:'balanced'})}
  if(monitorCtx.state==='suspended')await monitorCtx.resume();
  monitorSource=monitorCtx.createMediaStreamSource(mic);monitorLimiter=monitorCtx.createDynamicsCompressor();monitorLimiter.threshold.value=-3;monitorLimiter.knee.value=0;monitorLimiter.ratio.value=20;monitorLimiter.attack.value=.001;monitorLimiter.release.value=.06;
  monitorGain=monitorCtx.createGain();monitorGain.gain.value=Number($('gain').value)/100;monitorSource.connect(monitorLimiter);monitorLimiter.connect(monitorGain);monitorGain.connect(monitorCtx.destination);monitoring=true;$('monitor').textContent='关闭返听';
  clientLog('K歌返听开启',{sampleRate:monitorCtx.sampleRate,baseLatency:monitorCtx.baseLatency,outputLatency:monitorCtx.outputLatency,gain:monitorGain.gain.value});
}
async function stopMonitor(){
  monitoring=false;try{monitorSource&&monitorSource.disconnect()}catch(_){}try{monitorLimiter&&monitorLimiter.disconnect()}catch(_){}try{monitorGain&&monitorGain.disconnect()}catch(_){}
  if(monitorCtx&&monitorCtx.state!=='closed')try{await monitorCtx.close()}catch(_){}
  monitorCtx=monitorSource=monitorLimiter=monitorGain=null;$('monitor').textContent='打开返听';
}
async function startSession(mime){
  var r=await fetch('/singeros/api/karaoke/recordings/start',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({song_id:song.id,song_title:song.title,mode:mode,mime:mime})});if(!r.ok)throw new Error(await r.text());return r.json();
}
async function upload(blob,n){
  var r=await fetch('/singeros/api/karaoke/recordings/'+recSession.id+'/chunk?seq='+n,{method:'POST',headers:{'content-type':'application/octet-stream'},body:blob});if(!r.ok)throw new Error(await r.text());
}
async function startKaraoke(){
  if(recording||!song)return;
  try{
    if(!window.isSecureContext||!navigator.mediaDevices||!navigator.mediaDevices.getUserMedia)throw new Error('getUserMedia unavailable');
    var dev=$('device').value;
    mic=await navigator.mediaDevices.getUserMedia({audio:Object.assign(dev?{deviceId:{exact:dev}}:{},{sampleRate:{ideal:48000},sampleSize:{ideal:16},channelCount:{ideal:2},latency:{ideal:.002},echoCancellation:false,noiseSuppression:false,autoGainControl:false})});
    await listDevices();await setupMeter();
    var pref=['audio/webm;codecs=opus','audio/webm','audio/ogg;codecs=opus','audio/mp4'];var mime=pref.find(function(x){return window.MediaRecorder&&MediaRecorder.isTypeSupported(x)})||'';
    recSession=await startSession(mime);seq=0;queue=Promise.resolve();
    recorder=new MediaRecorder(mic,mime?{mimeType:mime,audioBitsPerSecond:256000}:{audioBitsPerSecond:256000});
    recorder.ondataavailable=function(e){if(!e.data||!e.data.size)return;var n=seq++;queue=queue.then(function(){return upload(e.data,n)}).catch(function(err){clientLog('K歌分片上传失败',{message:String(err),seq:n})})};
    recorder.onerror=function(e){clientLog('K歌MediaRecorder错误',{message:e.error&&e.error.message})};
    recorder.start(5000);recording=true;startedPerf=performance.now();$('start').disabled=true;$('stop').disabled=false;$('monitor').disabled=false;$('original').disabled=true;$('accompaniment').disabled=true;$('captureStatus').innerHTML='<span class="ok">录制中 · '+esc(mode==='original'?'原唱':'伴奏')+'</span>';
    $('track').currentTime=0;await $('track').play();clientLog('K歌开始',{song:song.title,mode:mode,mime:mime});
  }catch(e){$('captureStatus').innerHTML='<span class="bad">'+esc((e.name||'Error')+': '+(e.message||e))+'</span>';clientLog('K歌启动失败',{name:e.name,message:e.message,stack:e.stack})}
}
async function stopKaraoke(){
  if(!recording)return;
  recording=false;var played=$('track').currentTime||0;$('track').pause();
  try{
    if(monitoring)await stopMonitor();
    if(recorder&&recorder.state!=='inactive')await new Promise(function(resolve){recorder.addEventListener('stop',resolve,{once:true});recorder.stop()});
    await queue;
    var elapsed=(performance.now()-startedPerf)/1000,duration=Math.max(played,elapsed);
    var r=await fetch('/singeros/api/karaoke/recordings/'+recSession.id+'/finalize',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({duration_seconds:duration,track_seconds:played})});if(!r.ok)throw new Error(await r.text());
    var saved=await r.json();$('captureStatus').innerHTML='<span class="ok">已保存：'+esc(saved.file)+' · '+fmt(saved.duration_seconds)+' · '+esc(saved.created_at_shanghai)+'</span>';clientLog('K歌保存完成',saved);await loadRecordings();
  }catch(e){$('captureStatus').innerHTML='<span class="bad">保存失败：'+esc(e.message||e)+'</span>';clientLog('K歌保存失败',{message:String(e)})}
  if(mic)mic.getTracks().forEach(function(t){t.stop()});mic=null;recorder=null;recSession=null;
  if(meterRAF)cancelAnimationFrame(meterRAF);try{meterSource&&meterSource.disconnect()}catch(_){}if(meterCtx&&meterCtx.state!=='closed')try{await meterCtx.close()}catch(_){}
  meterCtx=meterSource=analyser=null;$('meterBar').style.width='0';$('start').disabled=false;$('stop').disabled=true;$('monitor').disabled=true;$('original').disabled=false;$('accompaniment').disabled=false;
}
async function loadRecordings(){
  var r=await fetch('/singeros/api/karaoke/recordings',{cache:'no-store'}),xs=await r.json();
  $('recordings').innerHTML=xs.map(function(x){return '<div class="rec"><div class="wide"><strong>'+esc(x.song_title)+'</strong><br><small>'+esc(x.file)+'</small></div><span>'+esc(x.mode==='original'?'原唱':'伴奏')+'</span><span>'+fmt(x.duration_seconds)+'</span><span>'+esc(x.created_at_shanghai)+'</span><button class="btn" data-play="'+esc(x.url)+'">播放</button></div>'}).join('')||'<div class="muted">暂无K歌录音</div>';
  Array.prototype.forEach.call(document.querySelectorAll('[data-play]'),function(b){b.onclick=function(){$('recordPlayer').src=b.dataset.play;$('recordPlayer').play()}});
}
$('original').onclick=function(){if(!recording&&song&&song.tracks.original){mode='original';applyTrack()}};
$('accompaniment').onclick=function(){if(!recording&&song&&song.tracks.accompaniment){mode='accompaniment';applyTrack()}};
$('track').ontimeupdate=updateLyrics;$('track').onseeked=updateLyrics;$('track').onended=function(){if(recording)stopKaraoke()};
$('start').onclick=startKaraoke;$('stop').onclick=stopKaraoke;$('play').onclick=function(){if(song)$('track').play().catch(function(e){clientLog('K歌播放失败',{message:e.message})})};
$('devices').onclick=listDevices;$('refreshRec').onclick=loadRecordings;
$('lyricsFull').onclick=openLyricsFullscreen;$('lyricsBack').onclick=closeLyricsFullscreen;
window.addEventListener('keydown',function(e){if(lyricsFullscreen&&(e.key==='Escape'||e.key==='Backspace')){e.preventDefault();closeLyricsFullscreen()}});
$('monitor').onclick=function(){if(!mic)return;if(monitoring)stopMonitor();else startMonitor().catch(function(e){clientLog('K歌返听失败',{message:e.message})})};
$('gain').oninput=function(){$('gainText').textContent=$('gain').value+'%';if(monitorGain)monitorGain.gain.value=Number($('gain').value)/100};
window.addEventListener('error',function(e){clientLog('K歌window.error',{message:e.message,filename:e.filename,line:e.lineno,column:e.colno})});
window.addEventListener('unhandledrejection',function(e){clientLog('K歌unhandledrejection',{reason:String(e.reason)})});
loadCatalog().catch(function(e){$('resourceStatus').innerHTML='<span class="bad">'+esc(e.message)+'</span>'});listDevices();loadRecordings();
})();
</script>
</body>
</html>`
