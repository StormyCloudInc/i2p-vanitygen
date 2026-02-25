package ui

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/go-i2p/i2p-vanitygen/internal/config"
	"github.com/go-i2p/i2p-vanitygen/internal/destination"
	"github.com/go-i2p/i2p-vanitygen/internal/generator"
	"github.com/go-i2p/i2p-vanitygen/internal/telemetry"
	"github.com/go-i2p/i2p-vanitygen/internal/updater"
	"github.com/go-i2p/i2p-vanitygen/internal/version"
)

type state struct {
	mu         sync.Mutex
	running    bool
	prefix     string
	cores      int
	status     string
	speed      string
	checked    string
	estimate   string
	result     string
	lastResult *generator.Result
	cancel     context.CancelFunc
	gen        *generator.Generator

	showOptIn        bool
	telemetryOptedIn bool

	// Auto-update
	updateAvailable   bool
	updateRelease     *updater.Release
	showUpdateOverlay bool
	updateDownloading bool
	updateProgress    float64 // 0.0–1.0
	updateDone        bool
	updateDoneError   string
	updateCancel      context.CancelFunc
}

func Run(w *app.Window) error {
	th := material.NewTheme()
	applyDarkTheme(th)
	maxCores := runtime.NumCPU()

	cfg := config.Load()

	var (
		prefixEditor     widget.Editor
		startBtn         widget.Clickable
		saveBtn          widget.Clickable
		coreSlider       widget.Float
		optInYesBtn      widget.Clickable
		optInNoBtn       widget.Clickable
		updateBannerBtn  widget.Clickable
		updateDismissBtn widget.Clickable
		updateInstallBtn widget.Clickable
		updateCancelBtn  widget.Clickable
	)
	prefixEditor.SingleLine = true
	coreSlider.Value = 1.0 // Start at max cores

	s := &state{
		cores:            maxCores,
		status:           "Idle",
		estimate:         "Awaiting input...",
		showOptIn:        !cfg.TelemetryAsked,
		telemetryOptedIn: cfg.TelemetryOptedIn,
	}

	// Set the window icon (Windows title bar)
	go func() {
		time.Sleep(200 * time.Millisecond)
		setWindowIcon("I2P Vanity Address Generator")
	}()

	// Background update check
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		rel, err := updater.Check(ctx)
		if err != nil || rel == nil {
			return
		}

		// Respect skipped version
		if cfg.SkippedVersion != "" && !updater.IsNewer(rel.TagName, cfg.SkippedVersion) {
			return
		}

		s.mu.Lock()
		s.updateAvailable = true
		s.updateRelease = rel
		s.mu.Unlock()
		w.Invalidate()
	}()

	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			s.stop()
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			paint.Fill(gtx.Ops, colorBg)

			// Handle opt-in buttons before other UI
			if optInYesBtn.Clicked(gtx) {
				s.showOptIn = false
				s.telemetryOptedIn = true
				go config.Save(&config.Config{TelemetryAsked: true, TelemetryOptedIn: true})
			}
			if optInNoBtn.Clicked(gtx) {
				s.showOptIn = false
				s.telemetryOptedIn = false
				go config.Save(&config.Config{TelemetryAsked: true, TelemetryOptedIn: false})
			}

			// Handle update banner buttons
			if updateBannerBtn.Clicked(gtx) {
				s.mu.Lock()
				s.showUpdateOverlay = true
				s.mu.Unlock()
			}
			if updateDismissBtn.Clicked(gtx) {
				s.mu.Lock()
				tag := ""
				if s.updateRelease != nil {
					tag = s.updateRelease.TagName
				}
				s.updateAvailable = false
				s.mu.Unlock()
				if tag != "" {
					go config.Save(&config.Config{
						TelemetryAsked:   cfg.TelemetryAsked,
						TelemetryOptedIn: cfg.TelemetryOptedIn,
						SkippedVersion:   tag,
					})
				}
			}

			// Handle update overlay buttons
			if updateInstallBtn.Clicked(gtx) {
				s.mu.Lock()
				done := s.updateDone
				downloading := s.updateDownloading
				rel := s.updateRelease
				s.mu.Unlock()

				if done {
					updater.Restart()
				} else if !downloading && rel != nil {
					s.mu.Lock()
					s.updateDownloading = true
					s.updateProgress = 0
					s.updateDoneError = ""
					s.mu.Unlock()

					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					s.mu.Lock()
					s.updateCancel = cancel
					s.mu.Unlock()

					go func() {
						progressCh := make(chan updater.DownloadProgress, 1)
						go func() {
							for p := range progressCh {
								s.mu.Lock()
								if p.TotalBytes > 0 {
									s.updateProgress = float64(p.BytesRead) / float64(p.TotalBytes)
								}
								s.mu.Unlock()
								w.Invalidate()
							}
						}()

						tmpPath, err := updater.Download(ctx, rel, progressCh)
						close(progressCh)
						cancel()
						if err != nil {
							s.mu.Lock()
							s.updateDownloading = false
							s.updateDoneError = "Download failed: " + err.Error()
							s.mu.Unlock()
							w.Invalidate()
							return
						}

						if err := updater.Apply(tmpPath); err != nil {
							os.Remove(tmpPath)
							s.mu.Lock()
							s.updateDownloading = false
							s.updateDoneError = "Install failed: " + err.Error()
							s.mu.Unlock()
							w.Invalidate()
							return
						}

						s.mu.Lock()
						s.updateDownloading = false
						s.updateDone = true
						s.mu.Unlock()
						w.Invalidate()
					}()
				}
			}
			if updateCancelBtn.Clicked(gtx) {
				s.mu.Lock()
				if s.updateCancel != nil {
					s.updateCancel()
				}
				s.showUpdateOverlay = false
				s.updateDownloading = false
				s.updateProgress = 0
				s.updateDoneError = ""
				s.mu.Unlock()
			}

			if startBtn.Clicked(gtx) {
				if s.running {
					s.stop()
				} else {
					s.start(w)
				}
			}
			if saveBtn.Clicked(gtx) {
				s.save()
			}

			// Sync slider to core count
			if !s.running {
				newCores := int(coreSlider.Value*float32(maxCores-1)+0.5) + 1
				if newCores < 1 {
					newCores = 1
				}
				if newCores > maxCores {
					newCores = maxCores
				}
				if newCores != s.cores {
					s.cores = newCores
					s.updateEstimate()
				}
			}

			newPrefix := strings.ToLower(prefixEditor.Text())
			if newPrefix != s.prefix {
				s.prefix = newPrefix
				s.updateEstimate()
			}

			layoutApp(gtx, th, s, &prefixEditor, &startBtn, &saveBtn, &coreSlider, maxCores, &updateBannerBtn, &updateDismissBtn)

			// Draw opt-in overlay on top
			if s.showOptIn {
				layoutOptInOverlay(gtx, th, &optInYesBtn, &optInNoBtn)
			}

			// Draw update overlay on top
			s.mu.Lock()
			showUpdate := s.showUpdateOverlay
			s.mu.Unlock()
			if showUpdate {
				layoutUpdateOverlay(gtx, th, s, &updateInstallBtn, &updateCancelBtn)
			}

			e.Frame(gtx.Ops)

			s.mu.Lock()
			needInvalidate := s.running || s.updateDownloading
			s.mu.Unlock()
			if needInvalidate {
				w.Invalidate()
			}
		}
	}
}

func layoutApp(gtx layout.Context, th *material.Theme, s *state, prefixEditor *widget.Editor, startBtn, saveBtn *widget.Clickable, coreSlider *widget.Float, maxCores int, updateBannerBtn, updateDismissBtn *widget.Clickable) layout.Dimensions {
	// Center content horizontally, pin to top, cap width at 460dp
	return layout.N.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		maxW := gtx.Dp(460)
		if gtx.Constraints.Max.X > maxW {
			gtx.Constraints.Max.X = maxW
			gtx.Constraints.Min.X = maxW
		}

		return layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(30), Left: unit.Dp(30), Right: unit.Dp(30)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				// Header
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layoutHeader(gtx, th)
				}),
				layout.Rigid(vspace(16)),

				// Update banner (conditional)
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					s.mu.Lock()
					available := s.updateAvailable
					rel := s.updateRelease
					s.mu.Unlock()
					if !available || rel == nil {
						return layout.Dimensions{}
					}
					return layout.Inset{Bottom: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layoutUpdateBanner(gtx, th, rel, updateBannerBtn, updateDismissBtn)
					})
				}),

				// Input card
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layoutInputCard(gtx, th, s, prefixEditor, startBtn, coreSlider, maxCores)
				}),
				layout.Rigid(vspace(20)),

				// Results card
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layoutResultsCard(gtx, th, s, saveBtn)
				}),
			)
		})
	})
}

func layoutHeader(gtx layout.Context, th *material.Theme) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
		// I2P logo: "I2P" text + colored dot grid
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					// "I2P" text
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.H2(th, "I2P")
						lbl.Color = colorText
						lbl.Font.Weight = font.Bold
						return lbl.Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Spacer{Width: unit.Dp(12)}.Layout(gtx)
					}),
					// Dot grid (4x2 colored circles from SVG)
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layoutLogoDots(gtx, unit.Dp(22), unit.Dp(6))
					}),
				)
			})
		}),
		layout.Rigid(vspace(6)),
		// Subtitle
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				lbl := material.H6(th, "Vanity Address Generator")
				lbl.Color = colorText
				lbl.Alignment = text.Middle
				return lbl.Layout(gtx)
			})
		}),
		// Version label
		layout.Rigid(vspace(4)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				lbl := material.Caption(th, version.Version)
				lbl.Color = colorMuted
				return lbl.Layout(gtx)
			})
		}),
	)
}

func layoutOptInOverlay(gtx layout.Context, th *material.Theme, yesBtn, noBtn *widget.Clickable) layout.Dimensions {
	// Semi-transparent scrim over entire window
	paint.Fill(gtx.Ops, colorOverlay)

	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		maxW := gtx.Dp(380)
		if gtx.Constraints.Max.X > maxW {
			gtx.Constraints.Max.X = maxW
		}
		gtx.Constraints.Min.X = gtx.Constraints.Max.X

		return cardWithBorder(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.H6(th, "Anonymous Telemetry")
					lbl.Color = colorText
					lbl.Font.Weight = font.SemiBold
					lbl.Alignment = text.Middle
					return lbl.Layout(gtx)
				}),
				layout.Rigid(vspace(12)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.Body2(th, "Help improve this tool by anonymously sharing generation times.\n\nOnly prefix length, duration, core count, and attempt count are sent. No keys, addresses, or personal data is ever collected.")
					lbl.Color = colorTextBody
					lbl.Alignment = text.Middle
					return lbl.Layout(gtx)
				}),
				layout.Rigid(vspace(20)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return layout.Flex{}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							btn := material.Button(th, noBtn, "No Thanks")
							btn.Background = color.NRGBA{R: 0x40, G: 0x40, B: 0x40, A: 0xff}
							btn.Color = colorTextBody
							btn.Inset = layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10)}
							return btn.Layout(gtx)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Spacer{Width: unit.Dp(12)}.Layout(gtx)
						}),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							btn := material.Button(th, yesBtn, "Sure!")
							btn.Background = colorAccent
							btn.Color = color.NRGBA{A: 0xff}
							btn.Font.Weight = font.SemiBold
							btn.Inset = layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10)}
							return btn.Layout(gtx)
						}),
					)
				}),
			)
		})
	})
}

func layoutUpdateBanner(gtx layout.Context, th *material.Theme, rel *updater.Release, updateBtn, dismissBtn *widget.Clickable) layout.Dimensions {
	bannerBg := color.NRGBA{R: 0x00, G: 0x1a, B: 0x22, A: 0xff}

	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			sz := gtx.Constraints.Min
			rr := gtx.Dp(8)
			rrect := clip.RRect{Rect: image.Rect(0, 0, sz.X, sz.Y), NE: rr, NW: rr, SE: rr, SW: rr}
			paint.FillShape(gtx.Ops, bannerBg, rrect.Op(gtx.Ops))
			paint.FillShape(gtx.Ops, colorAccent, clip.Stroke{Path: rrect.Path(gtx.Ops), Width: 1}.Op())
			return layout.Dimensions{Size: sz}
		}),
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(14)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Body2(th, fmt.Sprintf("Update available: %s", rel.TagName))
						lbl.Color = colorAccent
						lbl.Font.Weight = font.SemiBold
						return lbl.Layout(gtx)
					}),
					layout.Rigid(vspace(8)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						return layout.Flex{}.Layout(gtx,
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								btn := material.Button(th, dismissBtn, "Later")
								btn.Background = color.NRGBA{R: 0x2a, G: 0x2a, B: 0x2a, A: 0xff}
								btn.Color = colorTextBody
								btn.Inset = layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8)}
								return btn.Layout(gtx)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Spacer{Width: unit.Dp(12)}.Layout(gtx)
							}),
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								btn := material.Button(th, updateBtn, "Update Now")
								btn.Background = colorAccent
								btn.Color = color.NRGBA{A: 0xff}
								btn.Font.Weight = font.SemiBold
								btn.Inset = layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8)}
								return btn.Layout(gtx)
							}),
						)
					}),
				)
			})
		}),
	)
}

func layoutUpdateOverlay(gtx layout.Context, th *material.Theme, s *state, installBtn, cancelBtn *widget.Clickable) layout.Dimensions {
	s.mu.Lock()
	downloading := s.updateDownloading
	progress := s.updateProgress
	done := s.updateDone
	doneErr := s.updateDoneError
	rel := s.updateRelease
	s.mu.Unlock()

	paint.Fill(gtx.Ops, colorOverlay)

	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		maxW := gtx.Dp(380)
		if gtx.Constraints.Max.X > maxW {
			gtx.Constraints.Max.X = maxW
		}
		gtx.Constraints.Min.X = gtx.Constraints.Max.X

		return cardWithBorder(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
				// Title
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.H6(th, "Software Update")
					lbl.Color = colorText
					lbl.Font.Weight = font.SemiBold
					lbl.Alignment = text.Middle
					return lbl.Layout(gtx)
				}),
				layout.Rigid(vspace(12)),

				// Body text
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					var msg string
					switch {
					case doneErr != "":
						msg = doneErr
					case done:
						msg = "Update installed successfully.\nRestart to use " + rel.TagName + "."
					case downloading:
						pct := int(progress * 100)
						msg = fmt.Sprintf("Downloading update... %d%%", pct)
					default:
						msg = fmt.Sprintf("Version %s is available.\nYou are running %s.",
							rel.TagName, version.Version)
					}
					lbl := material.Body2(th, msg)
					lbl.Color = colorTextBody
					lbl.Alignment = text.Middle
					return lbl.Layout(gtx)
				}),
				layout.Rigid(vspace(12)),

				// Progress bar
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if !downloading {
						return layout.Dimensions{}
					}
					return layoutProgressBar(gtx, progress)
				}),
				layout.Rigid(vspace(12)),

				// Buttons
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X

					if done {
						btn := material.Button(th, installBtn, "Restart Now")
						btn.Background = colorAccent
						btn.Color = color.NRGBA{A: 0xff}
						btn.Font.Weight = font.SemiBold
						btn.Inset = layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10)}
						return btn.Layout(gtx)
					}

					if doneErr != "" {
						btn := material.Button(th, cancelBtn, "Close")
						btn.Background = color.NRGBA{R: 0x40, G: 0x40, B: 0x40, A: 0xff}
						btn.Color = colorTextBody
						btn.Inset = layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10)}
						return btn.Layout(gtx)
					}

					if downloading {
						btn := material.Button(th, cancelBtn, "Cancel")
						btn.Background = color.NRGBA{R: 0x40, G: 0x40, B: 0x40, A: 0xff}
						btn.Color = colorTextBody
						btn.Inset = layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10)}
						return btn.Layout(gtx)
					}

					return layout.Flex{}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							btn := material.Button(th, cancelBtn, "Cancel")
							btn.Background = color.NRGBA{R: 0x40, G: 0x40, B: 0x40, A: 0xff}
							btn.Color = colorTextBody
							btn.Inset = layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10)}
							return btn.Layout(gtx)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Spacer{Width: unit.Dp(12)}.Layout(gtx)
						}),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							btn := material.Button(th, installBtn, "Install & Restart")
							btn.Background = colorAccent
							btn.Color = color.NRGBA{A: 0xff}
							btn.Font.Weight = font.SemiBold
							btn.Inset = layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10)}
							return btn.Layout(gtx)
						}),
					)
				}),
			)
		})
	})
}

func layoutProgressBar(gtx layout.Context, progress float64) layout.Dimensions {
	barHeight := gtx.Dp(6)
	width := gtx.Constraints.Max.X

	rr := barHeight / 2
	track := clip.RRect{
		Rect: image.Rect(0, 0, width, barHeight),
		NE:   rr, NW: rr, SE: rr, SW: rr,
	}
	paint.FillShape(gtx.Ops, colorInputBg, track.Op(gtx.Ops))

	fillW := int(float64(width) * progress)
	if fillW > 0 {
		fill := clip.RRect{
			Rect: image.Rect(0, 0, fillW, barHeight),
			NE:   rr, NW: rr, SE: rr, SW: rr,
		}
		paint.FillShape(gtx.Ops, colorAccent, fill.Op(gtx.Ops))
	}

	return layout.Dimensions{Size: image.Pt(width, barHeight)}
}

// layoutLogoDots draws the 4x2 colored dot grid from the I2P logo SVG.
func layoutLogoDots(gtx layout.Context, dotSize, gap unit.Dp) layout.Dimensions {
	colors := [2][4]color.NRGBA{
		{colorLogoGreen, colorLogoYellow, colorLogoGreen, colorLogoRed},
		{colorLogoYellow, colorLogoRed, colorLogoYellow, colorLogoGreen},
	}

	ds := gtx.Dp(dotSize)
	g := gtx.Dp(gap)
	r := ds / 2

	for row := 0; row < 2; row++ {
		for col := 0; col < 4; col++ {
			x := col * (ds + g)
			y := row * (ds + g)
			rrect := clip.RRect{
				Rect: image.Rect(x, y, x+ds, y+ds),
				NE:   r, NW: r, SE: r, SW: r,
			}
			paint.FillShape(gtx.Ops, colors[row][col], rrect.Op(gtx.Ops))
		}
	}

	totalW := 4*ds + 3*g
	totalH := 2*ds + 1*g
	return layout.Dimensions{Size: image.Pt(totalW, totalH)}
}

func layoutInputCard(gtx layout.Context, th *material.Theme, s *state, prefixEditor *widget.Editor, startBtn *widget.Clickable, coreSlider *widget.Float, maxCores int) layout.Dimensions {
	return cardWithBorder(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			// Target Prefix — force full width
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(sectionLabel(th, "TARGET PREFIX")),
					layout.Rigid(vspace(8)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return styledInput(gtx, th, prefixEditor, "e.g. hello", !s.running)
					}),
				)
			}),
			// Validation
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if s.prefix == "" {
					return layout.Dimensions{}
				}
				if err := destination.ValidatePrefix(s.prefix); err != nil {
					return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						lbl := material.Caption(th, err.Error())
						lbl.Color = color.NRGBA{R: 0xff, G: 0x44, B: 0x44, A: 0xff}
						return lbl.Layout(gtx)
					})
				}
				return layout.Dimensions{}
			}),
			layout.Rigid(vspace(18)),

			// CPU Cores
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(sectionLabel(th, "CPU CORES")),
					layout.Rigid(vspace(8)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layoutCoreSelector(gtx, th, s, coreSlider, maxCores)
					}),
				)
			}),
			layout.Rigid(vspace(20)),

			// Start button — force full width
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				label := "Start Search"
				bg := colorAccent
				fg := color.NRGBA{R: 0x00, G: 0x00, B: 0x00, A: 0xff}
				if s.running {
					label = "Stop"
					bg = color.NRGBA{R: 0x40, G: 0x40, B: 0x40, A: 0xff}
					fg = colorTextBody
				}
				btn := material.Button(th, startBtn, label)
				btn.Background = bg
				btn.Color = fg
				btn.Font.Weight = font.SemiBold
				btn.TextSize = unit.Sp(16)
				btn.Inset = layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10)}
				return btn.Layout(gtx)
			}),
		)
	})
}

func layoutCoreSelector(gtx layout.Context, th *material.Theme, s *state, slider *widget.Float, maxCores int) layout.Dimensions {
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		// Slider
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			sl := material.Slider(th, slider)
			sl.Color = colorAccent
			return sl.Layout(gtx)
		}),
		// Badge
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(15)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return badge(gtx, th, fmt.Sprintf("%d / %d", s.cores, maxCores))
			})
		}),
	)
}

func layoutResultsCard(gtx layout.Context, th *material.Theme, s *state, saveBtn *widget.Clickable) layout.Dimensions {
	s.mu.Lock()
	status := s.status
	speed := s.speed
	checked := s.checked
	estimate := s.estimate
	result := s.result
	hasResult := s.lastResult != nil
	s.mu.Unlock()

	return cardWithBorder(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			// Stats 2-column grid
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return statBox(gtx, th, "STATUS", status)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Spacer{Width: unit.Dp(15)}.Layout(gtx)
					}),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return statBox(gtx, th, "EST. TIME", estimate)
					}),
				)
			}),

			// Speed/checked row
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if speed == "" && checked == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return statBox(gtx, th, "SPEED", speed)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Spacer{Width: unit.Dp(15)}.Layout(gtx)
						}),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return statBox(gtx, th, "CHECKED", checked)
						}),
					)
				})
			}),
			layout.Rigid(vspace(15)),

			// Generated Address
			layout.Rigid(sectionLabel(th, "GENERATED ADDRESS")),
			layout.Rigid(vspace(8)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return resultBox(gtx, th, result)
			}),
			layout.Rigid(vspace(15)),

			// Save button — force full width
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				btn := material.Button(th, saveBtn, "Save Keys")
				if hasResult {
					btn.Background = colorAccent
					btn.Color = color.NRGBA{A: 0xff}
				} else {
					btn.Background = color.NRGBA{A: 0}
					btn.Color = colorMuted
				}
				btn.Font.Weight = font.SemiBold
				btn.Inset = layout.Inset{Top: unit.Dp(14), Bottom: unit.Dp(14)}
				return btn.Layout(gtx)
			}),
		)
	})
}

// --- Reusable drawing helpers ---

func cardWithBorder(gtx layout.Context, content func(gtx layout.Context) layout.Dimensions) layout.Dimensions {
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			sz := gtx.Constraints.Min
			rr := gtx.Dp(8)
			rrect := clip.RRect{Rect: image.Rect(0, 0, sz.X, sz.Y), NE: rr, NW: rr, SE: rr, SW: rr}
			// Fill
			paint.FillShape(gtx.Ops, colorCard, rrect.Op(gtx.Ops))
			// Border
			paint.FillShape(gtx.Ops, colorCardBorder, clip.Stroke{Path: rrect.Path(gtx.Ops), Width: 1}.Op())
			return layout.Dimensions{Size: sz}
		}),
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(20)).Layout(gtx, content)
		}),
	)
}

func styledInput(gtx layout.Context, th *material.Theme, editor *widget.Editor, hint string, enabled bool) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			sz := gtx.Constraints.Min
			rr := gtx.Dp(6)
			rrect := clip.RRect{Rect: image.Rect(0, 0, sz.X, sz.Y), NE: rr, NW: rr, SE: rr, SW: rr}
			paint.FillShape(gtx.Ops, colorInputBg, rrect.Op(gtx.Ops))
			paint.FillShape(gtx.Ops, colorInputBdr, clip.Stroke{Path: rrect.Path(gtx.Ops), Width: 1}.Op())
			return layout.Dimensions{Size: sz}
		}),
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Inset{Top: unit.Dp(14), Bottom: unit.Dp(14), Left: unit.Dp(14), Right: unit.Dp(14)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				ed := material.Editor(th, editor, hint)
				ed.TextSize = unit.Sp(16)
				ed.Color = colorText
				ed.HintColor = colorLabel
				if !enabled {
					ed.Color = colorMuted
				}
				return ed.Layout(gtx)
			})
		}),
	)
}

func statBox(gtx layout.Context, th *material.Theme, label, value string) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			sz := gtx.Constraints.Min
			rr := gtx.Dp(6)
			rrect := clip.RRect{Rect: image.Rect(0, 0, sz.X, sz.Y), NE: rr, NW: rr, SE: rr, SW: rr}
			paint.FillShape(gtx.Ops, colorInputBg, rrect.Op(gtx.Ops))
			paint.FillShape(gtx.Ops, colorCardBorder, clip.Stroke{Path: rrect.Path(gtx.Ops), Width: 1}.Op())
			return layout.Dimensions{Size: sz}
		}),
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Caption(th, label)
						lbl.Color = colorLabel
						lbl.Font.Weight = font.SemiBold
						return lbl.Layout(gtx)
					}),
					layout.Rigid(vspace(4)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if value == "" {
							value = "-"
						}
						lbl := material.Body2(th, value)
						lbl.Color = colorText
						return lbl.Layout(gtx)
					}),
				)
			})
		}),
	)
}

func resultBox(gtx layout.Context, th *material.Theme, result string) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			sz := gtx.Constraints.Min
			rr := gtx.Dp(6)
			rrect := clip.RRect{Rect: image.Rect(0, 0, sz.X, sz.Y), NE: rr, NW: rr, SE: rr, SW: rr}
			paint.FillShape(gtx.Ops, colorInputBg, rrect.Op(gtx.Ops))
			paint.FillShape(gtx.Ops, colorCardBorder, clip.Stroke{Path: rrect.Path(gtx.Ops), Width: 1}.Op())
			return layout.Dimensions{Size: sz}
		}),
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				txt := "No result yet"
				clr := colorLabel
				if result != "" {
					txt = result
					clr = colorAccent
				}
				lbl := material.Body2(th, txt)
				lbl.Color = clr
				lbl.Font.Typeface = typefaceMono
				lbl.Alignment = text.Middle
				return lbl.Layout(gtx)
			})
		}),
	)
}

func badge(gtx layout.Context, th *material.Theme, text string) layout.Dimensions {
	return layout.Stack{Alignment: layout.Center}.Layout(gtx,
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			sz := gtx.Constraints.Min
			rr := gtx.Dp(6)
			rrect := clip.RRect{Rect: image.Rect(0, 0, sz.X, sz.Y), NE: rr, NW: rr, SE: rr, SW: rr}
			paint.FillShape(gtx.Ops, colorBadgeBg, rrect.Op(gtx.Ops))
			return layout.Dimensions{Size: sz}
		}),
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(12), Right: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				lbl := material.Body2(th, text)
				lbl.Color = colorTextBody
				lbl.Font.Weight = font.SemiBold
				return lbl.Layout(gtx)
			})
		}),
	)
}

func sectionLabel(th *material.Theme, label string) func(gtx layout.Context) layout.Dimensions {
	return func(gtx layout.Context) layout.Dimensions {
		lbl := material.Caption(th, label)
		lbl.Color = colorLabel
		lbl.Font.Weight = font.SemiBold
		return lbl.Layout(gtx)
	}
}

func vspace(dp float32) func(gtx layout.Context) layout.Dimensions {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Spacer{Height: unit.Dp(dp)}.Layout(gtx)
	}
}

// --- State methods ---

func (s *state) updateEstimate() {
	if s.prefix == "" || destination.ValidatePrefix(s.prefix) != nil {
		s.mu.Lock()
		s.estimate = "Awaiting input..."
		s.mu.Unlock()
		return
	}
	attempts := destination.EstimateAttempts(len(s.prefix))
	keysPerSec := 500_000.0 * float64(s.cores)
	seconds := attempts / keysPerSec
	s.mu.Lock()
	if seconds < 1 {
		s.estimate = "< 1 second"
	} else {
		s.estimate = "~" + formatDuration(time.Duration(seconds*float64(time.Second)))
	}
	s.mu.Unlock()
}

func (s *state) start(w *app.Window) {
	if s.prefix == "" || destination.ValidatePrefix(s.prefix) != nil {
		return
	}

	s.mu.Lock()
	s.running = true
	s.status = "Searching..."
	s.speed = ""
	s.checked = ""
	s.result = ""
	s.lastResult = nil
	s.mu.Unlock()

	gen := generator.New(s.prefix, s.cores)
	ctx, cancel := context.WithCancel(context.Background())

	s.mu.Lock()
	s.gen = gen
	s.cancel = cancel
	s.mu.Unlock()

	resultCh, statsCh := gen.Start(ctx)

	go func() {
		for stats := range statsCh {
			s.mu.Lock()
			s.speed = fmt.Sprintf("%s keys/sec", formatNumber(stats.KeysPerSec))
			s.checked = fmt.Sprintf("%s", formatUint(stats.Checked))
			s.mu.Unlock()
			w.Invalidate()
		}
	}()

	go func() {
		for result := range resultCh {
			s.mu.Lock()
			s.lastResult = &result
			s.result = result.Address
			s.status = fmt.Sprintf("Found in %s (%s attempts)", formatDuration(result.Duration), formatUint(result.Attempts))
			s.running = false
			prefixLen := len(s.prefix)
			cores := s.cores
			optedIn := s.telemetryOptedIn
			s.mu.Unlock()
			w.Invalidate()

			if optedIn {
				telemetry.Submit(telemetry.Payload{
					PrefixLength:    prefixLen,
					DurationSeconds: result.Duration.Seconds(),
					CoresUsed:       cores,
					Attempts:        result.Attempts,
				})
			}
		}
	}()
}

func (s *state) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	s.status = "Stopped"
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *state) save() {
	s.mu.Lock()
	r := s.lastResult
	s.mu.Unlock()
	if r == nil {
		return
	}
	addr := strings.ReplaceAll(r.Address, ".b32.i2p", "")
	if len(addr) > 16 {
		addr = addr[:16]
	}
	path := "vanity_" + addr + ".dat"
	if err := r.Destination.SaveKeys(path); err != nil {
		s.mu.Lock()
		s.status = "Save error: " + err.Error()
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.status = "Keys saved to " + path
	s.mu.Unlock()
}

func formatNumber(n float64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.2fM", n/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", n/1_000)
	}
	return fmt.Sprintf("%.0f", n)
}

func formatUint(n uint64) string {
	if n >= 1_000_000_000 {
		return fmt.Sprintf("%.2fB", float64(n)/1_000_000_000)
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return "< 1s"
	}
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60

	if h > 24 {
		days := h / 24
		h = h % 24
		return fmt.Sprintf("%dd %dh %dm", days, h, m)
	}
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, sec)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}
