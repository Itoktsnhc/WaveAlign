// WaveAlign lines up videos with separately recorded audio by their
// waveforms, and writes the videos with that audio, copying their picture as
// it is.
package main

import (
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func main() {
	a := newApp()
	a.configureFFmpeg()
	if a.ffmpegFound != "" {
		a.status = ""
	}
	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "WaveAlign · 音视频波形同步",
			Width:     1440,
			Height:    920,
			MinWidth:  1080,
			MinHeight: 680,
			StateKey:  "main",
			Content:   ui.View(a.view),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
