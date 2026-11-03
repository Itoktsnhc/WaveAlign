package main

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"

	"github.com/egoist/mygo/ui"

	"wavealign/internal/media"
)

func (a *app) view(c *ui.Context) {
	c.SetTheme(a.theme)
	t := c.Theme()
	a.tick()
	a.held = c.Modifiers()
	_, a.tl.end = a.timeRange()
	a.tl.clamp()
	if a.playing {
		c.AnimationFrame()
		a.tl.follow(a.playhead)
	}
	if a.tl.fitPending && a.tl.width > 0 && (len(a.audios) > 0 || len(a.clips) > 0) {
		a.tl.fit()
		a.tl.fitPending = false
	}
	a.shortcuts(c)
	a.updatePreview()

	root := ui.Column(c).Fill().Background(t.Background)
	if files := root.DroppedFiles(); files != nil {
		a.addFiles(files)
		a.tl.fitPending = true
	}
	if root.FileDragOver() {
		root.Border(2, t.Accent)
	}
	root.Children(func() {
		a.toolbar(c)
		ui.Divider(c)
		ui.SplitVertical(c, &a.upper, func() {
			ui.Split(c, &a.sidebar, func() { a.sidebarView(c) }, func() { a.previewView(c) }).Fill()
		}, func() {
			a.timelinePanel(c)
		}).Grow(1)
		ui.Divider(c)
		a.statusBar(c)
	})
	a.exportDialog(c)
	a.settingsDialog(c)
	a.conflictDialog(c)
}

// conflictDialog tells, after matching, which videos landed on the same
// stretch of the timeline.
func (a *app) conflictDialog(c *ui.Context) {
	if !a.conflictOpen {
		return
	}
	msg := describeConflicts(a.conflictList) +
		"\n视频只有一条轨道。重叠可能是匹配错误（看置信度、试听确认），也可能这些视频本来就拍摄于同一时段（多机位）。" +
		"请微调、重新对齐或移除其中一个；冲突处在时间轴上以红色标出。"
	switch ui.AlertDialog(c, &a.conflictOpen, fmt.Sprintf("%d 处时间轴冲突", len(a.conflictList)), msg, "知道了", "查看第一处") {
	case 1:
		k := a.conflictList[0]
		a.selectClip(k.b.id)
		a.tl.reveal(math.Min(k.a.offset, k.b.offset), math.Max(k.a.end(), k.b.end()))
		a.seek(k.from)
	}
}

func (a *app) shortcuts(c *ui.Context) {
	if c.Shortcut(0, ui.KeySpace) {
		a.togglePlay()
	}
	if c.Shortcut(ui.Cmd, ui.KeyS) {
		a.saveProject(false, nil)
	}
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyS) {
		a.saveProject(true, nil)
	}
	if c.Shortcut(ui.Cmd, ui.KeyO) {
		a.openProject()
	}
	if c.Shortcut(0, ui.KeyHome) {
		lo, _ := a.timeRange()
		if cl := a.selectedClip(); cl != nil && cl.placed() {
			lo = cl.offset
		}
		a.seek(lo)
	}
	step := 1 / a.frameRate()
	for _, k := range []struct {
		mods ui.Modifiers
		d    float64
	}{{0, step}, {ui.Shift, 0.010}, {ui.Alt, 0.001}} {
		if c.Shortcut(k.mods, ui.KeyRight) {
			a.nudge(k.d)
		}
		if c.Shortcut(k.mods, ui.KeyLeft) {
			a.nudge(-k.d)
		}
	}
}

// frameRate is the selected clip's, or 30.
func (a *app) frameRate() float64 {
	if c := a.selectedClip(); c != nil && c.info != nil {
		if v, ok := c.info.Video(); ok && v.FPS > 1 {
			return v.FPS
		}
	}
	return 30
}

func (a *app) toolbar(c *ui.Context) {
	ui.Row(c).Padding(8, 12).Gap(8).Children(func() {
		ui.Button(c, "＋ 添加音频").Tooltip("匹配源 1：一个或多个音频文件，按顺序拼接为一条").OnClick(a.chooseAudios)
		ui.Button(c, "＋ 添加视频").Tooltip("匹配源 2：一个或多个视频，可选择其中的音轨").OnClick(a.chooseVideos)
		ui.Box(c).Width(1).Height(22).Background(c.Theme().Border)
		all := make([]int, 0, len(a.clips))
		for _, cl := range a.clips {
			all = append(all, cl.id)
		}
		ui.PrimaryButton(c, "自动对齐全部").Disabled(len(a.audios) == 0 || len(a.clips) == 0).
			Tooltip("用波形互相关（GCC-PHAT）把每个视频的音轨定位到外部音频上").
			OnClick(func() { a.alignClips(all) })
		ui.Button(c, "导出…").Disabled(len(a.clips) == 0 || len(a.audios) == 0).OnClick(func() { a.exportOpen = true })
		ui.Spacer(c)
		ui.Button(c, "打开工程").OnClick(a.openProject)
		ui.Button(c, "保存工程").Disabled(len(a.clips) == 0 && len(a.audios) == 0).
			Tooltip("Ctrl+S；Ctrl+Shift+S 另存为").OnClick(func() { a.saveProject(false, nil) })
		ui.Button(c, "设置").OnClick(func() { a.settingsOpen = true })
	})
}

func (a *app) statusBar(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Padding(5, 12).Gap(12).Children(func() {
		col := t.TextMuted
		if a.status != "" && !a.statusOK {
			col = t.Danger
		}
		msg := a.status
		if msg == "" {
			msg = "把音频和视频文件拖进窗口，或用工具栏添加。空格 播放/暂停 · ←/→ 微调 1 帧 · Shift ±10ms · Alt ±1ms"
		}
		ui.Text(c, msg).FontSize(12).TextColor(col).SingleLine().Grow(1)
		hw := media.DecodeLabel(a.prev.mode)
		if a.prev.mode == "" {
			hw = "待检测（" + hwLabels[max(indexOf(hwValues, a.cfg.HWAccel), 0)] + "）"
		}
		if n := len(a.conflicts()); n > 0 {
			ui.Text(c, fmt.Sprintf("时间轴冲突 %d 处", n)).FontSize(12).TextColor(t.Danger)
		}
		ui.Text(c, "预览解码: "+hw).FontSize(12).TextColor(t.TextMuted)
	})
}

// ---- Sidebar ----

func (a *app) sidebarView(c *ui.Context) {
	t := c.Theme()
	ui.Scroll(c).Fill().Background(ui.Hex("#17181c")).Children(func() {
		ui.Column(c).Padding(12).Gap(14).Children(func() {
			a.audioSection(c, t)
			a.clipSection(c, t)
		})
	})
}

func sectionTitle(c *ui.Context, title, sub string) {
	ui.Column(c).Gap(2).Children(func() {
		ui.Text(c, title).Bold().FontSize(14)
		if sub != "" {
			ui.Text(c, sub).FontSize(11).TextColor(c.Theme().TextMuted)
		}
	})
}

// channelOptions lists what can be matched of n channels.
func channelOptions(n int) []string {
	opts := []string{"全部声道混合"}
	for i := 1; i <= max(n, 1); i++ {
		name := fmt.Sprintf("声道 %d", i)
		if n == 2 {
			name += map[int]string{1: "（左）", 2: "（右）"}[i]
		}
		opts = append(opts, name)
	}
	return opts
}

func indexOf(opts []string, s string) int {
	for i, o := range opts {
		if o == s {
			return i
		}
	}
	return -1
}

func (a *app) audioSection(c *ui.Context, t *ui.Theme) {
	total := a.extEnd()
	sectionTitle(c, "匹配源 1 · 外部音频", fmt.Sprintf("%d 个文件，按顺序拼接 · 共 %s · 拖动行可排序", len(a.audios), fmtTime(total)))
	if len(a.audios) == 0 {
		drop := ui.Column(c).Padding(16).Radius(8).Border(1, t.Border).Center().Children(func() {
			ui.Text(c, "拖入或添加 WAV / MP3 / FLAC… 文件").TextColor(t.TextMuted).FontSize(12)
		})
		if files := drop.DroppedFiles(); files != nil {
			a.addAudios(files, nil)
			a.tl.fitPending = true
		}
		return
	}
	a.audioList.Reorder = a.reorderAudios
	a.audioList.Selected = &a.audioSel
	a.audioList.Key = func(i int) any { return a.audios[i].id }
	rowH := float32(0)
	for _, it := range a.audios {
		rowH += 46
		if len(it.streams) > 1 {
			rowH += 30
		}
	}
	ui.List(c, &a.audioList, len(a.audios), func(i int) {
		it := a.audios[i]
		ui.Column(c).Padding(6, 8).Gap(4).Children(func() {
			ui.Row(c).Gap(6).Children(func() {
				ui.Text(c, strconv.Itoa(i+1)).FontSize(11).TextColor(t.TextMuted).Width(16)
				ui.Text(c, it.name()).SingleLine().Grow(1).Tooltip(it.path)
				ui.Text(c, a.sourceState(&it.source)).FontSize(11).TextColor(stateColor(t, &it.source))
				ui.Button(c, "↑").Disabled(i == 0).Tooltip("上移").OnClick(func() { a.moveAudio(i, i-1) })
				ui.Button(c, "↓").Disabled(i == len(a.audios)-1).Tooltip("下移").OnClick(func() { a.moveAudio(i, i+1) })
				ui.Button(c, "✕").Tooltip("移除").OnClick(func() { a.removeAudio(it.id) })
			})
			if len(it.streams) > 1 {
				a.streamSelect(c, &it.source)
			}
		})
	}).Height(min(rowH+8, 280)).Border(1, t.Border).Radius(8).Padding(2)

	maxCh := 0
	for _, it := range a.audios {
		maxCh = max(maxCh, it.channels())
	}
	opts := channelOptions(maxCh)
	cur := opts[min(a.channel, len(opts)-1)]
	ui.Row(c).Gap(8).Children(func() {
		ui.Text(c, "匹配用声道").FontSize(12).TextColor(t.TextMuted)
		if ui.Select(c, &cur, opts).Grow(1).Tooltip("立体声或多声道录音可只用某一路做匹配，例如领夹麦所在声道").Changed() {
			a.channel = max(indexOf(opts, cur), 0)
			a.extKey = ""
		}
	})
}

// streamSelect chooses a source's audio stream.
func (a *app) streamSelect(c *ui.Context, s *source) {
	opts := make([]string, len(s.streams))
	for i, st := range s.streams {
		opts[i] = st.Label(i)
	}
	cur := opts[max(s.stream, 0)]
	if ui.Select(c.Key(s.id), &cur, opts).Changed() {
		a.setStream(s, indexOf(opts, cur))
	}
}

func (a *app) sourceState(s *source) string {
	switch {
	case s.err != "":
		return "错误"
	case s.loading && s.info == nil:
		return "读取中…"
	case s.loading:
		return fmt.Sprintf("解码 %d%%", int(s.progress*100))
	default:
		return fmtTime(s.duration())
	}
}

func stateColor(t *ui.Theme, s *source) ui.Color {
	if s.err != "" {
		return t.Danger
	}
	return t.TextMuted
}

func (a *app) clipSection(c *ui.Context, t *ui.Theme) {
	sectionTitle(c, "匹配源 2 · 视频", fmt.Sprintf("%d 个视频 · 点击选中，可在时间轴上拖动微调", len(a.clips)))
	if len(a.clips) == 0 {
		drop := ui.Column(c).Padding(16).Radius(8).Border(1, t.Border).Center().Children(func() {
			ui.Text(c, "拖入或添加 MP4 / MOV / MKV… 文件").TextColor(t.TextMuted).FontSize(12)
		})
		if files := drop.DroppedFiles(); files != nil {
			a.addClips(files, nil)
			a.tl.fitPending = true
		}
		return
	}
	for _, cl := range a.clips {
		a.clipCard(c.Key(cl.id), t, cl)
	}
}

func (a *app) clipCard(c *ui.Context, t *ui.Theme, cl *clip) {
	sel := cl.id == a.selected
	card := ui.Column(c).Padding(10).Gap(6).Radius(8).Border(1, t.Border).Background(ui.Hex("#1d1f25"))
	if sel {
		card.Border(1.5, t.Accent).Background(ui.Hex("#20263a"))
	}
	if card.Clicked() {
		a.selectClip(cl.id)
	}
	card.Children(func() {
		ui.Row(c).Gap(6).Children(func() {
			ui.Text(c, cl.name()).Bold().SingleLine().Grow(1).Tooltip(cl.path)
			ui.Text(c, a.sourceState(&cl.source)).FontSize(11).TextColor(stateColor(t, &cl.source))
		})
		if cl.info != nil {
			if v, ok := cl.info.Video(); ok {
				ui.Text(c, fmt.Sprintf("%s · %d×%d · %.3g fps", v.Codec, v.Width, v.Height, v.FPS)).FontSize(11).TextColor(t.TextMuted)
			}
		}
		if len(cl.streams) > 0 {
			a.streamSelect(c, &cl.source)
			opts := channelOptions(cl.channels())
			cur := opts[min(cl.channel, len(opts)-1)]
			ui.Row(c).Gap(8).Children(func() {
				ui.Text(c, "匹配用声道").FontSize(12).TextColor(t.TextMuted)
				if ui.Select(c.Key("ch"), &cur, opts).Grow(1).Changed() {
					cl.channel = max(indexOf(opts, cur), 0)
				}
			})
		}
		switch {
		case cl.err != "":
			ui.Text(c, cl.err).FontSize(12).TextColor(t.Danger)
		case cl.aligning:
			ui.Progress(c, cl.alignProg)
		case cl.loading && cl.info != nil:
			ui.Text(c, "解码音轨中，完成后可匹配").FontSize(12).TextColor(t.TextMuted)
		case cl.aligned:
			conf := int(cl.confidence*100 + 0.5)
			col := t.Success
			if conf < 60 {
				col = t.Warning
			}
			if conf < 30 {
				col = t.Danger
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Text(c, "偏移 "+fmtTime(cl.offset)).FontSize(12).Font("monospace")
				ui.Text(c, fmt.Sprintf("置信度 %d%%", conf)).FontSize(12).TextColor(col)
				if d := cl.offset - cl.autoOffset; math.Abs(d) > 1e-6 {
					ui.Text(c, "手动 "+fmtDelta(d)).FontSize(12).TextColor(t.Warning)
				}
			})
			if math.Abs(cl.driftPPM) >= 2 {
				drift := cl.driftPPM * 1e-6 * cl.duration() * 1000
				ui.Text(c, fmt.Sprintf("检测到时钟漂移 %+.1f ppm（全片累计约 %.0f ms，已按中点对齐）", cl.driftPPM, math.Abs(drift))).
					FontSize(11).TextColor(t.Warning)
			}
		case cl.manual:
			ui.Text(c, "手动放置 · 偏移 "+fmtTime(cl.offset)).FontSize(12).Font("monospace")
		default:
			ui.Text(c, "未匹配，尚未放到时间轴").FontSize(12).TextColor(t.TextMuted)
		}
		if cs := a.conflictsOf(cl); len(cs) > 0 {
			other := cs[0].a
			if other == cl {
				other = cs[0].b
			}
			msg := fmt.Sprintf("与 %s 重叠 %.1f 秒", other.name(), cs[0].to-cs[0].from)
			if len(cs) > 1 {
				msg += fmt.Sprintf(" 等 %d 处", len(cs))
			}
			ui.Text(c, msg).FontSize(12).TextColor(t.Danger).SingleLine().Tooltip(describeConflicts(cs))
		}
		if cl.exporting {
			ui.Progress(c, cl.exportProg)
		} else if cl.exported != "" {
			ui.Text(c, "已导出 → "+filepath.Base(cl.exported)).FontSize(11).TextColor(t.Success).SingleLine().Tooltip(cl.exported)
		} else if cl.exportErr != "" {
			ui.Text(c, cl.exportErr).FontSize(11).TextColor(t.Danger)
		}
		ui.Row(c).Gap(6).Children(func() {
			ui.Button(c, "对齐").Disabled(cl.aligning || len(cl.streams) == 0 || cl.loading || len(a.audios) == 0).
				OnClick(func() { a.alignClips([]int{cl.id}) })
			if !cl.placed() {
				ui.Button(c, "放到播放头").Disabled(cl.info == nil).
					Tooltip("不自动匹配，手动把视频放到播放头处，再在时间轴上微调").
					OnClick(func() { a.placeAtPlayhead(cl) })
			}
			ui.Button(c, "定位").Disabled(!cl.placed()).Tooltip("在时间轴上显示并跳到片头").OnClick(func() {
				a.selectClip(cl.id)
				a.tl.reveal(cl.offset, cl.end())
				a.seek(cl.offset)
			})
			if cl.placed() {
				ui.Button(c, "复位").Disabled(!cl.aligned || cl.offset == cl.autoOffset).Tooltip("回到自动匹配的位置").OnClick(func() {
					cl.offset = cl.autoOffset
					a.clipMoved()
				})
			}
			ui.Spacer(c)
			ui.Button(c, "移除").OnClick(func() { a.removeClip(cl.id) })
		})
	})
}

// ---- Preview ----

var monitorLabels = []string{"外部音频", "视频原声", "混合", "左右分离"}

func (a *app) previewView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Padding(10).Gap(8).Children(func() {
		screen := ui.Box(c).Grow(1).Background(ui.Hex("#000000")).Radius(6).Clip()
		screen.Children(func() {
			img := ui.Image(c, a.prev.bmp).Fill().Fit(ui.Contain)
			if !a.prev.has {
				img.Opacity(0.25)
				ui.Text(c, a.prev.label).TextColor(t.TextMuted).Attach(ui.AnchorCenter, ui.AnchorCenter)
			}
			if pc := a.previewClip(); pc != nil {
				ui.Text(c, pc.name()+"  "+fmtTime(a.playhead-pc.offset)).FontSize(11).
					TextColor(ui.Hex("#ffffff").Alpha(0.75)).Background(ui.Hex("#000000").Alpha(0.5)).
					Padding(2, 6).Radius(4).Attach(ui.AnchorTopLeft, ui.AnchorTopLeft).Left(8).Top(8)
			}
		})
		ui.Row(c).Gap(10).Children(func() {
			label := "▶ 播放"
			if a.playing {
				label = "❚❚ 暂停"
			}
			ui.PrimaryButton(c, label).Width(96).Tooltip("空格").OnClick(a.togglePlay)
			ui.Button(c, "⏮").Tooltip("到选中视频的片头（Home）").OnClick(func() {
				if cl := a.selectedClip(); cl != nil && cl.placed() {
					a.seek(cl.offset)
				} else {
					lo, _ := a.timeRange()
					a.seek(lo)
				}
			})
			ui.Text(c, fmtTime(a.playhead)).FontSize(18).Font("monospace").Width(150)
			ui.Spacer(c)
			if ui.Segmented(c, &a.monitor, monitorLabels...).Label("监听").
				Tooltip("左右分离：外部音频在左声道、视频原声在右声道，戴耳机最容易听出错位").Changed() {
				a.monitorChanged()
			}
		})
		ui.Row(c).Gap(10).Children(func() {
			ui.Text(c, "外部音频").FontSize(12).TextColor(t.TextMuted)
			if ui.Slider(c.Key("ga"), &a.gainA, 0, 2).Label("外部音频音量").Grow(1).Changed() {
				a.monitorChanged()
			}
			ui.Text(c, "视频原声").FontSize(12).TextColor(t.TextMuted)
			if ui.Slider(c.Key("gb"), &a.gainB, 0, 2).Label("视频原声音量").Grow(1).Changed() {
				a.monitorChanged()
			}
			if ui.Checkbox(c, &a.onlySel, "只听选中视频").Changed() {
				a.monitorChanged()
			}
		})
	})
}

// ---- Timeline panel ----

func (a *app) timelinePanel(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(6, 10).Gap(6).Children(func() {
			cl := a.selectedClip()
			if cl == nil || !cl.placed() {
				ui.Text(c, "时间轴").Bold()
				ui.Text(c, "选中时间轴上的视频后可以微调它的位置").FontSize(12).TextColor(t.TextMuted)
			} else {
				ui.Text(c, cl.name()).Bold().SingleLine().MaxWidth(220).Tooltip(cl.path)
				ui.Text(c, "偏移(秒)").FontSize(12).TextColor(t.TextMuted)
				v := cl.offset
				if ui.NumberInput(c.Key(cl.id), &v, -1e6, 1e6, 0.001).Width(130).Changed() {
					cl.offset = v
					a.clipMoved()
				}
				frame := 1 / a.frameRate()
				for _, n := range []struct {
					label string
					d     float64
				}{{"−1帧", -frame}, {"−10ms", -0.01}, {"−1ms", -0.001}, {"+1ms", 0.001}, {"+10ms", 0.01}, {"+1帧", frame}} {
					ui.Button(c, n.label).OnClick(func() { a.nudge(n.d) })
				}
			}
			ui.Spacer(c)
			ui.Text(c, "滚轮 / Ctrl+滚轮 / Alt+滚轮 缩放 · Shift+滚轮或拖滚动条平移 · 拖片段微调（Shift 精细）").FontSize(11).TextColor(t.TextMuted).SingleLine()
			ui.Button(c, "−").Tooltip("缩小").OnClick(func() { a.tl.zoom(1/1.6, a.tl.width/2) })
			ui.Button(c, "+").Tooltip("放大").OnClick(func() { a.tl.zoom(1.6, a.tl.width/2) })
			ui.Button(c, "全部").Tooltip("缩放到全部内容").OnClick(a.tl.fit)
		})
		canvas := ui.Box(c).Grow(1).FillWidth().Clip()
		if b := canvas.Bounds(); b.W > 0 {
			a.tl.width, a.tl.height, a.tl.box = b.W, b.H, b
		}
		if a.tl.drag == dragClip {
			canvas.Cursor(ui.CursorGrabbing)
		} else if a.tl.hoverClip != 0 {
			canvas.Cursor(ui.CursorGrab)
		}
		canvas.HandleInput(a.timelineInput)
		canvas.Draw(a.drawTimeline)
		canvas.PointerPosition() // frames as the pointer moves, for hovering
	})
}

// ---- Dialogs ----

func field(c *ui.Context, label string, fn func()) {
	ui.Row(c).Gap(10).Children(func() {
		ui.Text(c, label).FontSize(13).TextColor(c.Theme().TextMuted).Width(84)
		ui.Row(c).Grow(1).Gap(8).Children(fn)
	})
}

func (a *app) exportDialog(c *ui.Context) {
	if !a.exportOpen {
		return
	}
	t := c.Theme()
	ui.Modal(c, &a.exportOpen, func() {
		ui.Column(c).Width(620).Padding(20).Gap(12).Children(func() {
			ui.Text(c, "导出视频").FontSize(18).Bold()
			ui.Text(c, "视频流直接复制（-c:v copy），画面不会二次编码；只编码新的音轨。新音轨从外部音频中按对齐位置截取，长度与视频一致，不足处补静音。").
				FontSize(12).TextColor(t.TextMuted)
			field(c, "导出范围", func() {
				ui.Segmented(c, &a.exportScope, fmt.Sprintf("全部视频（%d）", len(a.clips)), "仅选中视频")
			})
			field(c, "输出目录", func() {
				ui.TextInput(c, &a.cfg.OutDir).Placeholder("与原视频相同目录").Grow(1)
				ui.Button(c, "选择…").OnClick(a.chooseOutDir)
			})
			field(c, "文件名后缀", func() {
				ui.TextInput(c, &a.cfg.Suffix).Width(160)
			})
			field(c, "容器", func() {
				ui.Segmented(c, &a.cfg.Container, "同原视频", "MP4", "MOV", "MKV")
			})
			field(c, "音频编码", func() {
				ui.Segmented(c, &a.cfg.Codec, "AAC 320k", "PCM 24-bit", "FLAC")
			})
			if a.cfg.Codec == 1 {
				ui.Text(c, "PCM 需要 MOV 或 MKV 容器，MP4 会自动改为 MOV。").FontSize(11).TextColor(t.TextMuted)
			} else if a.cfg.Codec == 2 {
				ui.Text(c, "FLAC 需要 MP4 或 MKV 容器，MOV 会自动改为 MKV。").FontSize(11).TextColor(t.TextMuted)
			}
			field(c, "声道", func() {
				ui.Segmented(c, &a.cfg.Layout, "同外部音频", "立体声", "单声道")
			})
			field(c, "采样率", func() {
				ui.Select(c, &a.cfg.Rate, []string{"自动", "44100", "48000", "96000"}).Width(140)
			})
			ui.Checkbox(c, &a.cfg.KeepOrig, "保留原视频音轨（作为后面的附加音轨）")

			targets := a.exportTargets()
			unaligned := 0
			for _, cl := range targets {
				if !cl.aligned {
					unaligned++
				}
			}
			if unaligned > 0 {
				ui.Text(c, fmt.Sprintf("注意：%d 个视频尚未自动匹配，将按当前手动位置导出。", unaligned)).FontSize(12).TextColor(t.Warning)
			}
			if a.exporting {
				done, sum := 0.0, 0.0
				for _, cl := range targets {
					sum++
					switch {
					case cl.exporting:
						done += cl.exportProg
					case cl.exported != "" || cl.exportErr != "":
						done++
					}
				}
				ui.Progress(c, done/max(sum, 1))
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if a.exporting {
					ui.Button(c, "停止导出").OnClick(func() {
						if a.exportCancel != nil {
							a.exportCancel()
						}
					})
				} else {
					ui.Button(c, "关闭").OnClick(func() { a.exportOpen = false })
				}
				ui.PrimaryButton(c, "开始导出").Disabled(a.exporting || len(targets) == 0).OnClick(a.startExport)
			})
		})
	})
}

var hwLabels = []string{"自动（GPU 优先）", "NVIDIA：NVDEC + CUDA 缩放", "Direct3D 11 解码", "关闭（CPU 解码）"}
var hwValues = []string{"auto", "cuda", "d3d11va", "none"}

func (a *app) settingsDialog(c *ui.Context) {
	if !a.settingsOpen {
		return
	}
	t := c.Theme()
	ui.Modal(c, &a.settingsOpen, func() {
		ui.Column(c).Width(600).Padding(20).Gap(12).Children(func() {
			ui.Text(c, "设置").FontSize(18).Bold()
			field(c, "ffmpeg 目录", func() {
				ui.TextInput(c, &a.cfg.FFmpegDir).Placeholder("自动：程序目录或 PATH").Grow(1)
				ui.Button(c, "选择…").OnClick(a.chooseFFmpegDir)
				ui.Button(c, "检测").OnClick(a.configureFFmpeg)
			})
			found := a.ffmpegFound
			if found == "" {
				found = "未找到 ffmpeg"
			}
			ui.Text(c, found).FontSize(11).TextColor(t.TextMuted)
			cur := hwLabels[max(indexOf(hwValues, a.cfg.HWAccel), 0)]
			field(c, "预览解码", func() {
				if ui.Select(c, &cur, hwLabels).Width(260).Changed() {
					a.cfg.HWAccel = hwValues[max(indexOf(hwLabels, cur), 0)]
					a.prev.mode = ""
					a.prev.seeked()
					a.cfg.save()
				}
			})
			ui.Text(c, "预览：NVIDIA 显卡上解码和缩放都在 GPU 上完成，只把 640×360 的小帧取回；不可用时依次回退 D3D11、CPU。当前："+media.DecodeLabel(a.prev.mode)+"。导出始终直接复制视频流，不解码也不编码画面。").FontSize(11).TextColor(t.TextMuted)
			field(c, "缓存", func() {
				ui.Text(c, media.CacheDir()).FontSize(11).TextColor(t.TextMuted).SingleLine().Grow(1).Tooltip(media.CacheDir())
				ui.Button(c, "清理缓存").OnClick(a.clearCache)
			})
			ui.Row(c).Justify(ui.End).Children(func() {
				ui.PrimaryButton(c, "完成").OnClick(func() {
					a.cfg.save()
					a.settingsOpen = false
				})
			})
		})
	})
}
