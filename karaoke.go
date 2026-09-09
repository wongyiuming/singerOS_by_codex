package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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
	SourceType     string       `json:"source_type"`
	SourceFile     string       `json:"source_file"`
	SourcePage     string       `json:"source_page,omitempty"`
	License        string       `json:"license,omitempty"`
	Language       string       `json:"language"`
	LyricsLanguage string       `json:"lyrics_language,omitempty"`
	LyricsOffset   float64      `json:"lyrics_offset_seconds,omitempty"`
	Duration       float64      `json:"duration_seconds"`
	Bytes          int64        `json:"bytes"`
	URL            string       `json:"url"`
	Lyrics         []KaraokeCue `json:"lyrics"`
	SyncedAt       string       `json:"synced_at_shanghai,omitempty"`
}

type KaraokeSong struct {
	ID             string                  `json:"id"`
	Title          string                  `json:"title"`
	Artist         string                  `json:"artist"`
	Album          string                  `json:"album,omitempty"`
	Year           int                     `json:"year,omitempty"`
	TrackNo        int                     `json:"track_no,omitempty"`
	Version        string                  `json:"version"`
	Language       string                  `json:"language"`
	LyricsLanguage string                  `json:"lyrics_language,omitempty"`
	LyricsVersion  string                  `json:"lyrics_version,omitempty"`
	Lyrics         []KaraokeCue            `json:"lyrics,omitempty"`
	Tracks         map[string]KaraokeTrack `json:"tracks"`
}

type KaraokeAlbum struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Year     int    `json:"year"`
	Language string `json:"language"`
	Order    int    `json:"order"`
}

type KaraokeCatalog struct {
	Provider  string         `json:"provider"`
	Language  string         `json:"language"`
	UpdatedAt string         `json:"updated_at_shanghai,omitempty"`
	Albums    []KaraokeAlbum `json:"albums"`
	Songs     []KaraokeSong  `json:"songs"`
}

var curatedAlbums = []KaraokeAlbum{
	{ID: "faith-hope-love-1992", Title: "信望愛", Year: 1992, Language: "粵語", Order: 1},
	{ID: "borrow-your-love-1993", Title: "借借你的愛", Year: 1993, Language: "粵語", Order: 2},
	{ID: "not-an-angel-1994", Title: "明明不是天使", Year: 1994, Language: "國語", Order: 3},
	{ID: "beautiful-night-1995", Title: "愈夜愈美麗", Year: 1995, Language: "粵語", Order: 4},
	{ID: "feng-yue-bao-jian-1997", Title: "風月寶鑑", Year: 1997, Language: "粵語", Order: 5},
	{ID: "people-mountain-people-sea-1997", Title: "人山人海", Year: 1997, Language: "粵語", Order: 6},
	{ID: "broad-daylight-2000", Title: "光天化日", Year: 2000, Language: "粵語", Order: 7},
	{ID: "my-21st-century-2003", Title: "我的廿一世紀", Year: 2003, Language: "粵語", Order: 8},
	{ID: "tomorrows-song-2004", Title: "明日之歌", Year: 2004, Language: "粵語", Order: 9},
	{ID: "five-cakes-two-fish-1996", Title: "5餅2魚", Year: 1996, Language: "粵語", Order: 10},
	{ID: "off-the-mountain-2011", Title: "拂了一身還滿", Year: 2011, Language: "粵語/國語", Order: 11},
	{ID: "under-lion-rock-2014", Title: "太平山下", Year: 2014, Language: "粵語", Order: 12},
	{ID: "singles-film", Title: "單曲 / 電影歌曲", Year: 1998, Language: "粵語/國語", Order: 13},
}

type KaraokeService struct {
	mu      sync.RWMutex
	root    string
	assets  string
	catalog KaraokeCatalog
}

func newKaraokeService(dataDir string) *KaraokeService {
	root := filepath.Join(filepath.Dir(dataDir), "karaoke")
	assets := filepath.Join(root, "assets")
	_ = os.MkdirAll(assets, 0o755)
	s := &KaraokeService{
		root:   root,
		assets: assets,
		catalog: KaraokeCatalog{
			Provider: "manual-curated-local",
			Language: "粵語/國語",
			Albums:   append([]KaraokeAlbum(nil), curatedAlbums...),
			Songs:    []KaraokeSong{},
		},
	}
	s.load()
	return s
}

func (s *KaraokeService) load() {
	b, err := os.ReadFile(filepath.Join(s.root, "catalog.json"))
	if err != nil {
		return
	}
	var c KaraokeCatalog
	if json.Unmarshal(b, &c) != nil || c.Provider != "manual-curated-local" {
		return
	}
	c.Language = "粵語/國語"
	c.Albums = append([]KaraokeAlbum(nil), curatedAlbums...)
	valid := make([]KaraokeSong, 0, len(c.Songs))
	for _, song := range c.Songs {
		if s.validLocalSong(song) {
			valid = append(valid, song)
		}
	}
	c.Songs = valid
	s.mu.Lock()
	s.catalog = c
	s.mu.Unlock()
}

func (s *KaraokeService) validLocalSong(song KaraokeSong) bool {
	if song.ID == "" || song.Title == "" || song.Artist == "" || (song.Language != "粵語" && song.Language != "國語") {
		return false
	}
	validTracks := 0
	for _, mode := range []string{"original", "accompaniment"} {
		track, ok := song.Tracks[mode]
		if !ok {
			continue
		}
		if track.Mode != mode || track.SourceType != "local" || (track.Language != "粵語" && track.Language != "國語") || track.SourceFile == "" {
			return false
		}
		ext := strings.ToLower(filepath.Ext(track.SourceFile))
		if ext == "" || len(ext) > 8 {
			return false
		}
		p := filepath.Join(s.assets, song.ID, mode+ext)
		st, err := os.Stat(p)
		if err != nil || st.IsDir() || st.Size() <= 0 {
			return false
		}
		validTracks++
	}
	return validTracks > 0
}

func (s *KaraokeService) snapshot() KaraokeCatalog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, _ := json.Marshal(s.catalog)
	var out KaraokeCatalog
	_ = json.Unmarshal(b, &out)
	return out
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

	api.GET("/assets/:song/:mode", func(c *gin.Context) {
		p, ok := service.asset(c.Param("song"), c.Param("mode"))
		if !ok {
			c.Status(http.StatusNotFound)
			return
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".ogg", ".opus":
			c.Header("Content-Type", "audio/ogg")
		case ".webm":
			c.Header("Content-Type", "audio/webm")
		case ".m4a", ".mp4":
			c.Header("Content-Type", "audio/mp4")
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
