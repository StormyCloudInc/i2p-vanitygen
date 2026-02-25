package main

import (
	"fyne.io/fyne/v2/app"

	"github.com/go-i2p/i2p-vanitygen/internal/ui"
)

func main() {
	a := app.New()
	mainApp := ui.New(a)
	mainApp.Show()
}
