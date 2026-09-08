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
var buildCommit = "dev"

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
		c.JSON(http.StatusOK, gin.H{"ok": true, "app": appName, "commit": buildCommit})
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

const page = "<!doctype html>\n<html lang=\"zh-CN\">\n<head>\n<meta charset=\"utf-8\">\n<meta name=\"viewport\" content=\"width=device-width,initial-scale=1,maximum-scale=1,user-scalable=no\">\n<title>singerOS · Tesla Karaoke</title>\n</head>\n<body>\n<div id=\"app\"></div>\n<script>\n(() => {\n'use strict';\n\nconst style = document.createElement('style');\nstyle.textContent = `\n:root{\n  --bg:#07090d;--panel:rgba(17,21,29,.78);--panel-strong:rgba(20,25,34,.94);\n  --line:rgba(255,255,255,.085);--line-strong:rgba(255,255,255,.14);\n  --text:#f7f8fb;--muted:#9098aa;--soft:#b8bfcc;--accent:#7c5cff;--accent2:#30d5c8;\n  --good:#70e3a1;--bad:#ff7b86;--warn:#ffc66d;--shadow:0 28px 80px rgba(0,0,0,.34);\n  --radius:24px\n}\n*{box-sizing:border-box}\nhtml{background:var(--bg)}\nbody{margin:0;min-height:100vh;color:var(--text);font-family:Inter,ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,\"Segoe UI\",sans-serif;background:\nradial-gradient(900px 540px at 8% -10%,rgba(124,92,255,.18),transparent 63%),\nradial-gradient(700px 500px at 96% 2%,rgba(48,213,200,.12),transparent 62%),\nlinear-gradient(180deg,#0a0c12 0%,#07090d 52%,#080a0f 100%);letter-spacing:-.01em}\nbody:before{content:\"\";position:fixed;inset:0;pointer-events:none;background-image:linear-gradient(rgba(255,255,255,.012) 1px,transparent 1px),linear-gradient(90deg,rgba(255,255,255,.012) 1px,transparent 1px);background-size:42px 42px;mask-image:linear-gradient(to bottom,black,transparent 80%)}\nbutton,input,select{font:inherit}\nbutton{border:0;color:inherit}\n.shell{max-width:1420px;margin:0 auto;padding:28px}\n.topbar{display:flex;align-items:center;justify-content:space-between;gap:20px;margin-bottom:24px}\n.brand{display:flex;align-items:center;gap:14px}\n.brand-mark{width:46px;height:46px;border-radius:15px;display:grid;place-items:center;background:linear-gradient(145deg,rgba(124,92,255,.98),rgba(79,58,188,.85));box-shadow:0 12px 38px rgba(124,92,255,.3),inset 0 1px 0 rgba(255,255,255,.32)}\n.brand-mark svg{width:24px;height:24px}\n.eyebrow{font-size:11px;letter-spacing:.16em;text-transform:uppercase;color:#a9a0ff;font-weight:800;margin-bottom:4px}\n.brand h1{font-size:22px;margin:0;font-weight:720;letter-spacing:-.035em}\n.brand-sub{font-size:13px;color:var(--muted);margin-top:4px}\n.badge{display:inline-flex;align-items:center;gap:8px;min-height:38px;padding:0 14px;border:1px solid var(--line-strong);border-radius:999px;background:rgba(255,255,255,.045);backdrop-filter:blur(14px);font-size:13px;color:var(--soft)}\n.badge:before{content:\"\";width:7px;height:7px;border-radius:50%;background:#6e7689;box-shadow:0 0 0 5px rgba(110,118,137,.08)}\n.badge.ok{color:var(--good);border-color:rgba(112,227,161,.22)}.badge.ok:before{background:var(--good);box-shadow:0 0 0 5px rgba(112,227,161,.09)}\n.badge.bad{color:var(--bad);border-color:rgba(255,123,134,.24)}.badge.bad:before{background:var(--bad);box-shadow:0 0 0 5px rgba(255,123,134,.09)}\n.hero{display:grid;grid-template-columns:minmax(0,1.35fr) minmax(330px,.65fr);gap:18px}\n.panel{position:relative;overflow:hidden;border:1px solid var(--line);border-radius:var(--radius);background:linear-gradient(155deg,rgba(22,27,37,.88),rgba(12,15,22,.78));box-shadow:var(--shadow);backdrop-filter:blur(22px)}\n.panel:after{content:\"\";position:absolute;inset:0;pointer-events:none;border-radius:inherit;box-shadow:inset 0 1px 0 rgba(255,255,255,.04)}\n.performance{padding:28px}\n.performance:before{content:\"\";position:absolute;width:360px;height:360px;border-radius:50%;right:-180px;top:-190px;background:radial-gradient(circle,rgba(124,92,255,.22),transparent 70%)}\n.section-label{font-size:12px;text-transform:uppercase;letter-spacing:.14em;color:var(--muted);font-weight:750}\n.performance-title{display:flex;align-items:flex-end;justify-content:space-between;gap:18px;margin:8px 0 24px}\n.performance-title h2{font-size:34px;line-height:1.05;margin:0;letter-spacing:-.05em;font-weight:750}\n.quality{display:flex;flex-wrap:wrap;gap:7px;justify-content:flex-end}\n.chip{padding:7px 10px;border-radius:999px;border:1px solid var(--line);background:rgba(255,255,255,.035);color:var(--soft);font-size:11px;font-weight:700}\n.actions{display:grid;grid-template-columns:1.4fr 1fr 1fr 1fr;gap:10px}\n.action{min-height:56px;border-radius:16px;border:1px solid var(--line);background:rgba(255,255,255,.048);display:flex;align-items:center;justify-content:center;gap:9px;padding:0 16px;font-weight:700;cursor:pointer;transition:transform .16s ease,background .16s ease,border-color .16s ease,box-shadow .16s ease}\n.action svg{width:19px;height:19px;opacity:.9}.action:hover{background:rgba(255,255,255,.075);border-color:var(--line-strong);transform:translateY(-1px)}\n.action:active{transform:translateY(0) scale(.99)}.action:disabled{opacity:.34;cursor:not-allowed;transform:none}\n.action.primary{background:linear-gradient(135deg,#7c5cff,#6848ee);border-color:rgba(166,148,255,.5);box-shadow:0 12px 34px rgba(124,92,255,.2)}\n.controls-grid{display:grid;grid-template-columns:minmax(260px,1.1fr) minmax(260px,.9fr);gap:14px;margin-top:18px}\n.control-card{border:1px solid var(--line);border-radius:18px;background:rgba(6,8,13,.28);padding:16px}\n.control-head{display:flex;justify-content:space-between;align-items:center;gap:12px;margin-bottom:12px}\n.control-title{font-size:13px;font-weight:760}.control-note{font-size:11px;color:var(--muted)}\n.select-wrap{display:flex;gap:8px}\nselect{width:100%;min-height:48px;border:1px solid var(--line-strong);border-radius:14px;padding:0 38px 0 13px;color:var(--text);background:#11151d;outline:none}\n.mini-btn{min-height:48px;white-space:nowrap;border-radius:14px;padding:0 14px;background:rgba(255,255,255,.055);border:1px solid var(--line);cursor:pointer}\n.monitor-line{display:flex;align-items:center;gap:14px}\n.monitor-value{font-size:28px;font-variant-numeric:tabular-nums;font-weight:760;min-width:66px;letter-spacing:-.04em}\ninput[type=range]{appearance:none;width:100%;height:5px;border-radius:999px;background:linear-gradient(90deg,var(--accent),var(--accent2));outline:none}\ninput[type=range]::-webkit-slider-thumb{appearance:none;width:22px;height:22px;border-radius:50%;background:#fff;border:5px solid #7c5cff;box-shadow:0 3px 14px rgba(0,0,0,.35)}\n.toggles{display:flex;gap:8px;flex-wrap:wrap;margin-top:14px}\n.toggle{display:inline-flex;align-items:center;gap:8px;padding:8px 10px;border-radius:12px;border:1px solid var(--line);background:rgba(255,255,255,.025);font-size:12px;color:var(--soft);cursor:pointer}\n.toggle input{accent-color:var(--accent)}\n.signal{padding:24px;display:flex;flex-direction:column;min-height:100%}\n.signal-top{display:flex;align-items:flex-start;justify-content:space-between;gap:16px}\n.db-big{font-size:52px;line-height:.95;font-weight:760;letter-spacing:-.065em;font-variant-numeric:tabular-nums;margin-top:14px}\n.db-unit{font-size:13px;color:var(--muted);font-weight:650;margin-left:5px;letter-spacing:0}\n.meter{height:12px;margin:22px 0 12px;border-radius:999px;background:rgba(255,255,255,.055);overflow:hidden;border:1px solid rgba(255,255,255,.04)}\n.bar{height:100%;width:0;background:linear-gradient(90deg,#42dfaa 0%,#74e597 52%,#ffc66d 78%,#ff7b86 100%);border-radius:inherit;box-shadow:0 0 22px rgba(66,223,170,.28);transition:width .08s linear}\n.feedback-box{margin-top:auto;border:1px solid rgba(124,92,255,.18);background:linear-gradient(145deg,rgba(124,92,255,.08),rgba(48,213,200,.035));border-radius:18px;padding:15px}\n.feedback-title{display:flex;align-items:center;gap:8px;font-size:12px;font-weight:760;color:#b7aaff;margin-bottom:7px}.feedback-title i{width:8px;height:8px;border-radius:50%;background:#8b72ff;box-shadow:0 0 14px #7c5cff}\n#feedback{font-size:13px;color:var(--soft);line-height:1.55}\n.feedback-copy{font-size:11px;color:var(--muted);margin-top:8px}\n.workspace{display:grid;grid-template-columns:minmax(0,.92fr) minmax(0,1.08fr);gap:18px;margin-top:18px}\n.workspace .panel{padding:22px}\n.panel-head{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:16px}\n.panel-head h3{margin:0;font-size:16px;letter-spacing:-.025em}.panel-head p{margin:4px 0 0;font-size:12px;color:var(--muted)}\n.list{display:flex;flex-direction:column;gap:8px;max-height:300px;overflow:auto;padding-right:4px}\n.list>div{display:flex;align-items:center;gap:7px;min-height:50px;padding:8px 10px;border:1px solid var(--line);border-radius:14px;background:rgba(255,255,255,.025);font-size:12px;color:var(--soft)}\n.list button{min-height:34px;border-radius:10px;padding:0 10px;background:rgba(255,255,255,.06);border:1px solid var(--line);cursor:pointer}\naudio{width:100%;margin-top:13px;height:42px}\n.caps{display:grid;grid-template-columns:155px minmax(0,1fr);gap:0;border:1px solid var(--line);border-radius:16px;overflow:hidden;max-height:362px;overflow-y:auto}\n.caps>div{padding:10px 12px;border-bottom:1px solid rgba(255,255,255,.045);font-size:11px;overflow-wrap:anywhere}\n.caps>div:nth-child(odd){color:var(--muted);background:rgba(255,255,255,.018);font-weight:700}.caps>div:nth-child(even){color:var(--soft);font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:10.5px}\n.diagnostics{margin-top:18px}\n.diagnostics summary{list-style:none;cursor:pointer;display:flex;align-items:center;justify-content:space-between;gap:14px;padding:18px 22px}\n.diagnostics summary::-webkit-details-marker{display:none}\n.diagnostics summary:after{content:\"+\";font-size:22px;color:var(--muted)}.diagnostics[open] summary:after{content:\"–\"}\n.diagnostics pre{margin:0 18px 18px;white-space:pre-wrap;overflow:auto;max-height:310px;background:#080a0f;border:1px solid var(--line);padding:14px;border-radius:14px;font-size:11px;color:#aeb6c6;line-height:1.55}\n.hint{color:var(--muted);font-size:11px}\n.safety{margin-top:14px;display:flex;align-items:center;gap:8px;color:var(--muted);font-size:11px}\n.safety svg{width:15px;height:15px;color:var(--good)}\n@media(max-width:980px){.hero,.workspace{grid-template-columns:1fr}.actions{grid-template-columns:1fr 1fr}.performance-title{align-items:flex-start;flex-direction:column}.quality{justify-content:flex-start}.controls-grid{grid-template-columns:1fr}}\n@media(max-width:620px){.shell{padding:16px}.topbar{align-items:flex-start}.brand-sub{display:none}.hero{gap:12px}.performance,.signal,.workspace .panel{padding:18px}.performance-title h2{font-size:28px}.actions{grid-template-columns:1fr}.controls-grid{gap:10px}.caps{grid-template-columns:1fr}.caps>div:nth-child(odd){padding-bottom:3px;border-bottom:0}.caps>div:nth-child(even){padding-top:3px}.db-big{font-size:44px}}\n`;\ndocument.head.appendChild(style);\n\ndocument.getElementById('app').innerHTML = `\n<main class=\"shell\">\n  <header class=\"topbar\">\n    <div class=\"brand\">\n      <div class=\"brand-mark\" aria-hidden=\"true\">\n        <svg viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"1.8\"><path d=\"M12 3v12\"/><path d=\"M8.5 7.5v5a3.5 3.5 0 0 0 7 0v-5\"/><path d=\"M5.5 12a6.5 6.5 0 0 0 13 0\"/><path d=\"M12 18.5V22\"/><path d=\"M8.5 22h7\"/></svg>\n      </div>\n      <div>\n        <div class=\"eyebrow\">Tesla vocal console</div>\n        <h1>singerOS</h1>\n        <div class=\"brand-sub\">车载 K 歌</div>\n      </div>\n    </div>\n    <div id=\"state\" class=\"badge\">未采集</div>\n  </header>\n\n  <section class=\"hero\">\n    <div class=\"panel performance\">\n      <div class=\"section-label\">Performance console</div>\n      <div class=\"performance-title\">\n        <h2>录音与监听</h2>\n        <div class=\"quality\">\n          <span class=\"chip\">48 kHz</span><span class=\"chip\">16-bit</span><span class=\"chip\">Opus 256k</span><span class=\"chip\">Adaptive AFS</span>\n        </div>\n      </div>\n\n      <div class=\"actions\">\n        <button id=\"start\" class=\"action primary\"><svg viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"2\"><rect x=\"6\" y=\"3\" width=\"12\" height=\"14\" rx=\"6\"/><path d=\"M4 11a8 8 0 0 0 16 0\"/><path d=\"M12 19v3\"/></svg>开始录制</button>\n        <button id=\"stop\" class=\"action\" disabled><svg viewBox=\"0 0 24 24\" fill=\"currentColor\"><rect x=\"6\" y=\"6\" width=\"12\" height=\"12\" rx=\"2\"/></svg>停止保存</button>\n        <button id=\"monitor\" class=\"action\" disabled><svg viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"2\"><path d=\"M4 14v-4a8 8 0 0 1 16 0v4\"/><path d=\"M4 14h3v6H5a1 1 0 0 1-1-1z\"/><path d=\"M20 14h-3v6h2a1 1 0 0 0 1-1z\"/></svg>打开返听</button>\n        <button id=\"speaker\" class=\"action\"><svg viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"2\"><path d=\"M11 5 6 9H3v6h3l5 4z\"/><path d=\"M15 9.5a4 4 0 0 1 0 5\"/><path d=\"M17.5 7a7 7 0 0 1 0 10\"/></svg>音响测试</button>\n      </div>\n\n      <div class=\"controls-grid\">\n        <div class=\"control-card\">\n          <div class=\"control-head\"><div class=\"control-title\">输入设备</div><div class=\"control-note\">优先物理麦克风</div></div>\n          <div class=\"select-wrap\">\n            <select id=\"device\"><option value=\"\">自动选择</option></select>\n            <button id=\"probe\" class=\"mini-btn\">刷新</button>\n          </div>\n          <div class=\"toggles\">\n            <label class=\"toggle\"><input id=\"aec\" type=\"checkbox\"> 回声消除</label>\n            <label class=\"toggle\"><input id=\"ns\" type=\"checkbox\"> 降噪</label>\n            <label class=\"toggle\"><input id=\"agc\" type=\"checkbox\"> 自动增益</label>\n          </div>\n        </div>\n\n        <div class=\"control-card\">\n          <div class=\"control-head\"><div class=\"control-title\">返听增益</div><div class=\"control-note\">动态 notch 不改变整体音量</div></div>\n          <div class=\"monitor-line\">\n            <div class=\"monitor-value\"><span id=\"gainValue\">85</span><span style=\"font-size:13px;color:var(--muted)\">%</span></div>\n            <input id=\"gain\" type=\"range\" min=\"0\" max=\"150\" value=\"85\">\n          </div>\n          <div class=\"safety\"><svg viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"2\"><path d=\"M12 3 4 6v5c0 5 3.4 8.7 8 10 4.6-1.3 8-5 8-10V6z\"/><path d=\"m9 12 2 2 4-4\"/></svg>原始录音轨与实时返听 DSP 完全分离</div>\n        </div>\n      </div>\n    </div>\n\n    <aside class=\"panel signal\">\n      <div class=\"signal-top\">\n        <div><div class=\"section-label\">Live input</div><div class=\"db-big\"><span id=\"dbValue\">-∞</span><span class=\"db-unit\">dB</span></div></div>\n        <span class=\"chip\">LIVE METER</span>\n      </div>\n      <div class=\"meter\"><div id=\"bar\" class=\"bar\"></div></div>\n      <div id=\"db\" class=\"hint\">-∞ dB</div>\n      <div class=\"feedback-box\">\n        <div class=\"feedback-title\"><i></i>Adaptive feedback suppression</div>\n        <div id=\"feedback\">动态反馈抑制：未启用</div>\n        <div class=\"feedback-copy\">动态反馈抑制</div>\n      </div>\n    </aside>\n  </section>\n\n  <section class=\"workspace\">\n    <div class=\"panel\">\n      <div class=\"panel-head\"><div><h3>录音库</h3><p>录音文件</p></div><button id=\"refresh\" class=\"mini-btn\">刷新</button></div>\n      <div id=\"recs\" class=\"list\"></div>\n      <audio id=\"player\" controls></audio>\n    </div>\n    <div class=\"panel\">\n      <div class=\"panel-head\"><div><h3>设备与链路</h3><p>浏览器权限、采集参数与实际音频上下文</p></div><span class=\"chip\">DIAGNOSTICS</span></div>\n      <div id=\"caps\" class=\"caps\"></div>\n    </div>\n  </section>\n\n  <details class=\"panel diagnostics\">\n    <summary><div><div class=\"section-label\">Developer telemetry</div><strong>诊断日志</strong></div><span class=\"hint\">需要时展开</span></summary>\n    <pre id=\"log\"></pre>\n  </details>\n</main>`;\n\nconst $ = id => document.getElementById(id);\nconst AudioCtx = window.AudioContext || window.webkitAudioContext;\nlet stream, ctx, source, analyser, monitorCtx, monitorSource, monitorGain, monitorAnalyser, monitorLimiter, monitorNotches = [], feedbackBins, feedbackTimer, meterFrame, recorder, session;\nlet seq = 0;\nlet uploadQueue = Promise.resolve();\nlet permissionState = 'unknown';\nlet devices = [];\nlet trackSettings = {};\nlet trackCapabilities = {};\nlet trackConstraints = {};\nlet monitoring = false;\nlet feedbackState = new Map();\nlet activeFeedback = [];\nlet recorderInfo = {};\nconst requestedAudioBitsPerSecond = 256000;\nconst logs = [];\n\nfunction logLine(message, details) {\n  const line = '[' + new Date().toLocaleTimeString('zh-CN', {hour12:false}) + '] ' + message +\n    (details !== undefined ? ' | ' + (typeof details === 'string' ? details : JSON.stringify(details)) : '');\n  logs.push(line);\n  if (logs.length > 180) logs.shift();\n  $('log').textContent = logs.join('\\n');\n  $('log').scrollTop = $('log').scrollHeight;\n}\n\nfunction explainError(error) {\n  const map = {\n    NotAllowedError: '浏览器或车机系统策略拒绝麦克风',\n    NotFoundError: '车机没有向网页暴露任何音频输入设备',\n    NotReadableError: '麦克风被系统/语音助手占用，或底层不可读',\n    AbortError: '底层音频设备启动失败',\n    OverconstrainedError: '车机音频设备不支持请求的采集参数',\n    SecurityError: '浏览器安全策略禁止音频采集',\n    TypeError: '页面不是安全上下文，或浏览器缺少所需接口',\n    TimeoutError: '15秒内没有权限/设备结果，可能没有实现授权界面'\n  };\n  return (error?.name || 'Error') + ': ' + (map[error?.name] || error?.message || String(error));\n}\n\nfunction renderCapabilities() {\n  const rows = [\n    ['安全上下文', window.isSecureContext],\n    ['协议', location.protocol],\n    ['getUserMedia', !!navigator.mediaDevices?.getUserMedia],\n    ['enumerateDevices', !!navigator.mediaDevices?.enumerateDevices],\n    ['Permissions API', !!navigator.permissions?.query],\n    ['麦克风权限', permissionState],\n    ['audioinput 数量', devices.length],\n    ['设备', devices.map(d => d.label || '(授权前名称隐藏)').join('；') || '无'],\n    ['Web Audio', !!AudioCtx],\n    ['MediaRecorder', !!window.MediaRecorder],\n    ['Recorder', JSON.stringify(recorderInfo)],\n    ['Track settings', JSON.stringify(trackSettings)],\n    ['Track capabilities', JSON.stringify(trackCapabilities)],\n    ['Track constraints', JSON.stringify(trackConstraints)],\n    ['采集 AudioContext', ctx ? (ctx.state + ', ' + ctx.sampleRate + 'Hz, base ' + Math.round((ctx.baseLatency||0)*1000) + 'ms') : '未创建'],\n    ['返听 AudioContext', monitorCtx ? (monitorCtx.state + ', ' + monitorCtx.sampleRate + 'Hz, base ' + Math.round((monitorCtx.baseLatency||0)*1000) + 'ms, out ' + Math.round((monitorCtx.outputLatency||0)*1000) + 'ms') : '未创建'],\n    ['上传会话', session?.id || '无'],\n    ['User-Agent', navigator.userAgent]\n  ];\n  $('caps').innerHTML = rows.map(([k,v]) => '<div>' + k + '</div><div>' + String(v) + '</div>').join('');\n}\n\nasync function probe() {\n  try {\n    if (navigator.permissions?.query) {\n      const p = await navigator.permissions.query({name:'microphone'});\n      permissionState = p.state;\n      p.onchange = () => { permissionState = p.state; logLine('麦克风权限变化', p.state); renderCapabilities(); };\n    }\n  } catch (e) {\n    permissionState = '无法查询';\n    logLine('权限查询失败', explainError(e));\n  }\n\n  try {\n    devices = (await navigator.mediaDevices?.enumerateDevices?.() || []).filter(x => x.kind === 'audioinput');\n    logLine('音频输入枚举完成', devices.map(x => x.label || '(未授权隐藏名称)'));\n    const select = $('device');\n    const previous = select.value;\n    select.innerHTML = '<option value=\"\">自动选择</option>' + devices.map(d => '<option value=\"' + d.deviceId + '\">' + (d.label || '未命名麦克风') + '</option>').join('');\n    if (devices.some(d => d.deviceId === previous)) {\n      select.value = previous;\n    } else {\n      const physical = devices.find(d => /shure|mvx2u|usb/i.test(d.label) && !/virtual|communications|通讯/i.test(d.label));\n      const normal = devices.find(d => d.deviceId !== 'communications' && !/virtual|communications|通讯/i.test(d.label));\n      const preferred = physical || normal || devices[0];\n      if (preferred) select.value = preferred.deviceId;\n    }\n  } catch (e) {\n    devices = [];\n    logLine('设备枚举失败', explainError(e));\n  }\n  renderCapabilities();\n}\n\nasync function ensureAudioContext() {\n  if (!AudioCtx) throw new Error('Web Audio API unavailable');\n  if (!ctx) ctx = new AudioCtx({latencyHint:'interactive'});\n  if (ctx.state === 'suspended') await ctx.resume();\n  renderCapabilities();\n  return ctx;\n}\n\nasync function ensureMonitorContext() {\n  if (!AudioCtx) throw new Error('Web Audio API unavailable');\n  if (!monitorCtx || monitorCtx.state === 'closed') {\n    const options = {latencyHint:0.02};\n    if (trackSettings.sampleRate) options.sampleRate = trackSettings.sampleRate;\n    try {\n      monitorCtx = new AudioCtx(options);\n    } catch (_) {\n      monitorCtx = new AudioCtx({latencyHint:'balanced'});\n    }\n  }\n  if (monitorCtx.state === 'suspended') await monitorCtx.resume();\n  if (stream && !monitorSource) monitorSource = monitorCtx.createMediaStreamSource(stream);\n  renderCapabilities();\n  return monitorCtx;\n}\n\nfunction startMeter() {\n  if (!analyser) return;\n  const values = new Uint8Array(analyser.fftSize);\n  const tick = () => {\n    analyser.getByteTimeDomainData(values);\n    let sum = 0;\n    for (const x of values) {\n      const v = (x - 128) / 128;\n      sum += v * v;\n    }\n    const rms = Math.sqrt(sum / values.length);\n    const db = rms ? 20 * Math.log10(rms) : -Infinity;\n    const dbText = Number.isFinite(db) ? db.toFixed(1) : '-∞';\n    $('db').textContent = dbText + ' dB';\n    if ($('dbValue')) $('dbValue').textContent = dbText;\n    $('bar').style.width = Math.max(0, Math.min(100, (db + 60) * 1.67)) + '%';\n    meterFrame = requestAnimationFrame(tick);\n  };\n  tick();\n}\n\nasync function startServerSession(mime) {\n  const r = await fetch('/singeros/api/recordings/start', {\n    method:'POST',\n    headers:{'content-type':'application/json'},\n    body:JSON.stringify({mime})\n  });\n  if (!r.ok) throw new Error(await r.text());\n  return r.json();\n}\n\nasync function uploadChunk(blob, number) {\n  const r = await fetch('/singeros/api/recordings/' + session.id + '/chunk?seq=' + number, {\n    method:'POST',\n    headers:{'content-type':'application/octet-stream'},\n    body:blob\n  });\n  if (!r.ok) throw new Error(await r.text());\n  logLine('录音分片上传完成', {seq:number, bytes:blob.size});\n}\n\nasync function startCapture() {\n  try {\n    if (!window.isSecureContext) throw new Error('当前不是 HTTPS 安全上下文，Tesla 浏览器不会开放麦克风');\n    if (!navigator.mediaDevices?.getUserMedia) throw new Error('getUserMedia unavailable');\n\n    await probe();\n    const selectedDevice = $('device').value;\n    const constraints = {\n      audio:{\n        ...(selectedDevice ? {deviceId:{exact:selectedDevice}} : {}),\n        sampleRate:{ideal:48000},\n        sampleSize:{ideal:16},\n        channelCount:{ideal:2},\n        latency:{ideal:0.002},\n        echoCancellation:$('aec').checked,\n        noiseSuppression:$('ns').checked,\n        autoGainControl:$('agc').checked\n      }\n    };\n    logLine('请求高保真采集', {deviceId:selectedDevice || 'auto', requested:constraints.audio});\n\n    const timeout = new Promise((_, reject) => setTimeout(() => {\n      const e = new Error('getUserMedia did not settle within 15 seconds');\n      e.name = 'TimeoutError';\n      reject(e);\n    }, 15000));\n\n    stream = await Promise.race([navigator.mediaDevices.getUserMedia(constraints), timeout]);\n    const track = stream.getAudioTracks()[0];\n    trackSettings = track.getSettings?.() || {};\n    trackCapabilities = track.getCapabilities?.() || {};\n    trackConstraints = track.getConstraints?.() || {};\n    logLine('麦克风采集成功', {label:track.label, settings:trackSettings});\n\n    track.onmute = () => logLine('音频输入轨道被系统静音');\n    track.onunmute = () => logLine('音频输入轨道恢复');\n    track.onended = () => logLine('音频输入轨道被系统终止');\n\n    await ensureAudioContext();\n    source = ctx.createMediaStreamSource(stream);\n    analyser = ctx.createAnalyser();\n    analyser.fftSize = 2048;\n    analyser.smoothingTimeConstant = 0.72;\n    source.connect(analyser);\n    startMeter();\n\n    await ensureMonitorContext();\n    logLine('返听音频上下文预热完成', {\n      latencyHint:'20ms',\n      sampleRate:monitorCtx.sampleRate,\n      baseLatency:monitorCtx.baseLatency,\n      outputLatency:monitorCtx.outputLatency\n    });\n\n    const preferred = ['audio/webm;codecs=opus','audio/webm','audio/ogg;codecs=opus','audio/mp4'];\n    const selectedMIME = preferred.find(x => MediaRecorder.isTypeSupported?.(x)) || '';\n    session = await startServerSession(selectedMIME);\n    seq = 0;\n    uploadQueue = Promise.resolve();\n\n    const recorderOptions = selectedMIME ? {mimeType:selectedMIME, audioBitsPerSecond:requestedAudioBitsPerSecond} : {audioBitsPerSecond:requestedAudioBitsPerSecond};\n    recorder = new MediaRecorder(stream, recorderOptions);\n    recorderInfo = {mimeType:recorder.mimeType, requestedAudioBitsPerSecond, actualAudioBitsPerSecond:recorder.audioBitsPerSecond || null, audioBitrateMode:recorder.audioBitrateMode || null};\n    logLine('MediaRecorder 高质量模式', recorderInfo);\n    recorder.ondataavailable = event => {\n      if (!event.data?.size) return;\n      const number = seq++;\n      uploadQueue = uploadQueue.then(() => uploadChunk(event.data, number));\n      uploadQueue.catch(e => logLine('分片上传失败', String(e)));\n    };\n    recorder.onerror = event => logLine('MediaRecorder 错误', event.error?.message || 'unknown');\n    recorder.start(5000);\n\n    $('state').textContent = '录制中';\n    $('state').className = 'badge ok';\n    $('start').disabled = true;\n    $('stop').disabled = false;\n    $('monitor').disabled = false;\n\n    await probe();\n    renderCapabilities();\n  } catch (e) {\n    logLine('启动失败', explainError(e));\n    $('state').textContent = '启动失败';\n    $('state').className = 'badge bad';\n  }\n}\n\nasync function stopCapture() {\n  try {\n    if (recorder && recorder.state !== 'inactive') {\n      await new Promise(resolve => {\n        recorder.addEventListener('stop', resolve, {once:true});\n        recorder.stop();\n      });\n    }\n    await uploadQueue;\n\n    if (session) {\n      const meta = {\n        userAgent:navigator.userAgent,\n        permission:permissionState,\n        devices:devices.map(d => ({label:d.label, deviceId:d.deviceId, groupId:d.groupId})),\n        settings:trackSettings,\n        capabilities:trackCapabilities,\n        constraints:trackConstraints,\n        recorder:recorderInfo,\n        audioContext:ctx ? {\n          state:ctx.state,\n          sampleRate:ctx.sampleRate,\n          baseLatency:ctx.baseLatency,\n          outputLatency:ctx.outputLatency\n        } : null\n      };\n      const r = await fetch('/singeros/api/recordings/' + session.id + '/finalize', {\n        method:'POST',\n        headers:{'content-type':'application/json'},\n        body:JSON.stringify(meta)\n      });\n      if (!r.ok) throw new Error(await r.text());\n      logLine('录音已持久化到服务器', await r.json());\n    }\n\n    if (monitoring) await stopMonitor();\n    if (stream) stream.getTracks().forEach(t => t.stop());\n    if (meterFrame) cancelAnimationFrame(meterFrame);\n    for (const node of [source, analyser, monitorSource]) {\n      try { node?.disconnect(); } catch {}\n    }\n    if (monitorCtx && monitorCtx.state !== 'closed') {\n      try { await monitorCtx.close(); } catch {}\n    }\n    stream = source = analyser = monitorSource = recorder = null;\n    monitorCtx = null;\n    monitorGain = monitorAnalyser = monitorLimiter = null;\n    monitorNotches = [];\n    feedbackBins = null;\n    activeFeedback = [];\n    feedbackState.clear();\n    session = null;\n    monitoring = false;\n    $('monitor').textContent = '打开返听';\n    $('monitor').disabled = true;\n    $('start').disabled = false;\n    $('stop').disabled = true;\n    $('state').textContent = '已保存';\n    $('state').className = 'badge ok';\n    renderCapabilities();\n    await listRecordings();\n  } catch (e) {\n    logLine('结束或保存失败', String(e));\n    $('state').textContent = '保存失败';\n    $('state').className = 'badge bad';\n  }\n}\n\nasync function listRecordings() {\n  const r = await fetch('/singeros/api/recordings');\n  const items = await r.json();\n  $('recs').innerHTML = items.length ? items.map(x =>\n    '<div><button data-play=\"' + x.id + '\">播放</button><button data-delete=\"' + x.id + '\">删除</button> ' +\n    x.id + ' · ' + (x.bytes/1024/1024).toFixed(2) + ' MiB</div>'\n  ).join('') : '暂无录音';\n\n  document.querySelectorAll('[data-play]').forEach(b => b.onclick = () => {\n    $('player').src = '/singeros/api/recordings/' + b.dataset.play + '/audio';\n    $('player').play().catch(e => logLine('回放失败', String(e)));\n  });\n  document.querySelectorAll('[data-delete]').forEach(b => b.onclick = async () => {\n    await fetch('/singeros/api/recordings/' + b.dataset.delete, {method:'DELETE'});\n    listRecordings();\n  });\n}\n\n$('start').onclick = startCapture;\n$('stop').onclick = stopCapture;\n$('refresh').onclick = listRecordings;\n$('probe').onclick = probe;\n$('gain').oninput = () => {\n  if ($('gainValue')) $('gainValue').textContent = $('gain').value;\n  if (monitorGain) monitorGain.gain.value = monitoring ? Number($('gain').value)/100 : 0;\n};\nfunction feedbackKey(freq) {\n  return Math.round(48 * Math.log2(freq / 55));\n}\nfunction looksHarmonic(data, bin, peakDb) {\n  let harmonicHits = 0;\n  for (const multiple of [2, 3, 4]) {\n    const h = Math.round(bin * multiple);\n    if (h < data.length && data[h] > peakDb - 15) harmonicHits++;\n  }\n  return harmonicHits >= 2;\n}\nfunction renderFeedbackState() {\n  if (!monitoring) {\n    $('feedback').textContent = '动态反馈抑制：未启用';\n    return;\n  }\n  const active = activeFeedback.filter(x => x.active);\n  $('feedback').textContent = active.length\n    ? '已抑制：' + active.map(x => Math.round(x.freq) + ' Hz').join(' · ')\n    : '监听中 · 当前没有需要抑制的反馈频点';\n}\nfunction activateFeedbackNotch(freq, peakDb, pnpr) {\n  const now = performance.now();\n  const existing = activeFeedback.find(x => x.active && Math.abs(Math.log2(x.freq / freq)) < 1/48);\n  if (existing) {\n    existing.lastSeen = now;\n    existing.peakDb = Math.max(existing.peakDb, peakDb);\n    return;\n  }\n  let slot = activeFeedback.find(x => !x.active);\n  if (!slot) slot = activeFeedback.slice().sort((a,b) => a.lastSeen - b.lastSeen)[0];\n  slot.active = true;\n  slot.freq = freq;\n  slot.lastSeen = now;\n  slot.peakDb = peakDb;\n  slot.node.frequency.setTargetAtTime(freq, monitorCtx.currentTime, 0.01);\n  slot.node.Q.setTargetAtTime(55, monitorCtx.currentTime, 0.01);\n  logLine('动态 notch 已锁定反馈频点', {frequencyHz:Math.round(freq), peakDb:Number(peakDb.toFixed(1)), pnprDb:Number(pnpr.toFixed(1)), q:55});\n  renderFeedbackState();\n}\nfunction detectFeedback() {\n  if (!monitoring || !monitorAnalyser || !monitorCtx || !feedbackBins) return;\n  monitorAnalyser.getFloatFrequencyData(feedbackBins);\n  const data = feedbackBins;\n  const binHz = monitorCtx.sampleRate / monitorAnalyser.fftSize;\n  const low = Math.max(16, Math.ceil(80 / binHz));\n  const high = Math.min(data.length - 20, Math.floor(10000 / binHz));\n  const now = performance.now();\n  const seenKeys = new Set();\n  const strongest = [];\n\n  for (let i = low; i <= high; i++) {\n    const db = data[i];\n    if (!Number.isFinite(db) || db < -48) continue;\n    if (db <= data[i-1] || db <= data[i+1]) continue;\n    let neighborSum = 0, neighborCount = 0;\n    for (let d = 4; d <= 14; d++) {\n      neighborSum += data[i-d] + data[i+d];\n      neighborCount += 2;\n    }\n    const neighborDb = neighborSum / neighborCount;\n    const pnpr = db - neighborDb;\n    if (pnpr < 12) continue;\n    strongest.push({i, freq:i * binHz, db, pnpr});\n  }\n\n  strongest.sort((a,b) => b.db - a.db);\n  if (strongest.length > 8) strongest.length = 8;\n\n  for (const p of strongest) {\n    const key = feedbackKey(p.freq);\n    seenKeys.add(key);\n    let st = feedbackState.get(key);\n    if (!st || now - st.last > 350) st = {hits:0, first:now, last:now, freq:p.freq, startDb:p.db, peakDb:p.db};\n    st.hits++;\n    st.last = now;\n    st.freq = p.freq;\n    st.peakDb = Math.max(st.peakDb, p.db);\n    st.pnpr = p.pnpr;\n    st.musical = looksHarmonic(data, p.i, p.db);\n    feedbackState.set(key, st);\n\n    const fastHowl = !st.musical && st.hits >= 2 && p.db > -18 && p.pnpr > 18;\n    const persistentHowl = !st.musical && st.hits >= 4 && (now - st.first) >= 280 && p.pnpr > 13;\n    if (fastHowl || persistentHowl) activateFeedbackNotch(p.freq, p.db, p.pnpr);\n  }\n\n  for (const [key, st] of feedbackState) {\n    if (now - st.last > 1800) feedbackState.delete(key);\n  }\n\n  for (const slot of activeFeedback) {\n    if (!slot.active) continue;\n    const key = feedbackKey(slot.freq);\n    let nearby = false;\n    for (const seen of seenKeys) {\n      if (Math.abs(seen - key) <= 1) { nearby = true; break; }\n    }\n    if (nearby) slot.lastSeen = now;\n    if (now - slot.lastSeen > 30000) {\n      logLine('动态 notch 释放', {frequencyHz:Math.round(slot.freq)});\n      slot.active = false;\n      slot.freq = 10;\n      slot.node.frequency.setTargetAtTime(10, monitorCtx.currentTime, 0.03);\n      slot.node.Q.setTargetAtTime(100, monitorCtx.currentTime, 0.03);\n      renderFeedbackState();\n    }\n  }\n}\nasync function stopMonitor() {\n  monitoring = false;\n  if (feedbackTimer) clearInterval(feedbackTimer);\n  feedbackTimer = null;\n  try { if (monitorSource && monitorAnalyser) monitorSource.disconnect(monitorAnalyser); } catch {}\n  try { if (monitorSource && monitorNotches[0]) monitorSource.disconnect(monitorNotches[0]); } catch {}\n  for (const node of [monitorAnalyser, ...monitorNotches, monitorLimiter, monitorGain]) {\n    try { node?.disconnect(); } catch {}\n  }\n  monitorGain = monitorAnalyser = monitorLimiter = null;\n  monitorNotches = [];\n  feedbackBins = null;\n  activeFeedback = [];\n  feedbackState.clear();\n  $('monitor').textContent = '打开返听';\n  renderFeedbackState();\n  logLine('动态反馈抑制返听关闭');\n}\nasync function startMonitor() {\n  await ensureMonitorContext();\n  if (!monitorSource) throw new Error('请先开始麦克风录制');\n\n  monitorAnalyser = monitorCtx.createAnalyser();\n  monitorAnalyser.fftSize = 8192;\n  monitorAnalyser.smoothingTimeConstant = 0.28;\n  feedbackBins = new Float32Array(monitorAnalyser.frequencyBinCount);\n  monitorSource.connect(monitorAnalyser);\n\n  monitorNotches = Array.from({length:10}, () => {\n    const n = monitorCtx.createBiquadFilter();\n    n.type = 'notch';\n    n.frequency.value = 10;\n    n.Q.value = 100;\n    return n;\n  });\n  activeFeedback = monitorNotches.map(node => ({node, active:false, freq:10, lastSeen:0, peakDb:-120}));\n\n  let chain = monitorSource;\n  for (const notch of monitorNotches) {\n    chain.connect(notch);\n    chain = notch;\n  }\n\n  monitorLimiter = monitorCtx.createDynamicsCompressor();\n  monitorLimiter.threshold.value = -3;\n  monitorLimiter.knee.value = 0;\n  monitorLimiter.ratio.value = 20;\n  monitorLimiter.attack.value = 0.001;\n  monitorLimiter.release.value = 0.06;\n  monitorGain = monitorCtx.createGain();\n  monitorGain.gain.value = Number($('gain').value)/100;\n  chain.connect(monitorLimiter);\n  monitorLimiter.connect(monitorGain);\n  monitorGain.connect(monitorCtx.destination);\n\n  monitoring = true;\n  $('monitor').textContent = '关闭返听';\n  renderFeedbackState();\n  feedbackTimer = setInterval(detectFeedback, 100);\n  logLine('独立低延迟返听链开启', {\n    gain:monitorGain.gain.value,\n    source:trackSettings.deviceId || $('device').value,\n    sampleRate:monitorCtx.sampleRate,\n    baseLatency:monitorCtx.baseLatency,\n    outputLatency:monitorCtx.outputLatency,\n    detector:'PNPR + harmonic guard + persistence / 100ms',\n    notches:10,\n    notchQ:55,\n    globalAutoGainReduction:false\n  });\n}\n$('monitor').onclick = async () => {\n  if (monitoring) await stopMonitor(); else await startMonitor();\n};\n$('speaker').onclick = async () => {\n  try {\n    const c = await ensureAudioContext();\n    const oscillator = c.createOscillator();\n    const gain = c.createGain();\n    const now = c.currentTime;\n    oscillator.frequency.value = 523.25;\n    gain.gain.setValueAtTime(0.0001, now);\n    gain.gain.exponentialRampToValueAtTime(0.05, now + 0.03);\n    gain.gain.exponentialRampToValueAtTime(0.0001, now + 0.35);\n    oscillator.connect(gain);\n    gain.connect(c.destination);\n    oscillator.start(now);\n    oscillator.stop(now + 0.36);\n    logLine('测试音已发送到默认音频输出', {sampleRate:c.sampleRate});\n  } catch (e) {\n    logLine('音响测试失败', explainError(e));\n  }\n};\n\ndocument.addEventListener('visibilitychange', () => {\n  logLine('页面可见性变化', document.visibilityState);\n  if (document.visibilityState === 'visible' && ctx?.state === 'suspended') {\n    ctx.resume().catch(e => logLine('恢复 AudioContext 失败', String(e)));\n  }\n});\n\nlogLine('能力探测页面加载', {\n  secureContext:window.isSecureContext,\n  protocol:location.protocol,\n  mediaDevices:!!navigator.mediaDevices,\n  mediaRecorder:!!window.MediaRecorder,\n  audioContext:!!AudioCtx\n});\nprobe();\nlistRecordings();\nrenderCapabilities();\n})();\n</script>\n</body>\n</html>"
