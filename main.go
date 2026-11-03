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
		// Closing with unsaved changes asks to save the project first.
		a.win.OnClose(func(e *mygo.CloseEvent) {
			if a.dirty() {
				e.PreventDefault()
				a.confirmClose()
				return
			}
			a.close()
		})
		mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
			if a.dirty() {
				e.PreventDefault()
				a.confirmClose()
			}
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
