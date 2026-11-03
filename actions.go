package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/audio"

	"wavealign/internal/align"
	"wavealign/internal/media"
	"wavealign/internal/playback"
)

// decodeSlots bounds how many files decode at once.
var decodeSlots = make(chan struct{}, 3)

// post runs fn on the main thread and redraws.
func (a *app) post(fn func()) {
	switch {
	case a.win != nil:
		a.win.Update(fn)
	case a.queue != nil:
		a.queue <- fn // tests run them
	default:
		fn()
	}
}

func (a *app) setStatus(msg string, ok bool) {
	a.status, a.statusOK = msg, ok
}

func (a *app) newID() int {
	a.nextID++
	return a.nextID
}

// addFiles adds dropped or chosen files: audio files to the external audio,
// the rest as videos.
func (a *app) addFiles(paths []string) {
	var au, vi []string
	for _, p := range paths {
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			continue
		}
		if isAudioFile(p) {
			au = append(au, p)
		} else {
			vi = append(vi, p)
		}
	}
	a.addAudios(au, nil)
	a.addClips(vi, nil)
}

// addAudios appends files to the external audio; saved, when given, restores
// their streams.
func (a *app) addAudios(paths []string, saved []projectAudio) {
	for i, p := range paths {
		it := &audioItem{source: source{id: a.newID(), path: p, loading: true, stream: -1}}
		want := -1
		if saved != nil {
			want = saved[i].Stream
		}
		a.audios = append(a.audios, it)
		a.load(&it.source, want, nil)
	}
	a.layoutAudios()
	a.sourcesChanged()
}

// addClips adds videos; saved, when given, restores their streams and places.
func (a *app) addClips(paths []string, saved []projectClip) {
	for i, p := range paths {
		c := &clip{source: source{id: a.newID(), path: p, loading: true, stream: -1}}
		want := -1
		if saved != nil {
			s := saved[i]
			want, c.channel, c.offset, c.aligned = s.Stream, s.Channel, s.Offset, s.Aligned
			c.autoOffset, c.confidence, c.driftPPM = s.AutoOffset, s.Confidence, s.DriftPPM
		} else if end := a.extEnd(); len(a.clips) > 0 {
			// Until matched, place it after the last clip.
			last := 0.0
			for _, o := range a.clips {
				last = max(last, o.end())
			}
			c.offset = min(last, end)
		}
		a.clips = append(a.clips, c)
		if a.selected == 0 {
			a.selected = c.id
		}
		a.load(&c.source, want, func(info *media.Info) {
			if st, ok := c.audioStream(); ok {
				c.lead = math.Max(0, st.StartTime-info.StartTime)
			}
			if c.channel > c.channels() {
				c.channel = 0
			}
		})
	}
	a.sourcesChanged()
}

// load probes and decodes a source on a goroutine. stream, if not -1, is
// the absolute index of the stream to use; probed runs on the main thread
// once the file is probed.
func (a *app) load(s *source, stream int, probed func(*media.Info)) {
	path := s.path
	go func() {
		info, err := media.Probe(context.Background(), path)
		a.post(func() {
			if err != nil {
				s.loading, s.err = false, err.Error()
				return
			}
			s.info, s.streams = info, info.Audio()
			s.stream = 0
			for i, st := range s.streams {
				if st.Index == stream {
					s.stream = i
				}
			}
			if len(s.streams) == 0 {
				s.stream = -1
				s.loading, s.err = false, "没有音轨，只能手动对齐"
			}
			if probed != nil {
				probed(info)
			}
			a.layoutAudios()
			if s.stream >= 0 {
				a.decode(s)
			}
		})
	}()
}

// decode decodes the source's chosen stream into the cache, on a goroutine.
func (a *app) decode(s *source) {
	st, ok := s.audioStream()
	if !ok {
		return
	}
	path, dur, id := s.path, s.duration(), s.id
	s.loading, s.progress, s.err = true, 0, ""
	go func() {
		decodeSlots <- struct{}{}
		defer func() { <-decodeSlots }()
		last := time.Now()
		pcm, peaks, err := media.DecodePCM(context.Background(), path, st.Index, dur, func(f float64) {
			if time.Since(last) > 150*time.Millisecond {
				last = time.Now()
				a.post(func() { s.progress = f })
			}
		})
		a.post(func() {
			if cur, ok := s.audioStream(); !a.has(id) || !ok || cur.Index != st.Index {
				// Removed, or another stream chosen meanwhile.
				pcm.Close()
				return
			}
			s.loading = false
			if err != nil {
				s.err = err.Error()
				return
			}
			old := s.pcm
			s.pcm, s.peaks = pcm, peaks
			a.layoutAudios()
			a.sourcesChanged()
			if old != nil {
				old.Close()
			}
		})
	}()
}

// has reports whether a source with the id is still in the project.
func (a *app) has(id int) bool {
	for _, it := range a.audios {
		if it.id == id {
			return true
		}
	}
	return a.clipByID(id) != nil
}

func (a *app) clipByID(id int) *clip {
	for _, c := range a.clips {
		if c.id == id {
			return c
		}
	}
	return nil
}

func (a *app) selectedClip() *clip { return a.clipByID(a.selected) }

// layoutAudios places the external audio files one after another.
func (a *app) layoutAudios() {
	t := 0.0
	for _, it := range a.audios {
		it.start = t
		if it.pcm != nil {
			t += it.pcm.Seconds()
		} else {
			t += it.duration()
		}
	}
}

// extEnd is where the external audio ends.
func (a *app) extEnd() float64 {
	if n := len(a.audios); n > 0 {
		last := a.audios[n-1]
		d := last.duration()
		if last.pcm != nil {
			d = last.pcm.Seconds()
		}
		return last.start + d
	}
	return 0
}

// timeRange is the span the timeline shows and the mix plays.
func (a *app) timeRange() (float64, float64) {
	lo, hi := 0.0, math.Max(a.extEnd(), 10)
	for _, c := range a.clips {
		lo = math.Min(lo, c.offset)
		hi = math.Max(hi, c.end())
	}
	return lo, hi
}

// sourcesChanged drops what depended on the files and their order.
func (a *app) sourcesChanged() {
	if a.playing {
		a.play() // anew, with the new files
	}
}

func (a *app) removeAudio(id int) {
	a.stopForEdit()
	for i, it := range a.audios {
		if it.id == id {
			it.pcm.Close()
			a.audios = append(a.audios[:i], a.audios[i+1:]...)
			break
		}
	}
	a.layoutAudios()
	a.extKey = ""
}

func (a *app) moveAudio(i, to int) {
	if i < 0 || i >= len(a.audios) || to < 0 || to >= len(a.audios) || i == to {
		return
	}
	a.stopForEdit()
	it := a.audios[i]
	a.audios = append(a.audios[:i], a.audios[i+1:]...)
	a.audios = append(a.audios[:to], append([]*audioItem{it}, a.audios[to:]...)...)
	a.layoutAudios()
	a.extKey = ""
}

// reorderAudios moves the rows of the list before row to, as ListState.Reorder asks.
func (a *app) reorderAudios(rows []int, to int) {
	a.stopForEdit()
	var moved, rest []*audioItem
	at := -1
	for i, it := range a.audios {
		if i == to {
			at = len(rest)
		}
		if contains(rows, i) {
			moved = append(moved, it)
		} else {
			rest = append(rest, it)
		}
	}
	if at < 0 {
		at = len(rest)
	}
	a.audios = append(append(append([]*audioItem{}, rest[:at]...), moved...), rest[at:]...)
	a.layoutAudios()
	a.extKey = ""
}

func contains(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func (a *app) removeClip(id int) {
	a.stopForEdit()
	for i, c := range a.clips {
		if c.id == id {
			c.pcm.Close()
			a.clips = append(a.clips[:i], a.clips[i+1:]...)
			break
		}
	}
	if a.selected == id {
		a.selected = 0
		if len(a.clips) > 0 {
			a.selected = a.clips[0].id
		}
	}
}

// stopForEdit stops playback before files it reads go away.
func (a *app) stopForEdit() {
	if a.playing {
		a.pause()
	}
	a.closePlayer()
}

// setStream chooses another audio stream of a source.
func (a *app) setStream(s *source, i int) {
	if i == s.stream || i < 0 || i >= len(s.streams) {
		return
	}
	a.stopForEdit()
	s.stream = i
	a.extKey = ""
	a.decode(s)
}

// ---- Matching ----

// alignClips matches the clips with the ids against the external audio.
func (a *app) alignClips(ids []int) {
	if len(a.audios) == 0 {
		a.setStatus("请先添加匹配源 1（外部音频）", false)
		return
	}
	for _, it := range a.audios {
		if it.loading {
			a.setStatus("外部音频仍在解码，请稍候", false)
			return
		}
	}
	type extFile struct {
		path    string
		stream  int
		start   float64
		channel int
	}
	var files []extFile
	var key strings.Builder
	for _, it := range a.audios {
		st, ok := it.audioStream()
		if !ok || it.pcm == nil {
			continue
		}
		ch := a.channel
		if ch > st.Channels {
			ch = 0
		}
		files = append(files, extFile{it.path, st.Index, it.start, ch})
		fmt.Fprintf(&key, "%s|%d|%.6f|%d;", it.path, st.Index, it.start, ch)
	}
	if len(files) == 0 {
		a.setStatus("外部音频没有可用的音轨", false)
		return
	}
	extLen := int(math.Ceil(a.extEnd() * media.AnalysisRate))
	cached := a.extData
	if a.extKey != key.String() {
		cached = nil
	}
	type job struct {
		c       *clip
		path    string
		stream  int
		channel int
		lead    float64
	}
	var jobs []job
	for _, id := range ids {
		c := a.clipByID(id)
		if c == nil || c.aligning {
			continue
		}
		st, ok := c.audioStream()
		if !ok {
			continue
		}
		c.aligning, c.alignProg, c.err = true, 0, ""
		jobs = append(jobs, job{c, c.path, st.Index, c.channel, c.lead})
	}
	if len(jobs) == 0 {
		return
	}
	a.setStatus(fmt.Sprintf("正在匹配 %d 个视频…", len(jobs)), true)
	extKey := key.String()
	go func() {
		ctx := context.Background()
		ext := cached
		if ext == nil {
			ext = make([]float32, extLen)
			for _, f := range files {
				x, err := media.DecodeMono(ctx, f.path, f.stream, f.channel, media.AnalysisRate)
				if err != nil {
					a.post(func() {
						for _, j := range jobs {
							j.c.aligning = false
						}
						a.setStatus("解码外部音频失败: "+err.Error(), false)
					})
					return
				}
				at := int(math.Round(f.start * media.AnalysisRate))
				if at+len(x) > len(ext) {
					ext = append(ext, make([]float32, at+len(x)-len(ext))...)
				}
				copy(ext[at:], x)
			}
			a.post(func() { a.extKey, a.extData = extKey, ext })
		}
		ok := 0
		for _, j := range jobs {
			q, err := media.DecodeMono(ctx, j.path, j.stream, j.channel, media.AnalysisRate)
			var res align.Result
			if err == nil {
				last := time.Now()
				res, err = align.Find(ext, q, align.Options{Rate: media.AnalysisRate, Progress: func(f float64) {
					if time.Since(last) > 100*time.Millisecond {
						last = time.Now()
						a.post(func() { j.c.alignProg = f })
					}
				}})
			}
			if err == nil {
				ok++
			}
			a.post(func() {
				c := j.c
				c.aligning = false
				if err != nil {
					c.err = "匹配失败: " + err.Error()
					return
				}
				c.offset = res.Lag - j.lead
				c.autoOffset = c.offset
				c.aligned = true
				c.confidence = res.Confidence
				c.driftPPM = res.Slope * 1e6
				a.clipMoved()
			})
		}
		a.post(func() {
			a.setStatus(fmt.Sprintf("匹配完成：%d/%d 个视频成功", ok, len(jobs)), ok == len(jobs))
			if c := a.selectedClip(); c != nil {
				a.tl.reveal(c.offset, c.end())
			}
		})
	}()
}

// ---- Playback ----

func (a *app) params() playback.Params {
	return playback.Params{Mode: playback.Mode(a.monitor), GainA: float32(a.gainA), GainB: float32(a.gainB)}
}

// segmentsB places the sound of the clips heard: the selected one, or all.
func (a *app) segmentsB(origin float64) []playback.Segment {
	var out []playback.Segment
	for _, c := range a.clips {
		if c.pcm == nil || a.onlySel && a.selected != 0 && c.id != a.selected {
			continue
		}
		out = append(out, playback.Segment{PCM: c.pcm, Start: int64(math.Round((c.offset + c.lead - origin) * playback.Rate))})
	}
	return out
}

func (a *app) play() {
	a.closePlayer()
	lo, hi := a.timeRange()
	var segA []playback.Segment
	for _, it := range a.audios {
		if it.pcm != nil {
			segA = append(segA, playback.Segment{PCM: it.pcm, Start: int64(math.Round((it.start - lo) * playback.Rate))})
		}
	}
	if a.playhead >= hi || a.playhead < lo {
		a.playhead = lo
	}
	a.origin = lo
	a.mix = playback.NewMix(segA, a.segmentsB(lo), int64((hi-lo)*playback.Rate), a.params())
	a.player = audio.NewPlayer(a.mix)
	a.player.Seek(secs(a.playhead - lo))
	a.player.Play()
	a.playing = true
	a.clock.Store(&clock{player: a.player, origin: lo})
	if err := audio.Err(); err != nil {
		a.setStatus("音频输出不可用: "+err.Error(), false)
	}
}

func (a *app) pause() {
	if a.player != nil {
		a.playhead = a.origin + a.player.Position().Seconds()
		a.player.Pause()
	}
	a.playing = false
	a.clock.Store(nil)
}

func (a *app) closePlayer() {
	if a.player != nil {
		a.player.Close()
		a.player, a.mix = nil, nil
	}
	a.playing = false
	a.clock.Store(nil)
}

func (a *app) togglePlay() {
	if a.playing {
		a.pause()
	} else {
		a.play()
	}
}

func (a *app) seek(t float64) {
	lo, hi := a.timeRange()
	a.playhead = math.Max(lo, math.Min(t, hi))
	if a.playing && a.player != nil {
		a.player.Seek(secs(a.playhead - a.origin))
	}
	a.prev.seeked()
}

// tick follows the player as it plays, from the view.
func (a *app) tick() {
	if !a.playing || a.player == nil {
		return
	}
	a.playhead = a.origin + a.player.Position().Seconds()
	if !a.player.Playing() {
		a.pause()
	}
}

// monitorChanged applies the mix settings at once, rather than after what
// the player decoded ahead.
func (a *app) monitorChanged() {
	if a.mix == nil {
		return
	}
	a.mix.SetParams(a.params())
	a.mix.SetB(a.segmentsB(a.origin))
	if a.playing {
		a.player.Seek(a.player.Position())
	}
}

// clipMoved follows a clip's new place in what plays.
func (a *app) clipMoved() {
	lo, hi := a.timeRange()
	if a.mix != nil && (lo < a.origin || int64((hi-a.origin)*playback.Rate) > a.mix.Len()) {
		// The clip went past the mix's ends: make it anew.
		if a.playing {
			a.play()
			return
		}
		a.closePlayer()
	}
	a.monitorChanged()
	a.prev.seeked()
}

// nudge moves the selected clip by d seconds.
func (a *app) nudge(d float64) {
	if c := a.selectedClip(); c != nil {
		c.offset += d
		a.clipMoved()
	}
}

func secs(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// ---- Export ----

func (a *app) exportTargets() []*clip {
	var out []*clip
	for _, c := range a.clips {
		if a.exportScope == 1 && c.id != a.selected {
			continue
		}
		if c.info == nil {
			continue
		}
		if _, ok := c.info.Video(); !ok {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (a *app) startExport() {
	targets := a.exportTargets()
	if len(targets) == 0 {
		a.setStatus("没有可导出的视频", false)
		return
	}
	var inputs []media.AudioInput
	rate, layout := 0, ""
	sameRate := true
	for i, it := range a.audios {
		st, ok := it.audioStream()
		if !ok || it.pcm == nil {
			a.setStatus("外部音频尚未就绪", false)
			return
		}
		inputs = append(inputs, media.AudioInput{Path: it.path, Stream: st.Index})
		if i == 0 {
			rate = st.SampleRate
			layout = st.Layout
			if layout == "" {
				layout = strconv.Itoa(st.Channels) + "c"
			}
		} else if st.SampleRate != rate {
			sameRate = false
		}
	}
	if len(inputs) == 0 {
		a.setStatus("请先添加外部音频", false)
		return
	}
	if !sameRate || rate < 8000 {
		rate = 48000
	}
	if r, err := strconv.Atoi(a.cfg.Rate); err == nil {
		rate = r
	}
	switch a.cfg.Layout {
	case 1:
		layout = "stereo"
	case 2:
		layout = "mono"
	}
	codec := []string{media.CodecAAC, media.CodecPCM, media.CodecFLAC}[a.cfg.Codec]
	type job struct {
		c   *clip
		opt media.ExportOptions
	}
	var jobs []job
	for _, c := range targets {
		ext := filepath.Ext(c.path)
		if a.cfg.Container > 0 {
			ext = []string{"", ".mp4", ".mov", ".mkv"}[a.cfg.Container]
		}
		ext = media.OutputExt(ext, codec)
		dir := a.cfg.OutDir
		if dir == "" {
			dir = filepath.Dir(c.path)
		}
		base := strings.TrimSuffix(filepath.Base(c.path), filepath.Ext(c.path)) + a.cfg.Suffix
		out := uniquePath(filepath.Join(dir, base+ext), c.path)
		c.exporting, c.exportProg, c.exported, c.exportErr = true, 0, "", ""
		jobs = append(jobs, job{c, media.ExportOptions{
			Video: c.path, Output: out, Inputs: inputs, Offset: c.offset, Duration: c.info.Duration,
			SampleRate: rate, Layout: layout, Codec: codec, Bitrate: "320k", KeepOriginal: a.cfg.KeepOrig,
		}})
	}
	a.cfg.save()
	ctx, cancel := context.WithCancel(context.Background())
	a.exporting, a.exportCancel = true, cancel
	a.setStatus(fmt.Sprintf("正在导出 %d 个视频（视频流直接复制，不重新编码）…", len(jobs)), true)
	hasWindow := a.win != nil
	go func() {
		if hasWindow {
			defer mygo.Power.KeepAwake("导出视频", false)()
		}
		ok := 0
		for _, j := range jobs {
			if ctx.Err() != nil {
				break
			}
			last := time.Now()
			j.opt.Progress = func(f float64) {
				if time.Since(last) > 150*time.Millisecond || f == 1 {
					last = time.Now()
					a.post(func() { j.c.exportProg = f })
				}
			}
			err := media.Export(ctx, j.opt)
			if err == nil {
				ok++
			} else {
				os.Remove(j.opt.Output)
			}
			a.post(func() {
				j.c.exporting = false
				if err != nil {
					j.c.exportErr = err.Error()
				} else {
					j.c.exported = j.opt.Output
				}
			})
		}
		a.post(func() {
			for _, j := range jobs {
				j.c.exporting = false
			}
			a.exporting, a.exportCancel = false, nil
			if ctx.Err() != nil {
				a.setStatus("导出已取消", false)
			} else {
				a.setStatus(fmt.Sprintf("导出完成：%d/%d 个文件", ok, len(jobs)), ok == len(jobs))
				if ok == len(jobs) {
					a.exportOpen = false
				}
			}
		})
	}()
}

// uniquePath returns p, or p with a number added, so that it names no file
// that exists, nor src.
func uniquePath(p, src string) string {
	ext := filepath.Ext(p)
	stem := strings.TrimSuffix(p, ext)
	for i := 2; ; i++ {
		if _, err := os.Stat(p); err != nil && !strings.EqualFold(p, src) {
			return p
		}
		p = fmt.Sprintf("%s (%d)%s", stem, i, ext)
	}
}

// ---- Files and dialogs ----

var (
	audioFilter = mygo.FileFilter{Name: "音频", Extensions: []string{"wav", "mp3", "flac", "m4a", "aac", "ogg", "opus", "aif", "aiff", "wma", "w64", "caf"}}
	videoFilter = mygo.FileFilter{Name: "视频", Extensions: []string{"mp4", "mov", "m4v", "mkv", "avi", "mxf", "mts", "m2ts", "webm", "wmv", "flv", "3gp"}}
	allFilter   = mygo.FileFilter{Name: "所有文件", Extensions: []string{"*"}}
	projFilter  = mygo.FileFilter{Name: "WaveAlign 工程", Extensions: []string{"json"}}
)

func (a *app) chooseAudios() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: "添加外部音频（匹配源 1）", Multiple: true,
			Filters: []mygo.FileFilter{audioFilter, videoFilter, allFilter}})
		if err == nil && len(paths) > 0 {
			a.post(func() { a.addAudios(paths, nil) })
		}
	}()
}

func (a *app) chooseVideos() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: "添加视频（匹配源 2）", Multiple: true,
			Filters: []mygo.FileFilter{videoFilter, allFilter}})
		if err == nil && len(paths) > 0 {
			a.post(func() { a.addClips(paths, nil) })
		}
	}()
}

func (a *app) chooseOutDir() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: "选择导出目录", Directory: true})
		if err == nil && len(paths) == 1 {
			a.post(func() { a.cfg.OutDir = paths[0] })
		}
	}()
}

func (a *app) chooseFFmpegDir() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: "选择 ffmpeg 所在目录", Directory: true})
		if err == nil && len(paths) == 1 {
			a.post(func() {
				a.cfg.FFmpegDir = paths[0]
				a.configureFFmpeg()
			})
		}
	}()
}

// configureFFmpeg finds ffmpeg again and says what it found.
func (a *app) configureFFmpeg() {
	if err := media.Configure(a.cfg.FFmpegDir); err != nil {
		a.ffmpegFound = ""
		a.setStatus(err.Error(), false)
		return
	}
	a.cfg.save()
	mp, _ := media.Tools()
	a.ffmpegFound = mp
}

func (a *app) saveProject() {
	p := project{Version: 1, Channel: a.channel}
	for _, it := range a.audios {
		st, _ := it.audioStream()
		p.Audios = append(p.Audios, projectAudio{Path: it.path, Stream: st.Index})
	}
	for _, c := range a.clips {
		st, _ := c.audioStream()
		p.Clips = append(p.Clips, projectClip{Path: c.path, Stream: st.Index, Channel: c.channel, Offset: c.offset,
			Aligned: c.aligned, AutoOffset: c.autoOffset, Confidence: c.confidence, DriftPPM: c.driftPPM})
	}
	data, _ := json.MarshalIndent(p, "", "  ")
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: a.win, Title: "保存工程", DefaultPath: "project.wavealign.json",
			Filters: []mygo.FileFilter{projFilter}})
		if err != nil || path == "" {
			return
		}
		err = os.WriteFile(path, data, 0o644)
		a.post(func() {
			if err != nil {
				a.setStatus("保存失败: "+err.Error(), false)
			} else {
				a.setStatus("已保存工程 "+filepath.Base(path), true)
			}
		})
	}()
}

func (a *app) openProject() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: "打开工程", Filters: []mygo.FileFilter{projFilter, allFilter}})
		if err != nil || len(paths) != 1 {
			return
		}
		data, err := os.ReadFile(paths[0])
		var p project
		if err == nil {
			err = json.Unmarshal(data, &p)
		}
		a.post(func() {
			if err != nil {
				a.setStatus("无法打开工程: "+err.Error(), false)
				return
			}
			a.loadProject(p)
			a.setStatus("已打开工程 "+filepath.Base(paths[0]), true)
		})
	}()
}

func (a *app) loadProject(p project) {
	a.closePlayer()
	for _, it := range a.audios {
		it.pcm.Close()
	}
	for _, c := range a.clips {
		c.pcm.Close()
	}
	a.audios, a.clips, a.selected, a.extKey, a.extData = nil, nil, 0, "", nil
	a.channel = p.Channel
	var ap, cp []string
	for _, x := range p.Audios {
		ap = append(ap, x.Path)
	}
	for _, x := range p.Clips {
		cp = append(cp, x.Path)
	}
	a.addAudios(ap, p.Audios)
	a.addClips(cp, p.Clips)
	a.tl.fitPending = true
}

// close stops playback and closes the decoded audio.
func (a *app) close() {
	a.closePlayer()
	a.prev.stop()
	for _, it := range a.audios {
		it.pcm.Close()
	}
	for _, c := range a.clips {
		c.pcm.Close()
	}
}

func (a *app) clearCache() {
	keep := map[string]bool{}
	for _, it := range a.audios {
		if it.pcm != nil {
			keep[it.pcm.Path] = true
		}
	}
	for _, c := range a.clips {
		if c.pcm != nil {
			keep[c.pcm.Path] = true
		}
	}
	freed, _ := media.ClearCache(keep)
	a.setStatus(fmt.Sprintf("已清理缓存 %.1f MB（保留当前工程使用的文件）", float64(freed)/1e6), true)
}
