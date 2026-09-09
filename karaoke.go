package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
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

var shanghaiLocation = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*60*60)
	}
	return loc
}()

type KaraokeCue struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

type KaraokeTrack struct {
	Mode           string       `json:"mode"`
	Version        string       `json:"version"`
	SourceFile     string       `json:"source_file"`
	SourcePage     string       `json:"source_page"`
	License        string       `json:"license"`
	LyricsLanguage string       `json:"lyrics_language"`
	Duration       float64      `json:"duration_seconds"`
	Bytes          int64        `json:"bytes"`
	URL            string       `json:"url"`
	Lyrics         []KaraokeCue `json:"lyrics"`
	SyncedAt       string       `json:"synced_at_shanghai"`
}

type KaraokeSong struct {
	ID      string                  `json:"id"`
	Title   string                  `json:"title"`
	Artist  string                  `json:"artist"`
	Version string                  `json:"version"`
	Tracks  map[string]KaraokeTrack `json:"tracks"`
}

type KaraokeCatalog struct {
	Provider      string        `json:"provider"`
	LastCrawl     string        `json:"last_crawl_shanghai"`
	NextCrawl     string        `json:"next_crawl_shanghai"`
	IntervalHours int           `json:"interval_hours"`
	LastTrigger   string        `json:"last_trigger"`
	LastError     string        `json:"last_error,omitempty"`
	Songs         []KaraokeSong `json:"songs"`
}

type karaokeSeedTrack struct {
	Mode, Version, FileTitle, Lang, License string
}

type karaokeSeedSong struct {
	ID, Title, Artist, Version string
	Tracks                     []karaokeSeedTrack
}

var karaokeSeeds = []karaokeSeedSong{
	{
		ID: "kazakhstan-2006", Title: "My Kazakhstan", Artist: "Kazakhstan national anthem", Version: "2006",
		Tracks: []karaokeSeedTrack{
			{Mode: "original", Version: "Kazakhstan 2006 vocal edition", FileTitle: "File:Kazakhstan 2006.ogg", Lang: "zh-cn", License: "Public domain / Wikimedia Commons"},
			{Mode: "accompaniment", Version: "U.S. Navy Band 2009 instrumental", FileTitle: "File:Kazakhstan national anthem, played by the U.S. Navy Band.ogg", Lang: "en", License: "Public domain / U.S. Navy"},
		},
	},
	{
		ID: "auld-lang-syne", Title: "Auld Lang Syne", Artist: "Traditional", Version: "two matched performances",
		Tracks: []karaokeSeedTrack{
			{Mode: "original", Version: "Bautsch 2025 vocal", FileTitle: "File:AuldLangSyne.ogg", Lang: "en", License: "CC0 1.0 / Wikimedia Commons"},
			{Mode: "accompaniment", Version: "U.S. Navy Band 1997 instrumental", FileTitle: "File:Auld Lang Syne - U.S. Navy Band.ogg", Lang: "en", License: "Public domain / U.S. Navy"},
		},
	},
}

type commonsImageInfo struct {
	URL            string         `json:"url"`
	DescriptionURL string         `json:"descriptionurl"`
	Size           int64          `json:"size"`
	Width          int            `json:"width"`
	Height         int            `json:"height"`
	Duration       float64        `json:"duration"`
	ExtMetadata    map[string]any `json:"extmetadata"`
}

type KaraokeService struct {
	mu       sync.RWMutex
	crawlMu  sync.Mutex
	root     string
	assets   string
	catalog  KaraokeCatalog
	client   *http.Client
	interval time.Duration
}

func newKaraokeService(dataDir string) *KaraokeService {
	root := filepath.Join(filepath.Dir(dataDir), "karaoke")
	assets := filepath.Join(root, "assets")
	_ = os.MkdirAll(assets, 0o755)
	s := &KaraokeService{
		root: root, assets: assets,
		client:   &http.Client{Timeout: 45 * time.Second},
		interval: 2 * time.Hour,
		catalog:  KaraokeCatalog{Provider: "wikimedia-commons-curated", IntervalHours: 2, Songs: []KaraokeSong{}},
	}
	s.load()
	go s.scheduler()
	return s
}

func (s *KaraokeService) load() {
	b, err := os.ReadFile(filepath.Join(s.root, "catalog.json"))
	if err != nil {
		return
	}
	var c KaraokeCatalog
	if json.Unmarshal(b, &c) == nil {
		s.catalog = c
	}
}

func (s *KaraokeService) scheduler() {
	time.Sleep(2 * time.Second)
	if err := s.Crawl("startup"); err != nil {
		log.Printf("karaoke crawl startup: %v", err)
	}
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for range t.C {
		if err := s.Crawl("scheduled"); err != nil {
			log.Printf("karaoke crawl scheduled: %v", err)
		}
	}
}

func (s *KaraokeService) snapshot() KaraokeCatalog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, _ := json.Marshal(s.catalog)
	var out KaraokeCatalog
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *KaraokeService) save(c KaraokeCatalog) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.root, "catalog.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(s.root, "catalog.json")); err != nil {
		return err
	}
	s.mu.Lock()
	s.catalog = c
	s.mu.Unlock()
	return nil
}

func (s *KaraokeService) Crawl(trigger string) error {
	s.crawlMu.Lock()
	defer s.crawlMu.Unlock()

	now := time.Now().In(shanghaiLocation)
	old := s.snapshot()
	next := KaraokeCatalog{
		Provider: "wikimedia-commons-curated", IntervalHours: 2,
		LastCrawl:   now.Format("2006-01-02 15:04:05"),
		NextCrawl:   now.Add(s.interval).Format("2006-01-02 15:04:05"),
		LastTrigger: trigger,
		Songs:       []KaraokeSong{},
	}
	oldByID := map[string]KaraokeSong{}
	for _, song := range old.Songs {
		oldByID[song.ID] = song
	}

	var errs []string
	for _, seed := range karaokeSeeds {
		song := KaraokeSong{ID: seed.ID, Title: seed.Title, Artist: seed.Artist, Version: seed.Version, Tracks: map[string]KaraokeTrack{}}
		for _, st := range seed.Tracks {
			track, err := s.syncTrack(seed.ID, st)
			if err != nil {
				errs = append(errs, seed.ID+"/"+st.Mode+": "+err.Error())
				if prev, ok := oldByID[seed.ID].Tracks[st.Mode]; ok {
					song.Tracks[st.Mode] = prev
				}
				continue
			}
			song.Tracks[st.Mode] = track
			time.Sleep(350 * time.Millisecond)
		}
		if len(song.Tracks) == 2 && len(song.Tracks["original"].Lyrics) > 0 && len(song.Tracks["accompaniment"].Lyrics) > 0 {
			next.Songs = append(next.Songs, song)
		} else if prev, ok := oldByID[seed.ID]; ok {
			next.Songs = append(next.Songs, prev)
		}
	}
	if len(errs) > 0 {
		next.LastError = strings.Join(errs, " | ")
	}
	if err := s.save(next); err != nil {
		return err
	}
	if len(next.Songs) == 0 {
		return errors.New("no karaoke song passed version/audio/lyrics validation")
	}
	if len(errs) > 0 {
		return fmt.Errorf("partial crawl: %s", next.LastError)
	}
	return nil
}

func (s *KaraokeService) syncTrack(songID string, seed karaokeSeedTrack) (KaraokeTrack, error) {
	info, err := s.commonsInfo(seed.FileTitle)
	if err != nil {
		return KaraokeTrack{}, err
	}
	vtt, err := s.commonsTimedText(seed.FileTitle, seed.Lang)
	if err != nil {
		return KaraokeTrack{}, fmt.Errorf("timed text: %w", err)
	}
	cues, err := parseVTT(vtt)
	if err != nil || len(cues) == 0 {
		return KaraokeTrack{}, errors.New("timed text empty or invalid")
	}
	dir := filepath.Join(s.assets, songID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return KaraokeTrack{}, err
	}
	ext := strings.ToLower(filepath.Ext(strings.TrimPrefix(seed.FileTitle, "File:")))
	if ext == "" || len(ext) > 8 {
		ext = ".ogg"
	}
	local := filepath.Join(dir, seed.Mode+ext)
	if err := s.downloadIfNeeded(info.URL, local, info.Size); err != nil {
		return KaraokeTrack{}, err
	}
	page := info.DescriptionURL
	if page == "" {
		page = "https://commons.wikimedia.org/wiki/" + url.PathEscape(seed.FileTitle)
	}
	return KaraokeTrack{
		Mode: seed.Mode, Version: seed.Version, SourceFile: seed.FileTitle, SourcePage: page,
		License: seed.License, LyricsLanguage: seed.Lang, Duration: info.Duration, Bytes: info.Size,
		URL:    "/singeros/api/karaoke/assets/" + songID + "/" + seed.Mode,
		Lyrics: cues, SyncedAt: time.Now().In(shanghaiLocation).Format("2006-01-02 15:04:05"),
	}, nil
}

func (s *KaraokeService) commonsInfo(title string) (commonsImageInfo, error) {
	q := url.Values{
		"action": {"query"}, "format": {"json"}, "prop": {"imageinfo"},
		"iiprop": {"url|size|mime|extmetadata"}, "titles": {title},
	}
	body, err := s.getWithRetry("https://commons.wikimedia.org/w/api.php?" + q.Encode())
	if err != nil {
		return commonsImageInfo{}, err
	}
	var payload struct {
		Query struct {
			Pages map[string]struct {
				ImageInfo []commonsImageInfo `json:"imageinfo"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return commonsImageInfo{}, err
	}
	for _, p := range payload.Query.Pages {
		if len(p.ImageInfo) > 0 && p.ImageInfo[0].URL != "" {
			return p.ImageInfo[0], nil
		}
	}
	return commonsImageInfo{}, errors.New("commons media not found")
}

func (s *KaraokeService) commonsTimedText(title, lang string) ([]byte, error) {
	q := url.Values{
		"action": {"timedtext"}, "title": {title}, "lang": {lang}, "trackformat": {"vtt"},
	}
	return s.getWithRetry("https://commons.wikimedia.org/w/api.php?" + q.Encode())
}

func (s *KaraokeService) getWithRetry(target string) ([]byte, error) {
	var last error
	for i := 0; i < 4; i++ {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("User-Agent", "singerOS/0.2 (https://github.com/wongyiuming/singerOS_by_codex)")
		resp, err := s.client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
			}
			last = fmt.Errorf("HTTP %d", resp.StatusCode)
			if resp.StatusCode != 429 && resp.StatusCode != 503 {
				return nil, last
			}
		} else {
			last = err
		}
		time.Sleep(time.Duration(i+1) * time.Second)
	}
	return nil, last
}

func (s *KaraokeService) downloadIfNeeded(remote, local string, expected int64) error {
	if st, err := os.Stat(local); err == nil && st.Size() > 0 && (expected <= 0 || st.Size() == expected) {
		return nil
	}
	req, _ := http.NewRequest(http.MethodGet, remote, nil)
	req.Header.Set("User-Agent", "singerOS/0.2")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("audio download HTTP %d", resp.StatusCode)
	}
	tmp := local + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, 128<<20))
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if expected > 0 && n != expected {
		_ = os.Remove(tmp)
		return fmt.Errorf("audio size mismatch: got %d expected %d", n, expected)
	}
	return os.Rename(tmp, local)
}

func parseVTT(data []byte) ([]KaraokeCue, error) {
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	var cues []KaraokeCue
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line == "WEBVTT" || strings.HasPrefix(line, "NOTE") {
			continue
		}
		if !strings.Contains(line, "-->") {
			if _, err := strconv.Atoi(line); err == nil && sc.Scan() {
				line = strings.TrimSpace(sc.Text())
			} else {
				continue
			}
		}
		if !strings.Contains(line, "-->") {
			continue
		}
		parts := strings.SplitN(line, "-->", 2)
		start, err1 := parseCueTime(strings.TrimSpace(parts[0]))
		endField := strings.Fields(strings.TrimSpace(parts[1]))
		if len(endField) == 0 {
			continue
		}
		end, err2 := parseCueTime(endField[0])
		if err1 != nil || err2 != nil {
			continue
		}
		var textLines []string
		for sc.Scan() {
			t := strings.TrimSpace(sc.Text())
			if t == "" {
				break
			}
			textLines = append(textLines, t)
		}
		text := strings.TrimSpace(strings.Join(textLines, " "))
		if text != "" {
			cues = append(cues, KaraokeCue{Start: start, End: end, Text: text})
		}
	}
	return cues, sc.Err()
}

func parseCueTime(v string) (float64, error) {
	v = strings.ReplaceAll(v, ",", ".")
	p := strings.Split(v, ":")
	if len(p) == 2 {
		m, e1 := strconv.ParseFloat(p[0], 64)
		s, e2 := strconv.ParseFloat(p[1], 64)
		if e1 != nil || e2 != nil {
			return 0, errors.New("bad cue time")
		}
		return m*60 + s, nil
	}
	if len(p) == 3 {
		h, e1 := strconv.ParseFloat(p[0], 64)
		m, e2 := strconv.ParseFloat(p[1], 64)
		s, e3 := strconv.ParseFloat(p[2], 64)
		if e1 != nil || e2 != nil || e3 != nil {
			return 0, errors.New("bad cue time")
		}
		return h*3600 + m*60 + s, nil
	}
	return 0, errors.New("bad cue time")
}

func (s *KaraokeService) asset(songID, mode string) (string, bool) {
	c := s.snapshot()
	for _, song := range c.Songs {
		if song.ID != songID {
			continue
		}
		tr, ok := song.Tracks[mode]
		if !ok {
			return "", false
		}
		ext := strings.ToLower(filepath.Ext(strings.TrimPrefix(tr.SourceFile, "File:")))
		p := filepath.Join(s.assets, songID, mode+ext)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

type karaokeRecSession struct {
	ID, SongID, SongTitle, Mode, MIME string
	Started                           time.Time
	Seq                               int
	Bytes                             int64
}

type KaraokeRecording struct {
	ID                string `json:"id"`
	File              string `json:"file"`
	SongID            string `json:"song_id"`
	SongTitle         string `json:"song_title"`
	Mode              string `json:"mode"`
	DurationSeconds   int    `json:"duration_seconds"`
	CreatedAtShanghai string `json:"created_at_shanghai"`
	StartedAtShanghai string `json:"started_at_shanghai"`
	Bytes             int64  `json:"bytes"`
	URL               string `json:"url"`
}

type KaraokeRecordingStore struct {
	mu       sync.Mutex
	dir      string
	sessions map[string]*karaokeRecSession
}

func newKaraokeRecordingStore(dataDir string) *KaraokeRecordingStore {
	dir := filepath.Join(dataDir, "karaoke")
	_ = os.MkdirAll(dir, 0o700)
	return &KaraokeRecordingStore{dir: dir, sessions: map[string]*karaokeRecSession{}}
}

func (s *KaraokeRecordingStore) start(songID, title, mode, mimeType string) (*karaokeRecSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newID()
	x := &karaokeRecSession{ID: id, SongID: songID, SongTitle: title, Mode: mode, MIME: mimeType, Started: time.Now().In(shanghaiLocation)}
	f, err := os.OpenFile(filepath.Join(s.dir, id+".part"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	s.sessions[id] = x
	return x, nil
}

func (s *KaraokeRecordingStore) append(id string, seq int, r io.Reader) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, ok := s.sessions[id]
	if !ok {
		return 0, errors.New("recording session not active")
	}
	if seq != x.Seq {
		return 0, fmt.Errorf("sequence mismatch: expected %d", x.Seq)
	}
	f, err := os.OpenFile(filepath.Join(s.dir, id+".part"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(r, maxChunkBytes+1))
	_ = f.Close()
	if err != nil {
		return n, err
	}
	if n > maxChunkBytes {
		return n, errors.New("chunk exceeds 16 MiB")
	}
	x.Seq++
	x.Bytes += n
	return n, nil
}

var badFilenameChars = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]+`)

func safeSongFilename(v string) string {
	v = strings.TrimSpace(badFilenameChars.ReplaceAllString(v, "_"))
	v = strings.Trim(v, ". ")
	if v == "" {
		return "karaoke"
	}
	r := []rune(v)
	if len(r) > 80 {
		v = string(r[:80])
	}
	return v
}

func numberFromAny(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	default:
		return 0
	}
}

func (s *KaraokeRecordingStore) finalize(id string, meta map[string]any) (KaraokeRecording, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, ok := s.sessions[id]
	if !ok {
		return KaraokeRecording{}, errors.New("recording session not active")
	}
	created := time.Now().In(shanghaiLocation)
	duration := int(math.Round(numberFromAny(meta["duration_seconds"])))
	if duration <= 0 || duration > 6*3600 {
		duration = int(math.Round(created.Sub(x.Started).Seconds()))
	}
	ext := extForMIME(x.MIME)
	base := safeSongFilename(x.SongTitle) + "_" + created.Format("20060102_150405")
	finalName := base + ext
	if _, err := os.Stat(filepath.Join(s.dir, finalName)); err == nil {
		finalName = base + "_" + id[:8] + ext
	}
	if err := os.Rename(filepath.Join(s.dir, id+".part"), filepath.Join(s.dir, finalName)); err != nil {
		return KaraokeRecording{}, err
	}
	rec := KaraokeRecording{
		ID: id, File: finalName, SongID: x.SongID, SongTitle: x.SongTitle, Mode: x.Mode,
		DurationSeconds:   duration,
		CreatedAtShanghai: created.Format("2006-01-02 15:04:05"),
		StartedAtShanghai: x.Started.Format("2006-01-02 15:04:05"),
		Bytes:             x.Bytes, URL: "/singeros/api/karaoke/recordings/" + id + "/audio",
	}
	b, _ := json.MarshalIndent(rec, "", "  ")
	_ = os.WriteFile(filepath.Join(s.dir, id+".json"), b, 0o600)
	delete(s.sessions, id)
	return rec, nil
}

func (s *KaraokeRecordingStore) list() []KaraokeRecording {
	entries, _ := os.ReadDir(s.dir)
	var out []KaraokeRecording
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var r KaraokeRecording
		if json.Unmarshal(b, &r) == nil && r.ID != "" {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAtShanghai > out[j].CreatedAtShanghai })
	return out
}

func (s *KaraokeRecordingStore) path(id string) (string, bool) {
	if !safeID.MatchString(id) {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if err != nil {
		return "", false
	}
	var r KaraokeRecording
	if json.Unmarshal(b, &r) != nil || r.File == "" {
		return "", false
	}
	p := filepath.Join(s.dir, filepath.Base(r.File))
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p, true
	}
	return "", false
}

func registerKaraokeRoutes(r *gin.Engine, dataDir string, secureHeaders func(*gin.Context)) {
	service := newKaraokeService(dataDir)
	recordings := newKaraokeRecordingStore(dataDir)

	r.GET("/singeros/karaoke", func(c *gin.Context) { c.Redirect(http.StatusTemporaryRedirect, "/singeros/karaoke/") })

	r.GET("/singeros/karaoke/", func(c *gin.Context) {
		secureHeaders(c)
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(karaokePage))
	})

	api := r.Group("/singeros/api/karaoke")
	api.GET("/catalog", func(c *gin.Context) { c.JSON(http.StatusOK, service.snapshot()) })
	api.POST("/crawl", func(c *gin.Context) {
		err := service.Crawl("manual")
		status := service.snapshot()
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"ok": false, "error": err.Error(), "catalog": status})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "catalog": status})
	})
	api.GET("/assets/:song/:mode", func(c *gin.Context) {
		p, ok := service.asset(c.Param("song"), c.Param("mode"))
		if !ok {
			c.Status(http.StatusNotFound)
			return
		}
		c.File(p)
	})
	api.POST("/recordings/start", func(c *gin.Context) {
		var req struct {
			SongID    string `json:"song_id"`
			SongTitle string `json:"song_title"`
			Mode      string `json:"mode"`
			MIME      string `json:"mime"`
		}
		if c.ShouldBindJSON(&req) != nil || req.SongID == "" || req.SongTitle == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "song_id and song_title required"})
			return
		}
		x, err := recordings.start(req.SongID, req.SongTitle, req.Mode, req.MIME)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"id": x.ID, "started_at_shanghai": x.Started.Format("2006-01-02 15:04:05")})
	})
	api.POST("/recordings/:id/chunk", func(c *gin.Context) {
		seq, err := strconv.Atoi(c.Query("seq"))
		if err != nil || seq < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid seq"})
			return
		}
		n, err := recordings.append(c.Param("id"), seq, c.Request.Body)
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"seq": seq, "bytes": n})
	})
	api.POST("/recordings/:id/finalize", func(c *gin.Context) {
		var meta map[string]any
		_ = c.ShouldBindJSON(&meta)
		rec, err := recordings.finalize(c.Param("id"), meta)
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, rec)
	})
	api.GET("/recordings", func(c *gin.Context) { c.JSON(http.StatusOK, recordings.list()) })
	api.GET("/recordings/:id/audio", func(c *gin.Context) {
		p, ok := recordings.path(c.Param("id"))
		if !ok {
			c.Status(http.StatusNotFound)
			return
		}
		c.File(p)
	})
}
