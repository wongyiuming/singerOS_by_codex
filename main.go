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

	r.GET("/singeros/", func(c *gin.Context) {
		secureHeaders(c)
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(page))
	})
	r.GET("/singeros/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "app": appName})
	})

	api := r.Group("/singeros/api")
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
			"url":       "/singeros/api/recordings/" + rec.ID + "/audio",
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

const page = "<!doctype html>\n<html lang=\"zh-CN\">\n<head>\n<meta charset=\"utf-8\">\n<meta name=\"viewport\" content=\"width=device-width,initial-scale=1,maximum-scale=1,user-scalable=no\">\n<title>singerOS · Tesla Karaoke</title>\n</head>\n<body>\n<div id=\"app\"></div>\n<script>\n(() => {\n'use strict';\n\nconst style = document.createElement('style');\nstyle.textContent = `\n*{box-sizing:border-box}body{margin:0;background:#090b10;color:#eef2f8;font-family:system-ui,-apple-system,\"Segoe UI\",sans-serif}\n.wrap{max-width:1180px;margin:auto;padding:20px}.head{display:flex;justify-content:space-between;align-items:center;gap:12px}\n.badge{padding:8px 12px;border:1px solid #566071;border-radius:999px}.ok{color:#67e8a0}.bad{color:#ff8e94}\n.grid{display:grid;grid-template-columns:1fr 1fr;gap:14px}.card{background:#11151d;border:1px solid #293140;border-radius:14px;padding:16px;margin:14px 0}\nbutton,input{font:inherit}button{min-height:48px;margin:4px;padding:9px 14px;border-radius:10px;border:1px solid #435066;background:#202733;color:white}\nbutton:disabled{opacity:.42}.primary{background:#2869ff}.meter{height:22px;background:#05070a;border:1px solid #394352;border-radius:99px;overflow:hidden}\n.bar{height:100%;width:0;background:linear-gradient(90deg,#35d27d,#ffd052,#ff5964)}pre{white-space:pre-wrap;overflow:auto;max-height:340px;background:#06080c;padding:12px;border-radius:9px;font-size:12px}\n.caps{display:grid;grid-template-columns:170px 1fr;gap:7px}.row{display:flex;flex-wrap:wrap;align-items:center;gap:8px}\naudio{width:100%;margin-top:10px}.list>div{padding:8px 0;border-bottom:1px solid #293140}.hint{color:#9da8b8;font-size:.9rem}\n@media(max-width:800px){.grid{grid-template-columns:1fr}.caps{grid-template-columns:1fr}.head{align-items:flex-start;flex-direction:column}}\n`;\ndocument.head.appendChild(style);\n\ndocument.getElementById('app').innerHTML = `\n<main class=\"wrap\">\n  <div class=\"head\">\n    <div><h1>singerOS · Tesla K歌</h1><div>车机麦克风能力探测 · 实时返听 · 5秒分片云端录制</div></div>\n    <div id=\"state\" class=\"badge\">未采集</div>\n  </div>\n\n  <section class=\"card\">\n    <div class=\"row\">\n      <button id=\"start\" class=\"primary\">请求麦克风并开始录制</button>\n      <button id=\"stop\" disabled>停止并保存</button>\n      <button id=\"speaker\">测试车载音响</button>\n      <button id=\"monitor\" disabled>打开返听</button>\n      <label>返听 <input id=\"gain\" type=\"range\" min=\"0\" max=\"35\" value=\"12\"></label>\n    </div>\n    <p class=\"row\"><label>输入设备 <select id=\"device\"><option value=\"\">自动选择</option></select></label><button id=\"probe\">刷新设备</button></p>\n    <p>\n      <strong>演唱高保真：</strong> 48 kHz · 优先双声道 · Opus 256 kbps\n      <label><input id=\"aec\" type=\"checkbox\"> 回声消除</label>\n      <label><input id=\"ns\" type=\"checkbox\"> 降噪</label>\n      <label><input id=\"agc\" type=\"checkbox\"> 自动增益</label>\n    </p>\n    <div class=\"meter\"><div id=\"bar\" class=\"bar\"></div></div>\n    <div id=\"db\">-∞ dB</div>\n    <p class=\"hint\">麦克风采集要求 HTTPS。返听默认关闭，避免车内啸叫；请在驻车状态测试。</p>\n  </section>\n\n  <section class=\"grid\">\n    <div class=\"card\"><h2>环境 / 权限 / 硬件</h2><div id=\"caps\" class=\"caps\"></div></div>\n    <div class=\"card\"><h2>服务器录音</h2><button id=\"refresh\">刷新</button><div id=\"recs\" class=\"list\"></div><audio id=\"player\" controls></audio></div>\n  </section>\n\n  <section class=\"card\"><h2>诊断日志</h2><pre id=\"log\"></pre></section>\n</main>`;\n\nconst $ = id => document.getElementById(id);\nconst AudioCtx = window.AudioContext || window.webkitAudioContext;\nlet stream, ctx, source, analyser, monitorGain, monitorStream, monitorSource, monitorHighpass, monitorCompressor, meterFrame, recorder, session;\nlet seq = 0;\nlet uploadQueue = Promise.resolve();\nlet permissionState = 'unknown';\nlet devices = [];\nlet trackSettings = {};\nlet trackCapabilities = {};\nlet trackConstraints = {};\nlet monitoring = false;\nlet recorderInfo = {};\nconst requestedAudioBitsPerSecond = 256000;\nconst logs = [];\n\nfunction logLine(message, details) {\n  const line = '[' + new Date().toLocaleTimeString('zh-CN', {hour12:false}) + '] ' + message +\n    (details !== undefined ? ' | ' + (typeof details === 'string' ? details : JSON.stringify(details)) : '');\n  logs.push(line);\n  if (logs.length > 180) logs.shift();\n  $('log').textContent = logs.join('\\n');\n  $('log').scrollTop = $('log').scrollHeight;\n}\n\nfunction explainError(error) {\n  const map = {\n    NotAllowedError: '浏览器或车机系统策略拒绝麦克风',\n    NotFoundError: '车机没有向网页暴露任何音频输入设备',\n    NotReadableError: '麦克风被系统/语音助手占用，或底层不可读',\n    AbortError: '底层音频设备启动失败',\n    OverconstrainedError: '车机音频设备不支持请求的采集参数',\n    SecurityError: '浏览器安全策略禁止音频采集',\n    TypeError: '页面不是安全上下文，或浏览器缺少所需接口',\n    TimeoutError: '15秒内没有权限/设备结果，可能没有实现授权界面'\n  };\n  return (error?.name || 'Error') + ': ' + (map[error?.name] || error?.message || String(error));\n}\n\nfunction renderCapabilities() {\n  const rows = [\n    ['安全上下文', window.isSecureContext],\n    ['协议', location.protocol],\n    ['getUserMedia', !!navigator.mediaDevices?.getUserMedia],\n    ['enumerateDevices', !!navigator.mediaDevices?.enumerateDevices],\n    ['Permissions API', !!navigator.permissions?.query],\n    ['麦克风权限', permissionState],\n    ['audioinput 数量', devices.length],\n    ['设备', devices.map(d => d.label || '(授权前名称隐藏)').join('；') || '无'],\n    ['Web Audio', !!AudioCtx],\n    ['MediaRecorder', !!window.MediaRecorder],\n    ['Recorder', JSON.stringify(recorderInfo)],\n    ['Track settings', JSON.stringify(trackSettings)],\n    ['Track capabilities', JSON.stringify(trackCapabilities)],\n    ['Track constraints', JSON.stringify(trackConstraints)],\n    ['AudioContext', ctx ? (ctx.state + ', ' + ctx.sampleRate + 'Hz, base ' + Math.round((ctx.baseLatency||0)*1000) + 'ms, out ' + Math.round((ctx.outputLatency||0)*1000) + 'ms') : '未创建'],\n    ['上传会话', session?.id || '无'],\n    ['User-Agent', navigator.userAgent]\n  ];\n  $('caps').innerHTML = rows.map(([k,v]) => '<div>' + k + '</div><div>' + String(v) + '</div>').join('');\n}\n\nasync function probe() {\n  try {\n    if (navigator.permissions?.query) {\n      const p = await navigator.permissions.query({name:'microphone'});\n      permissionState = p.state;\n      p.onchange = () => { permissionState = p.state; logLine('麦克风权限变化', p.state); renderCapabilities(); };\n    }\n  } catch (e) {\n    permissionState = '无法查询';\n    logLine('权限查询失败', explainError(e));\n  }\n\n  try {\n    devices = (await navigator.mediaDevices?.enumerateDevices?.() || []).filter(x => x.kind === 'audioinput');\n    logLine('音频输入枚举完成', devices.map(x => x.label || '(未授权隐藏名称)'));\n    const select = $('device');\n    const previous = select.value;\n    select.innerHTML = '<option value=\"\">自动选择</option>' + devices.map(d => '<option value=\"' + d.deviceId + '\">' + (d.label || '未命名麦克风') + '</option>').join('');\n    if (devices.some(d => d.deviceId === previous)) {\n      select.value = previous;\n    } else {\n      const physical = devices.find(d => /shure|mvx2u|usb/i.test(d.label) && !/virtual|communications|通讯/i.test(d.label));\n      const normal = devices.find(d => d.deviceId !== 'communications' && !/virtual|communications|通讯/i.test(d.label));\n      const preferred = physical || normal || devices[0];\n      if (preferred) select.value = preferred.deviceId;\n    }\n  } catch (e) {\n    devices = [];\n    logLine('设备枚举失败', explainError(e));\n  }\n  renderCapabilities();\n}\n\nasync function ensureAudioContext() {\n  if (!AudioCtx) throw new Error('Web Audio API unavailable');\n  if (!ctx) ctx = new AudioCtx({latencyHint:'interactive'});\n  if (ctx.state === 'suspended') await ctx.resume();\n  renderCapabilities();\n  return ctx;\n}\n\nfunction startMeter() {\n  if (!analyser) return;\n  const values = new Uint8Array(analyser.fftSize);\n  const tick = () => {\n    analyser.getByteTimeDomainData(values);\n    let sum = 0;\n    for (const x of values) {\n      const v = (x - 128) / 128;\n      sum += v * v;\n    }\n    const rms = Math.sqrt(sum / values.length);\n    const db = rms ? 20 * Math.log10(rms) : -Infinity;\n    $('db').textContent = (Number.isFinite(db) ? db.toFixed(1) : '-∞') + ' dB';\n    $('bar').style.width = Math.max(0, Math.min(100, (db + 60) * 1.67)) + '%';\n    meterFrame = requestAnimationFrame(tick);\n  };\n  tick();\n}\n\nasync function startServerSession(mime) {\n  const r = await fetch('/singeros/api/recordings/start', {\n    method:'POST',\n    headers:{'content-type':'application/json'},\n    body:JSON.stringify({mime})\n  });\n  if (!r.ok) throw new Error(await r.text());\n  return r.json();\n}\n\nasync function uploadChunk(blob, number) {\n  const r = await fetch('/singeros/api/recordings/' + session.id + '/chunk?seq=' + number, {\n    method:'POST',\n    headers:{'content-type':'application/octet-stream'},\n    body:blob\n  });\n  if (!r.ok) throw new Error(await r.text());\n  logLine('录音分片上传完成', {seq:number, bytes:blob.size});\n}\n\nasync function startCapture() {\n  try {\n    if (!window.isSecureContext) throw new Error('当前不是 HTTPS 安全上下文，Tesla 浏览器不会开放麦克风');\n    if (!navigator.mediaDevices?.getUserMedia) throw new Error('getUserMedia unavailable');\n\n    await probe();\n    const selectedDevice = $('device').value;\n    const constraints = {\n      audio:{\n        ...(selectedDevice ? {deviceId:{exact:selectedDevice}} : {}),\n        sampleRate:{ideal:48000},\n        sampleSize:{ideal:16},\n        channelCount:{ideal:2},\n        latency:{ideal:0.002},\n        echoCancellation:$('aec').checked,\n        noiseSuppression:$('ns').checked,\n        autoGainControl:$('agc').checked\n      }\n    };\n    logLine('请求高保真采集', {deviceId:selectedDevice || 'auto', requested:constraints.audio});\n\n    const timeout = new Promise((_, reject) => setTimeout(() => {\n      const e = new Error('getUserMedia did not settle within 15 seconds');\n      e.name = 'TimeoutError';\n      reject(e);\n    }, 15000));\n\n    stream = await Promise.race([navigator.mediaDevices.getUserMedia(constraints), timeout]);\n    const track = stream.getAudioTracks()[0];\n    trackSettings = track.getSettings?.() || {};\n    trackCapabilities = track.getCapabilities?.() || {};\n    trackConstraints = track.getConstraints?.() || {};\n    logLine('麦克风采集成功', {label:track.label, settings:trackSettings});\n\n    track.onmute = () => logLine('音频输入轨道被系统静音');\n    track.onunmute = () => logLine('音频输入轨道恢复');\n    track.onended = () => logLine('音频输入轨道被系统终止');\n\n    await ensureAudioContext();\n    source = ctx.createMediaStreamSource(stream);\n    analyser = ctx.createAnalyser();\n    analyser.fftSize = 2048;\n    analyser.smoothingTimeConstant = 0.72;\n    source.connect(analyser);\n    startMeter();\n\n    const preferred = ['audio/webm;codecs=opus','audio/webm','audio/ogg;codecs=opus','audio/mp4'];\n    const selectedMIME = preferred.find(x => MediaRecorder.isTypeSupported?.(x)) || '';\n    session = await startServerSession(selectedMIME);\n    seq = 0;\n    uploadQueue = Promise.resolve();\n\n    const recorderOptions = selectedMIME ? {mimeType:selectedMIME, audioBitsPerSecond:requestedAudioBitsPerSecond} : {audioBitsPerSecond:requestedAudioBitsPerSecond};\n    recorder = new MediaRecorder(stream, recorderOptions);\n    recorderInfo = {mimeType:recorder.mimeType, requestedAudioBitsPerSecond, actualAudioBitsPerSecond:recorder.audioBitsPerSecond || null, audioBitrateMode:recorder.audioBitrateMode || null};\n    logLine('MediaRecorder 高质量模式', recorderInfo);\n    recorder.ondataavailable = event => {\n      if (!event.data?.size) return;\n      const number = seq++;\n      uploadQueue = uploadQueue.then(() => uploadChunk(event.data, number));\n      uploadQueue.catch(e => logLine('分片上传失败', String(e)));\n    };\n    recorder.onerror = event => logLine('MediaRecorder 错误', event.error?.message || 'unknown');\n    recorder.start(5000);\n\n    $('state').textContent = '录制中';\n    $('state').className = 'badge ok';\n    $('start').disabled = true;\n    $('stop').disabled = false;\n    $('monitor').disabled = false;\n\n    await probe();\n    renderCapabilities();\n  } catch (e) {\n    logLine('启动失败', explainError(e));\n    $('state').textContent = '启动失败';\n    $('state').className = 'badge bad';\n  }\n}\n\nasync function stopCapture() {\n  try {\n    if (recorder && recorder.state !== 'inactive') {\n      await new Promise(resolve => {\n        recorder.addEventListener('stop', resolve, {once:true});\n        recorder.stop();\n      });\n    }\n    await uploadQueue;\n\n    if (session) {\n      const meta = {\n        userAgent:navigator.userAgent,\n        permission:permissionState,\n        devices:devices.map(d => ({label:d.label, deviceId:d.deviceId, groupId:d.groupId})),\n        settings:trackSettings,\n        capabilities:trackCapabilities,\n        constraints:trackConstraints,\n        recorder:recorderInfo,\n        audioContext:ctx ? {\n          state:ctx.state,\n          sampleRate:ctx.sampleRate,\n          baseLatency:ctx.baseLatency,\n          outputLatency:ctx.outputLatency\n        } : null\n      };\n      const r = await fetch('/singeros/api/recordings/' + session.id + '/finalize', {\n        method:'POST',\n        headers:{'content-type':'application/json'},\n        body:JSON.stringify(meta)\n      });\n      if (!r.ok) throw new Error(await r.text());\n      logLine('录音已持久化到服务器', await r.json());\n    }\n\n    if (stream) stream.getTracks().forEach(t => t.stop());\n    if (meterFrame) cancelAnimationFrame(meterFrame);\n    if (monitorStream) monitorStream.getTracks().forEach(t => t.stop());\n    for (const node of [source, analyser, monitorSource, monitorHighpass, monitorCompressor, monitorGain]) {\n      try { node?.disconnect(); } catch {}\n    }\n    stream = source = analyser = monitorSource = monitorHighpass = monitorCompressor = monitorGain = recorder = null;\n    monitorStream = null;\n    session = null;\n    monitoring = false;\n    $('monitor').textContent = '打开返听';\n    $('monitor').disabled = true;\n    $('start').disabled = false;\n    $('stop').disabled = true;\n    $('state').textContent = '已保存';\n    $('state').className = 'badge ok';\n    renderCapabilities();\n    await listRecordings();\n  } catch (e) {\n    logLine('结束或保存失败', String(e));\n    $('state').textContent = '保存失败';\n    $('state').className = 'badge bad';\n  }\n}\n\nasync function listRecordings() {\n  const r = await fetch('/singeros/api/recordings');\n  const items = await r.json();\n  $('recs').innerHTML = items.length ? items.map(x =>\n    '<div><button data-play=\"' + x.id + '\">播放</button><button data-delete=\"' + x.id + '\">删除</button> ' +\n    x.id + ' · ' + (x.bytes/1024/1024).toFixed(2) + ' MiB</div>'\n  ).join('') : '暂无录音';\n\n  document.querySelectorAll('[data-play]').forEach(b => b.onclick = () => {\n    $('player').src = '/singeros/api/recordings/' + b.dataset.play + '/audio';\n    $('player').play().catch(e => logLine('回放失败', String(e)));\n  });\n  document.querySelectorAll('[data-delete]').forEach(b => b.onclick = async () => {\n    await fetch('/singeros/api/recordings/' + b.dataset.delete, {method:'DELETE'});\n    listRecordings();\n  });\n}\n\n$('start').onclick = startCapture;\n$('stop').onclick = stopCapture;\n$('refresh').onclick = listRecordings;\n$('probe').onclick = probe;\n$('gain').oninput = () => {\n  if (monitorGain) monitorGain.gain.value = monitoring ? Number($('gain').value)/100 : 0;\n};\nasync function stopMonitor() {\n  monitoring = false;\n  if (monitorGain) monitorGain.gain.value = 0;\n  if (monitorStream) monitorStream.getTracks().forEach(t => t.stop());\n  for (const node of [monitorSource, monitorHighpass, monitorCompressor, monitorGain]) {\n    try { node?.disconnect(); } catch {}\n  }\n  monitorStream = null;\n  monitorSource = monitorHighpass = monitorCompressor = monitorGain = null;\n  $('monitor').textContent = '打开返听';\n  logLine('车载安全返听关闭');\n}\nasync function startMonitor() {\n  await ensureAudioContext();\n  const selectedDevice = trackSettings.deviceId || $('device').value;\n  const monitorConstraints = {audio:{\n    ...(selectedDevice ? {deviceId:{exact:selectedDevice}} : {}),\n    sampleRate:{ideal:48000},\n    channelCount:{ideal:1},\n    echoCancellation:true,\n    noiseSuppression:false,\n    autoGainControl:false\n  }};\n  try {\n    monitorStream = await navigator.mediaDevices.getUserMedia(monitorConstraints);\n    const mt = monitorStream.getAudioTracks()[0];\n    monitorSource = ctx.createMediaStreamSource(monitorStream);\n    monitorHighpass = ctx.createBiquadFilter();\n    monitorHighpass.type = 'highpass';\n    monitorHighpass.frequency.value = 90;\n    monitorHighpass.Q.value = 0.707;\n    monitorCompressor = ctx.createDynamicsCompressor();\n    monitorCompressor.threshold.value = -20;\n    monitorCompressor.knee.value = 10;\n    monitorCompressor.ratio.value = 10;\n    monitorCompressor.attack.value = 0.003;\n    monitorCompressor.release.value = 0.18;\n    monitorGain = ctx.createGain();\n    monitorGain.gain.value = Number($('gain').value)/100;\n    monitorSource.connect(monitorHighpass);\n    monitorHighpass.connect(monitorCompressor);\n    monitorCompressor.connect(monitorGain);\n    monitorGain.connect(ctx.destination);\n    monitoring = true;\n    $('monitor').textContent = '关闭返听';\n    logLine('车载安全返听开启', {\n      gain:monitorGain.gain.value,\n      echoCancellation:mt.getSettings?.().echoCancellation,\n      device:mt.label,\n      chain:'AEC monitor stream -> 90Hz HPF -> 10:1 limiter'\n    });\n  } catch (e) {\n    await stopMonitor();\n    logLine('返听启动失败，为避免啸叫未回退到原始录音轨', explainError(e));\n  }\n}\n$('monitor').onclick = async () => {\n  if (monitoring) await stopMonitor(); else await startMonitor();\n};\n$('speaker').onclick = async () => {\n  try {\n    const c = await ensureAudioContext();\n    const oscillator = c.createOscillator();\n    const gain = c.createGain();\n    const now = c.currentTime;\n    oscillator.frequency.value = 523.25;\n    gain.gain.setValueAtTime(0.0001, now);\n    gain.gain.exponentialRampToValueAtTime(0.05, now + 0.03);\n    gain.gain.exponentialRampToValueAtTime(0.0001, now + 0.35);\n    oscillator.connect(gain);\n    gain.connect(c.destination);\n    oscillator.start(now);\n    oscillator.stop(now + 0.36);\n    logLine('测试音已发送到默认音频输出', {sampleRate:c.sampleRate});\n  } catch (e) {\n    logLine('音响测试失败', explainError(e));\n  }\n};\n\ndocument.addEventListener('visibilitychange', () => {\n  logLine('页面可见性变化', document.visibilityState);\n  if (document.visibilityState === 'visible' && ctx?.state === 'suspended') {\n    ctx.resume().catch(e => logLine('恢复 AudioContext 失败', String(e)));\n  }\n});\n\nlogLine('能力探测页面加载', {\n  secureContext:window.isSecureContext,\n  protocol:location.protocol,\n  mediaDevices:!!navigator.mediaDevices,\n  mediaRecorder:!!window.MediaRecorder,\n  audioContext:!!AudioCtx\n});\nprobe();\nlistRecordings();\nrenderCapabilities();\n})();\n</script>\n</body>\n</html>"
