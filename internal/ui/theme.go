package ui

import (
	"image/color"

	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/text"
	"gioui.org/widget/material"
)

var (
	colorBg         = color.NRGBA{R: 0x0a, G: 0x0a, B: 0x0a, A: 0xff} // #0a0a0a body
	colorWindow     = color.NRGBA{R: 0x14, G: 0x14, B: 0x14, A: 0xff} // #141414 app window
	colorCard       = color.NRGBA{R: 0x1a, G: 0x1a, B: 0x1a, A: 0xff} // #1a1a1a card
	colorCardBorder = color.NRGBA{R: 0x2a, G: 0x2a, B: 0x2a, A: 0xff} // #2a2a2a border
	colorInputBg    = color.NRGBA{R: 0x14, G: 0x14, B: 0x14, A: 0xff} // #141414 input bg
	colorInputBdr   = color.NRGBA{R: 0x33, G: 0x33, B: 0x33, A: 0xff} // #333333 input border
	colorText       = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff} // #ffffff
	colorTextBody   = color.NRGBA{R: 0xe0, G: 0xe0, B: 0xe0, A: 0xff} // #e0e0e0
	colorLabel      = color.NRGBA{R: 0x88, G: 0x88, B: 0x88, A: 0xff} // #888888
	colorMuted      = color.NRGBA{R: 0x55, G: 0x55, B: 0x55, A: 0xff} // #555555
	colorAccent     = color.NRGBA{R: 0x00, G: 0xe5, B: 0xff, A: 0xff} // #00e5ff cyan
	colorBadgeBg    = color.NRGBA{R: 0x2a, G: 0x2a, B: 0x2a, A: 0xff} // #2a2a2a
	colorDisabled   = color.NRGBA{R: 0x33, G: 0x33, B: 0x33, A: 0xff} // #333333

	// I2P logo dot colors (from SVG)
	colorLogoGreen  = color.NRGBA{R: 0x60, G: 0xab, B: 0x60, A: 0xff} // #60ab60
	colorLogoYellow = color.NRGBA{R: 0xff, G: 0xc4, B: 0x34, A: 0xff} // #ffc434
	colorLogoRed    = color.NRGBA{R: 0xe1, G: 0x56, B: 0x47, A: 0xff} // #e15647

	// Modal overlay scrim
	colorOverlay = color.NRGBA{A: 0xCC} // semi-transparent black

	// Typeface names — Gio will resolve these from system fonts, falling back to bundled Go fonts.
	typefaceUI   font.Typeface = "Segoe UI"
	typefaceMono font.Typeface = "Consolas"
)

func applyDarkTheme(th *material.Theme) {
	th.Palette.Bg = colorBg
	th.Palette.Fg = colorText
	th.Palette.ContrastBg = colorAccent
	th.Palette.ContrastFg = color.NRGBA{R: 0x00, G: 0x00, B: 0x00, A: 0xff}

	// Enable system fonts alongside bundled Go fonts.
	th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))

	// Set default typeface to Segoe UI (Windows system font).
	th.Face = typefaceUI
}
