package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	maxChunkBytes = int64(16 << 20)
	appName       = "singerOS"
)

type Session struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"started_at"`
	Seq       int       `json:"seq"`
	MIME      string    `json:"mime"`
	Bytes     int64     `json:"bytes"`
}

type Store struct {
	mu       sync.Mutex
	dir      string
	sessions map[string]*Session
}

type Recording struct {
	ID       string         `json:"id"`
	File     string         `json:"file"`
	Bytes    int64          `json:"bytes"`
	Modified time.Time      `json:"modified"`
	Meta     map[string]any `json:"meta,omitempty"`
}

var safeID = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,96}$`)

func newID() string {
	return fmt.Sprintf("%x-%x", time.Now().UnixNano(), os.Getpid())
}

func (s *Store) start(mimeType string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := newID()
	session := &Session{
		ID:        id,
		StartedAt: time.Now().UTC(),
		MIME:      mimeType,
	}
	part := filepath.Join(s.dir, id+".part")
	f, err := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	s.sessions[id] = session
	return session, nil
}

func (s *Store) append(id string, seq int, r io.Reader) (int64, error) {
	if !safeID.MatchString(id) {
		return 0, errors.New("invalid recording id")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		return 0, errors.New("recording session not active")
	}
	if seq != session.Seq {
		return 0, fmt.Errorf("sequence mismatch: expected %d", session.Seq)
	}

	f, err := os.OpenFile(filepath.Join(s.dir, id+".part"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	n, err := io.Copy(f, io.LimitReader(r, maxChunkBytes+1))
	if err != nil {
		return n, err
	}
	if n > maxChunkBytes {
		return n, errors.New("chunk exceeds 16 MiB")
	}
	session.Seq++
	session.Bytes += n
	return n, nil
}

func extForMIME(m string) string {
	base := strings.Split(m, ";")[0]
	if exts, _ := mime.ExtensionsByType(base); len(exts) > 0 {
		return exts[0]
	}
	switch base {
	case "audio/webm":
		return ".webm"
	case "audio/ogg":
		return ".ogg"
	case "audio/mp4":
		return ".m4a"
	default:
		return ".bin"
	}
}

func (s *Store) finalize(id string, clientMeta map[string]any) (Recording, error) {
	if !safeID.MatchString(id) {
		return Recording{}, errors.New("invalid recording id")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		return Recording{}, errors.New("recording session not active")
	}

	ext := extForMIME(session.MIME)
	finalName := id + ext
	finalPath := filepath.Join(s.dir, finalName)
	if err := os.Rename(filepath.Join(s.dir, id+".part"), finalPath); err != nil {
		return Recording{}, err
	}

	meta := map[string]any{
		"id":         id,
		"started_at": session.StartedAt,
		"ended_at":   time.Now().UTC(),
		"mime":       session.MIME,
		"bytes":      session.Bytes,
		"chunks":     session.Seq,
		"file":       finalName,
		"client":     clientMeta,
	}
	metaBytes, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(filepath.Join(s.dir, id+".json"), metaBytes, 0o600)

	rec := Recording{
		ID:       id,
		File:     finalName,
		Bytes:    session.Bytes,
		Modified: time.Now().UTC(),
		Meta:     meta,
	}
	delete(s.sessions, id)
	return rec, nil
}

func (s *Store) list() []Recording {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	out := make([]Recording, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".part") || strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		rec := Recording{ID: id, File: e.Name(), Bytes: info.Size(), Modified: info.ModTime()}
		if b, err := os.ReadFile(filepath.Join(s.dir, id+".json")); err == nil {
			var meta map[string]any
			if json.Unmarshal(b, &meta) == nil {
				rec.Meta = meta
			}
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out
}

func (s *Store) audioPath(id string) (string, bool) {
	if !safeID.MatchString(id) {
		return "", false
	}
	matches, _ := filepath.Glob(filepath.Join(s.dir, id+".*"))
	for _, p := range matches {
		if strings.HasSuffix(p, ".json") || strings.HasSuffix(p, ".part") {
			continue
		}
		return p, true
	}
	return "", false
}

func (s *Store) delete(id string) {
	if !safeID.MatchString(id) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
	matches, _ := filepath.Glob(filepath.Join(s.dir, id+".*"))
	for _, p := range matches {
		_ = os.Remove(p)
	}
}

func main() {
	listen := flag.String("listen", ":8080", "listen address")
	dataDir := flag.String("data", "./recordings", "recording storage directory")
	tlsCert := flag.String("tls-cert", "", "TLS certificate PEM")
	tlsKey := flag.String("tls-key", "", "TLS private key PEM")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		log.Fatal(err)
	}
	store := &Store{dir: *dataDir, sessions: map[string]*Session{}}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	secureHeaders := func(c *gin.Context) {
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		c.Header("Pragma", "no-cache")
		c.Header("Permissions-Policy", "microphone=(self), camera=()")
		c.Header("Feature-Policy", "microphone 'self'; camera 'none'")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Cross-Origin-Opener-Policy", "same-origin")
	}

	r.GET("/", func(c *gin.Context) {
		secureHeaders(c)
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(page))
	})
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "app": appName})
	})

	api := r.Group("/api")
	api.POST("/recordings/start", func(c *gin.Context) {
		var req struct {
			MIME string `json:"mime"`
		}
		_ = c.ShouldBindJSON(&req)
		session, err := store.start(req.MIME)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, session)
	})
	api.POST("/recordings/:id/chunk", func(c *gin.Context) {
		seq, err := strconv.Atoi(c.Query("seq"))
		if err != nil || seq < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid seq"})
			return
		}
		n, err := store.append(c.Param("id"), seq, c.Request.Body)
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"seq": seq, "bytes": n})
	})
	api.POST("/recordings/:id/finalize", func(c *gin.Context) {
		var meta map[string]any
		_ = c.ShouldBindJSON(&meta)
		rec, err := store.finalize(c.Param("id"), meta)
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"recording": rec,
			"url":       "/api/recordings/" + rec.ID + "/audio",
		})
	})
	api.GET("/recordings", func(c *gin.Context) {
		c.JSON(http.StatusOK, store.list())
	})
	api.GET("/recordings/:id/audio", func(c *gin.Context) {
		path, ok := store.audioPath(c.Param("id"))
		if !ok {
			c.Status(http.StatusNotFound)
			return
		}
		c.File(path)
	})
	api.DELETE("/recordings/:id", func(c *gin.Context) {
		store.delete(c.Param("id"))
		c.Status(http.StatusNoContent)
	})

	if *tlsCert != "" || *tlsKey != "" {
		if *tlsCert == "" || *tlsKey == "" {
			log.Fatal("both -tls-cert and -tls-key are required")
		}
		log.Printf("%s HTTPS listening on %s", appName, *listen)
		log.Fatal(r.RunTLS(*listen, *tlsCert, *tlsKey))
	}

	log.Printf("%s HTTP listening on %s; Tesla microphone capture requires HTTPS", appName, *listen)
	log.Fatal(r.Run(*listen))
}

const page = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1,user-scalable=no">
<title>singerOS · Tesla Karaoke</title>
</head>
<body>
<div id="app"></div>
<script>
(() => {
'use strict';

const style = document.createElement('style');
style.textContent = `
*{box-sizing:border-box}body{margin:0;background:#090b10;color:#eef2f8;font-family:system-ui,-apple-system,"Segoe UI",sans-serif}
.wrap{max-width:1180px;margin:auto;padding:20px}.head{display:flex;justify-content:space-between;align-items:center;gap:12px}
.badge{padding:8px 12px;border:1px solid #566071;border-radius:999px}.ok{color:#67e8a0}.bad{color:#ff8e94}
.grid{display:grid;grid-template-columns:1fr 1fr;gap:14px}.card{background:#11151d;border:1px solid #293140;border-radius:14px;padding:16px;margin:14px 0}
button,input{font:inherit}button{min-height:48px;margin:4px;padding:9px 14px;border-radius:10px;border:1px solid #435066;background:#202733;color:white}
button:disabled{opacity:.42}.primary{background:#2869ff}.meter{height:22px;background:#05070a;border:1px solid #394352;border-radius:99px;overflow:hidden}
.bar{height:100%;width:0;background:linear-gradient(90deg,#35d27d,#ffd052,#ff5964)}pre{white-space:pre-wrap;overflow:auto;max-height:340px;background:#06080c;padding:12px;border-radius:9px;font-size:12px}
.caps{display:grid;grid-template-columns:170px 1fr;gap:7px}.row{display:flex;flex-wrap:wrap;align-items:center;gap:8px}
audio{width:100%;margin-top:10px}.list>div{padding:8px 0;border-bottom:1px solid #293140}.hint{color:#9da8b8;font-size:.9rem}
@media(max-width:800px){.grid{grid-template-columns:1fr}.caps{grid-template-columns:1fr}.head{align-items:flex-start;flex-direction:column}}
`;
document.head.appendChild(style);

document.getElementById('app').innerHTML = `
<main class="wrap">
  <div class="head">
    <div><h1>singerOS · Tesla K歌</h1><div>车机麦克风能力探测 · 实时返听 · 5秒分片云端录制</div></div>
    <div id="state" class="badge">未采集</div>
  </div>

  <section class="card">
    <div class="row">
      <button id="start" class="primary">请求麦克风并开始录制</button>
      <button id="stop" disabled>停止并保存</button>
      <button id="speaker">测试车载音响</button>
      <button id="monitor" disabled>打开返听</button>
      <label>返听 <input id="gain" type="range" min="0" max="120" value="50"></label>
    </div>
    <p>
      <label><input id="aec" type="checkbox" checked> 回声消除</label>
      <label><input id="ns" type="checkbox"> 降噪</label>
      <label><input id="agc" type="checkbox" checked> 自动增益</label>
    </p>
    <div class="meter"><div id="bar" class="bar"></div></div>
    <div id="db">-∞ dB</div>
    <p class="hint">麦克风采集要求 HTTPS。返听默认关闭，避免车内啸叫；请在驻车状态测试。</p>
  </section>

  <section class="grid">
    <div class="card"><h2>环境 / 权限 / 硬件</h2><div id="caps" class="caps"></div></div>
    <div class="card"><h2>服务器录音</h2><button id="refresh">刷新</button><div id="recs" class="list"></div><audio id="player" controls></audio></div>
  </section>

  <section class="card"><h2>诊断日志</h2><pre id="log"></pre></section>
</main>`;

const $ = id => document.getElementById(id);
const AudioCtx = window.AudioContext || window.webkitAudioContext;
let stream, ctx, source, analyser, monitorGain, meterFrame, recorder, session;
let seq = 0;
let uploadQueue = Promise.resolve();
let permissionState = 'unknown';
let devices = [];
let trackSettings = {};
let trackCapabilities = {};
let trackConstraints = {};
let monitoring = false;
const logs = [];

function logLine(message, details) {
  const line = '[' + new Date().toLocaleTimeString('zh-CN', {hour12:false}) + '] ' + message +
    (details !== undefined ? ' | ' + (typeof details === 'string' ? details : JSON.stringify(details)) : '');
  logs.push(line);
  if (logs.length > 180) logs.shift();
  $('log').textContent = logs.join('\n');
  $('log').scrollTop = $('log').scrollHeight;
}

function explainError(error) {
  const map = {
    NotAllowedError: '浏览器或车机系统策略拒绝麦克风',
    NotFoundError: '车机没有向网页暴露任何音频输入设备',
    NotReadableError: '麦克风被系统/语音助手占用，或底层不可读',
    AbortError: '底层音频设备启动失败',
    OverconstrainedError: '车机音频设备不支持请求的采集参数',
    SecurityError: '浏览器安全策略禁止音频采集',
    TypeError: '页面不是安全上下文，或浏览器缺少所需接口',
    TimeoutError: '15秒内没有权限/设备结果，可能没有实现授权界面'
  };
  return (error?.name || 'Error') + ': ' + (map[error?.name] || error?.message || String(error));
}

function renderCapabilities() {
  const rows = [
    ['安全上下文', window.isSecureContext],
    ['协议', location.protocol],
    ['getUserMedia', !!navigator.mediaDevices?.getUserMedia],
    ['enumerateDevices', !!navigator.mediaDevices?.enumerateDevices],
    ['Permissions API', !!navigator.permissions?.query],
    ['麦克风权限', permissionState],
    ['audioinput 数量', devices.length],
    ['设备', devices.map(d => d.label || '(授权前名称隐藏)').join('；') || '无'],
    ['Web Audio', !!AudioCtx],
    ['MediaRecorder', !!window.MediaRecorder],
    ['Track settings', JSON.stringify(trackSettings)],
    ['Track capabilities', JSON.stringify(trackCapabilities)],
    ['Track constraints', JSON.stringify(trackConstraints)],
    ['AudioContext', ctx ? (ctx.state + ', ' + ctx.sampleRate + 'Hz, base ' + Math.round((ctx.baseLatency||0)*1000) + 'ms, out ' + Math.round((ctx.outputLatency||0)*1000) + 'ms') : '未创建'],
    ['上传会话', session?.id || '无'],
    ['User-Agent', navigator.userAgent]
  ];
  $('caps').innerHTML = rows.map(([k,v]) => '<div>' + k + '</div><div>' + String(v) + '</div>').join('');
}

async function probe() {
  try {
    if (navigator.permissions?.query) {
      const p = await navigator.permissions.query({name:'microphone'});
      permissionState = p.state;
      p.onchange = () => { permissionState = p.state; logLine('麦克风权限变化', p.state); renderCapabilities(); };
    }
  } catch (e) {
    permissionState = '无法查询';
    logLine('权限查询失败', explainError(e));
  }

  try {
    devices = (await navigator.mediaDevices?.enumerateDevices?.() || []).filter(x => x.kind === 'audioinput');
    logLine('音频输入枚举完成', devices.map(x => x.label || '(未授权隐藏名称)'));
  } catch (e) {
    devices = [];
    logLine('设备枚举失败', explainError(e));
  }
  renderCapabilities();
}

async function ensureAudioContext() {
  if (!AudioCtx) throw new Error('Web Audio API unavailable');
  if (!ctx) ctx = new AudioCtx({latencyHint:'interactive'});
  if (ctx.state === 'suspended') await ctx.resume();
  renderCapabilities();
  return ctx;
}

function startMeter() {
  if (!analyser) return;
  const values = new Uint8Array(analyser.fftSize);
  const tick = () => {
    analyser.getByteTimeDomainData(values);
    let sum = 0;
    for (const x of values) {
      const v = (x - 128) / 128;
      sum += v * v;
    }
    const rms = Math.sqrt(sum / values.length);
    const db = rms ? 20 * Math.log10(rms) : -Infinity;
    $('db').textContent = (Number.isFinite(db) ? db.toFixed(1) : '-∞') + ' dB';
    $('bar').style.width = Math.max(0, Math.min(100, (db + 60) * 1.67)) + '%';
    meterFrame = requestAnimationFrame(tick);
  };
  tick();
}

async function startServerSession(mime) {
  const r = await fetch('/api/recordings/start', {
    method:'POST',
    headers:{'content-type':'application/json'},
    body:JSON.stringify({mime})
  });
  if (!r.ok) throw new Error(await r.text());
  return r.json();
}

async function uploadChunk(blob, number) {
  const r = await fetch('/api/recordings/' + session.id + '/chunk?seq=' + number, {
    method:'POST',
    headers:{'content-type':'application/octet-stream'},
    body:blob
  });
  if (!r.ok) throw new Error(await r.text());
  logLine('录音分片上传完成', {seq:number, bytes:blob.size});
}

async function startCapture() {
  try {
    if (!window.isSecureContext) throw new Error('当前不是 HTTPS 安全上下文，Tesla 浏览器不会开放麦克风');
    if (!navigator.mediaDevices?.getUserMedia) throw new Error('getUserMedia unavailable');

    const constraints = {
      audio:{
        echoCancellation:$('aec').checked,
        noiseSuppression:$('ns').checked,
        autoGainControl:$('agc').checked
      }
    };

    const timeout = new Promise((_, reject) => setTimeout(() => {
      const e = new Error('getUserMedia did not settle within 15 seconds');
      e.name = 'TimeoutError';
      reject(e);
    }, 15000));

    stream = await Promise.race([navigator.mediaDevices.getUserMedia(constraints), timeout]);
    const track = stream.getAudioTracks()[0];
    trackSettings = track.getSettings?.() || {};
    trackCapabilities = track.getCapabilities?.() || {};
    trackConstraints = track.getConstraints?.() || {};
    logLine('麦克风采集成功', {label:track.label, settings:trackSettings});

    track.onmute = () => logLine('音频输入轨道被系统静音');
    track.onunmute = () => logLine('音频输入轨道恢复');
    track.onended = () => logLine('音频输入轨道被系统终止');

    await ensureAudioContext();
    source = ctx.createMediaStreamSource(stream);
    analyser = ctx.createAnalyser();
    analyser.fftSize = 2048;
    analyser.smoothingTimeConstant = 0.72;
    monitorGain = ctx.createGain();
    monitorGain.gain.value = 0;
    source.connect(analyser);
    source.connect(monitorGain);
    monitorGain.connect(ctx.destination);
    startMeter();

    const preferred = ['audio/webm;codecs=opus','audio/webm','audio/ogg;codecs=opus','audio/mp4'];
    const selectedMIME = preferred.find(x => MediaRecorder.isTypeSupported?.(x)) || '';
    session = await startServerSession(selectedMIME);
    seq = 0;
    uploadQueue = Promise.resolve();

    recorder = new MediaRecorder(stream, selectedMIME ? {mimeType:selectedMIME} : undefined);
    recorder.ondataavailable = event => {
      if (!event.data?.size) return;
      const number = seq++;
      uploadQueue = uploadQueue.then(() => uploadChunk(event.data, number));
      uploadQueue.catch(e => logLine('分片上传失败', String(e)));
    };
    recorder.onerror = event => logLine('MediaRecorder 错误', event.error?.message || 'unknown');
    recorder.start(5000);

    $('state').textContent = '录制中';
    $('state').className = 'badge ok';
    $('start').disabled = true;
    $('stop').disabled = false;
    $('monitor').disabled = false;

    await probe();
    renderCapabilities();
  } catch (e) {
    logLine('启动失败', explainError(e));
    $('state').textContent = '启动失败';
    $('state').className = 'badge bad';
  }
}

async function stopCapture() {
  try {
    if (recorder && recorder.state !== 'inactive') {
      await new Promise(resolve => {
        recorder.addEventListener('stop', resolve, {once:true});
        recorder.stop();
      });
    }
    await uploadQueue;

    if (session) {
      const meta = {
        userAgent:navigator.userAgent,
        permission:permissionState,
        devices:devices.map(d => ({label:d.label, deviceId:d.deviceId, groupId:d.groupId})),
        settings:trackSettings,
        capabilities:trackCapabilities,
        constraints:trackConstraints,
        audioContext:ctx ? {
          state:ctx.state,
          sampleRate:ctx.sampleRate,
          baseLatency:ctx.baseLatency,
          outputLatency:ctx.outputLatency
        } : null
      };
      const r = await fetch('/api/recordings/' + session.id + '/finalize', {
        method:'POST',
        headers:{'content-type':'application/json'},
        body:JSON.stringify(meta)
      });
      if (!r.ok) throw new Error(await r.text());
      logLine('录音已持久化到服务器', await r.json());
    }

    if (stream) stream.getTracks().forEach(t => t.stop());
    if (meterFrame) cancelAnimationFrame(meterFrame);
    for (const node of [source, analyser, monitorGain]) {
      try { node?.disconnect(); } catch {}
    }
    stream = source = analyser = monitorGain = recorder = null;
    session = null;
    monitoring = false;
    $('monitor').textContent = '打开返听';
    $('monitor').disabled = true;
    $('start').disabled = false;
    $('stop').disabled = true;
    $('state').textContent = '已保存';
    $('state').className = 'badge ok';
    renderCapabilities();
    await listRecordings();
  } catch (e) {
    logLine('结束或保存失败', String(e));
    $('state').textContent = '保存失败';
    $('state').className = 'badge bad';
  }
}

async function listRecordings() {
  const r = await fetch('/api/recordings');
  const items = await r.json();
  $('recs').innerHTML = items.length ? items.map(x =>
    '<div><button data-play="' + x.id + '">播放</button><button data-delete="' + x.id + '">删除</button> ' +
    x.id + ' · ' + (x.bytes/1024/1024).toFixed(2) + ' MiB</div>'
  ).join('') : '暂无录音';

  document.querySelectorAll('[data-play]').forEach(b => b.onclick = () => {
    $('player').src = '/api/recordings/' + b.dataset.play + '/audio';
    $('player').play().catch(e => logLine('回放失败', String(e)));
  });
  document.querySelectorAll('[data-delete]').forEach(b => b.onclick = async () => {
    await fetch('/api/recordings/' + b.dataset.delete, {method:'DELETE'});
    listRecordings();
  });
}

$('start').onclick = startCapture;
$('stop').onclick = stopCapture;
$('refresh').onclick = listRecordings;
$('gain').oninput = () => {
  if (monitorGain) monitorGain.gain.value = monitoring ? Number($('gain').value)/100 : 0;
};
$('monitor').onclick = async () => {
  await ensureAudioContext();
  monitoring = !monitoring;
  monitorGain.gain.value = monitoring ? Number($('gain').value)/100 : 0;
  $('monitor').textContent = monitoring ? '关闭返听' : '打开返听';
  logLine(monitoring ? '实时返听开启' : '实时返听关闭');
};
$('speaker').onclick = async () => {
  try {
    const c = await ensureAudioContext();
    const oscillator = c.createOscillator();
    const gain = c.createGain();
    const now = c.currentTime;
    oscillator.frequency.value = 523.25;
    gain.gain.setValueAtTime(0.0001, now);
    gain.gain.exponentialRampToValueAtTime(0.05, now + 0.03);
    gain.gain.exponentialRampToValueAtTime(0.0001, now + 0.35);
    oscillator.connect(gain);
    gain.connect(c.destination);
    oscillator.start(now);
    oscillator.stop(now + 0.36);
    logLine('测试音已发送到默认音频输出', {sampleRate:c.sampleRate});
  } catch (e) {
    logLine('音响测试失败', explainError(e));
  }
};

document.addEventListener('visibilitychange', () => {
  logLine('页面可见性变化', document.visibilityState);
  if (document.visibilityState === 'visible' && ctx?.state === 'suspended') {
    ctx.resume().catch(e => logLine('恢复 AudioContext 失败', String(e)));
  }
});

logLine('能力探测页面加载', {
  secureContext:window.isSecureContext,
  protocol:location.protocol,
  mediaDevices:!!navigator.mediaDevices,
  mediaRecorder:!!window.MediaRecorder,
  audioContext:!!AudioCtx
});
probe();
listRecordings();
renderCapabilities();
})();
</script>
</body>
</html>`
