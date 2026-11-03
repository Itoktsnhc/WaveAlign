package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/audio"
	"github.com/egoist/mygo/ui"

	"wavealign/internal/media"
	"wavealign/internal/playback"
	"wavealign/internal/wave"
)

// source is a file whose audio the app decoded: an external audio file, or a
// video.
type source struct {
	id      int
	path    string
	info    *media.Info
	streams []media.Stream // its audio streams
	stream  int            // the one used, an index into streams
	want    int            // the absolute index asked for, until probed; -1 for the first
	pcm     *media.PCM
	peaks   *wave.Peaks

	loading  bool
	progress float64
	err      string
}

func (s *source) name() string { return filepath.Base(s.path) }

func (s *source) ready() bool { return s.pcm != nil }

// audioStream returns the chosen audio stream, if there is one.
func (s *source) audioStream() (media.Stream, bool) {
	if s.stream < 0 || s.stream >= len(s.streams) {
		return media.Stream{}, false
	}
	return s.streams[s.stream], true
}

// channels returns how many channels the chosen stream has.
func (s *source) channels() int {
	if st, ok := s.audioStream(); ok {
		return st.Channels
	}
	return 0
}

// duration is the file's length, decoded or as ffprobe says.
func (s *source) duration() float64 {
	if s.info != nil && s.info.Duration > 0 {
		return s.info.Duration
	}
	if s.pcm != nil {
		return s.pcm.Seconds()
	}
	return 0
}

// audioItem is a file of the external audio (match source 1).
type audioItem struct {
	source
	start float64 // where it starts on the timeline: the files play one after another
}

// clip is a video (match source 2), placed on the timeline.
type clip struct {
	source
	channel int // channel matched: 0 all mixed, else counted from 1

	// offset is the timeline time of the video's start.
	offset float64
	// lead is when its audio stream starts, after the video's start.
	lead float64

	aligned    bool    // offset came from matching (and maybe nudged since)
	manual     bool    // placed on the timeline by hand, unmatched
	autoOffset float64 // what matching found
	confidence float64
	driftPPM   float64
	aligning   bool
	alignProg  float64

	exporting  bool
	exportProg float64
	exported   string // the file written
	exportErr  string
}

func (c *clip) end() float64 { return c.offset + c.duration() }

// placed reports whether the clip is on the timeline: matched, or placed by
// hand. Clips not yet matched wait in the sidebar.
func (c *clip) placed() bool { return c.aligned || c.manual }

// settings last from run to run.
type settings struct {
	FFmpegDir string `json:"ffmpegDir"`
	HWAccel   string `json:"hwaccel"` // auto, cuda, d3d11va, none
	OutDir    string `json:"outDir"`
	Suffix    string `json:"suffix"`
	Container int    `json:"container"` // 0 same as source, 1 MP4, 2 MOV, 3 MKV
	Codec     int    `json:"codec"`     // 0 AAC, 1 PCM, 2 FLAC
	Layout    int    `json:"layout"`    // 0 as the external audio, 1 stereo, 2 mono
	Rate      string `json:"rate"`      // "自动" or a rate
	KeepOrig  bool   `json:"keepOriginal"`
}

func settingsPath() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "WaveAlign", "settings.json")
}

func loadSettings() settings {
	s := settings{HWAccel: "auto", Suffix: "_synced", Rate: "自动", KeepOrig: true}
	if data, err := os.ReadFile(settingsPath()); err == nil {
		json.Unmarshal(data, &s)
	}
	if s.Suffix == "" {
		s.Suffix = "_synced"
	}
	if s.Rate == "" {
		s.Rate = "自动"
	}
	return s
}

func (s settings) save() {
	p := settingsPath()
	os.MkdirAll(filepath.Dir(p), 0o755)
	data, _ := json.MarshalIndent(s, "", "  ")
	os.WriteFile(p, data, 0o644)
}

// clock is the playing player, which goroutines read the playhead from.
type clock struct {
	player *audio.Player
	origin float64
}

// app is the window's state. Its fields change on the main thread only;
// goroutines post their results through win.Update.
type app struct {
	win   *mygo.Window
	queue chan func() // without a window, as in tests: what goroutines post
	theme *ui.Theme
	cfg   settings

	audios  []*audioItem
	clips   []*clip
	nextID  int
	channel int // channel of the external audio matched: 0 mixed, else from 1

	selected int // id of the selected clip, 0 for none

	// The external audio decoded for matching, kept while it stays the same.
	extKey  string
	extData []float32

	// Timeline.
	tl   timeline
	held ui.Modifiers // the modifier keys held

	// Playback.
	playhead float64
	playing  bool
	player   *audio.Player
	mix      *playback.Mix
	origin   float64 // timeline time of the mix's first frame
	clock    atomic.Pointer[clock]
	monitor  int // playback.Mode
	gainA    float64
	gainB    float64
	onlySel  bool // only the selected clip's sound plays

	prev preview

	// Dialogs.
	exportOpen   bool
	exportScope  int // 0 all aligned, 1 selected
	exporting    bool
	exportCancel func()
	settingsOpen bool
	conflictOpen bool
	conflictList []conflict

	projectPath string // where the project was saved or opened from
	saved       string // the project as saved, to tell what changed
	closing     bool   // the save prompt shows
	ffmpegFound string

	status    string
	statusOK  bool
	sidebar   float32
	upper     float32
	audioSel  int
	audioList ui.ListState
}

func newApp() *app {
	a := &app{
		cfg:      loadSettings(),
		gainA:    1,
		gainB:    1,
		onlySel:  true,
		monitor:  int(playback.Both),
		sidebar:  400,
		upper:    470,
		audioSel: -1,
	}
	t := ui.DarkTheme()
	t.Accent = ui.Hex("#4f8cff")
	t.AccentHover = ui.Hex("#6a9dff")
	t.AccentPressed = ui.Hex("#3b74e0")
	t.Spacing = 3.5
	a.theme = t
	a.tl.pxPerSec = 20
	a.prev.init()
	return a
}

var audioExts = map[string]bool{
	".wav": true, ".mp3": true, ".flac": true, ".m4a": true, ".aac": true, ".ogg": true, ".opus": true,
	".aif": true, ".aiff": true, ".wma": true, ".w64": true, ".caf": true, ".ac3": true,
}

func isAudioFile(p string) bool { return audioExts[strings.ToLower(filepath.Ext(p))] }

// project is what Save writes: the files, their streams and the alignment.
type project struct {
	Version int            `json:"version"`
	Channel int            `json:"channel"`
	Audios  []projectAudio `json:"audios"`
	Clips   []projectClip  `json:"clips"`
}

type projectAudio struct {
	Path   string `json:"path"`
	Stream int    `json:"stream"`
}

type projectClip struct {
	Path       string  `json:"path"`
	Stream     int     `json:"stream"`
	Channel    int     `json:"channel"`
	Offset     float64 `json:"offset"`
	Aligned    bool    `json:"aligned"`
	Placed     bool    `json:"placed,omitempty"`
	AutoOffset float64 `json:"autoOffset"`
	Confidence float64 `json:"confidence"`
	DriftPPM   float64 `json:"driftPPM"`
}
