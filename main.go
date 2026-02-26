package main

import (
	"os"

	"gioui.org/app"

	"github.com/go-i2p/i2p-vanitygen/internal/ui"
	"github.com/go-i2p/i2p-vanitygen/internal/updater"
)

func main() {
	updater.Cleanup()

	go func() {
		w := new(app.Window)
		w.Option(app.Title("I2P Vanity Address Generator"))
		w.Option(app.Size(520, 680))
		if err := ui.Run(w); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}()
	app.Main()
}
