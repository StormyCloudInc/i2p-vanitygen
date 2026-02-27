package ui

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/gesture"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/go-i2p/i2p-vanitygen/internal/address"
	"github.com/go-i2p/i2p-vanitygen/internal/config"
	"github.com/go-i2p/i2p-vanitygen/internal/generator"
	"github.com/go-i2p/i2p-vanitygen/internal/gpu"
	"github.com/go-i2p/i2p-vanitygen/internal/updater"
	"github.com/go-i2p/i2p-vanitygen/internal/version"
)

type state struct {
	mu         sync.Mutex
	running    bool
	prefix     string   // raw input text
	prefixes   []string // parsed individual prefixes
	cores      int
	status   string
	speed    string
	elapsed  string
	checked  string
	estimate string
	result     string
	lastResult *generator.Result
	cancel     context.CancelFunc
	gen        *generator.Generator

	// Network
	network address.Network
	scheme  address.Scheme

	// GPU
	gpuAvailable bool
	gpuDevices   []gpu.Device
	useGPU       bool
	gpuDevice    int

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
		gpuToggle        widget.Bool
		netI2PBtn        widget.Clickable
		netTorBtn        widget.Clickable
		updateBannerBtn  widget.Clickable
		updateDismissBtn widget.Clickable
		updateInstallBtn widget.Clickable
		updateCancelBtn  widget.Clickable
		scrollList       widget.List
		projectLink      gesture.Click
		donateLink       gesture.Click
		stormyLink       gesture.Click
	)
	scrollList.Axis = layout.Vertical
	prefixEditor.SingleLine = true
	coreSlider.Value = 1.0 // Start at max cores

	initNetwork := address.ParseNetwork(cfg.Network)
	var initScheme address.Scheme
	switch initNetwork {
	case address.NetworkTorV3:
		initScheme = address.TorV3Scheme{}
	default:
		initScheme = address.I2PScheme{}
	}

	s := &state{
		cores:            maxCores,
		network:          initNetwork,
		scheme:           initScheme,
		status:           "Idle",
		estimate:         "Awaiting input...",
	}

	// Detect GPU devices in background to avoid blocking UI startup
	go func() {
		if devices, err := gpu.ListDevices(); err == nil && len(devices) > 0 {
			s.mu.Lock()
			s.gpuAvailable = true
			s.gpuDevices = devices
			s.useGPU = true
			s.mu.Unlock()
			gpuToggle.Value = true
			w.Invalidate()
		}
	}()

	// Set the window icon (Windows title bar)
	go func() {
		time.Sleep(200 * time.Millisecond)
		setWindowIcon("Vanity Domain Generator")
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
						SkippedVersion: tag,
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

			// Handle network selector
			if !s.running {
				if netI2PBtn.Clicked(gtx) && s.network != address.NetworkI2P {
					s.network = address.NetworkI2P
					s.scheme = address.I2PScheme{}
					s.result = ""
					s.lastResult = nil
					s.updateEstimate()
				}
				if netTorBtn.Clicked(gtx) && s.network != address.NetworkTorV3 {
					s.network = address.NetworkTorV3
					s.scheme = address.TorV3Scheme{}
					s.result = ""
					s.lastResult = nil
					s.updateEstimate()
				}
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

			// Sync GPU toggle (only relevant for GPU-capable schemes)
			if !s.running && s.gpuAvailable && s.scheme.SupportsGPU() && gpuToggle.Value != s.useGPU {
				s.useGPU = gpuToggle.Value
				s.updateEstimate()
			}

			newPrefix := strings.ToLower(prefixEditor.Text())
			if newPrefix != s.prefix {
				s.prefix = newPrefix
				// Parse comma-separated prefixes
				var parsed []string
				for _, p := range strings.Split(newPrefix, ",") {
					p = strings.TrimSpace(p)
					if p != "" {
						parsed = append(parsed, p)
					}
				}
				s.prefixes = parsed
				s.updateEstimate()
			}

			// Handle link clicks
			for {
				e, ok := projectLink.Update(gtx.Source)
				if !ok {
					break
				}
				if e.Kind == gesture.KindClick {
					openURL("https://github.com/StormyCloudInc/Vanity-Generator/releases/tag/" + version.Version)
				}
			}
			for {
				e, ok := donateLink.Update(gtx.Source)
				if !ok {
					break
				}
				if e.Kind == gesture.KindClick {
					openURL("https://stormycloud.org/donate")
				}
			}
			for {
				e, ok := stormyLink.Update(gtx.Source)
				if !ok {
					break
				}
				if e.Kind == gesture.KindClick {
					openURL("https://stormycloud.org")
				}
			}

			layoutApp(gtx, th, s, &prefixEditor, &startBtn, &saveBtn, &coreSlider, maxCores, &gpuToggle, &netI2PBtn, &netTorBtn, &updateBannerBtn, &updateDismissBtn, &scrollList, &projectLink, &donateLink, &stormyLink)

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

func layoutApp(gtx layout.Context, th *material.Theme, s *state, prefixEditor *widget.Editor, startBtn, saveBtn *widget.Clickable, coreSlider *widget.Float, maxCores int, gpuToggle *widget.Bool, netI2PBtn, netTorBtn *widget.Clickable, updateBannerBtn, updateDismissBtn *widget.Clickable, scrollList *widget.List, projectLink, donateLink, stormyLink *gesture.Click) layout.Dimensions {
	// Fill window width with side padding
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4), Left: unit.Dp(20), Right: unit.Dp(20)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.N.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			// Sections: 0=header, 1=spacer, 2=update banner, 3=input card, 4=spacer, 5=results card, 6=bottom spacer
			const numSections = 7

			list := material.List(th, scrollList)
			list.Indicator.MinorWidth = unit.Dp(4)
			list.Indicator.Color = colorMuted
			return list.Layout(gtx, numSections, func(gtx layout.Context, index int) layout.Dimensions {
				switch index {
				case 0: // Header
					return layoutHeader(gtx, th, projectLink, donateLink, stormyLink)
				case 1: // Spacer after header
					return layout.Spacer{Height: unit.Dp(10)}.Layout(gtx)
				case 2: // Update banner (conditional)
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
				case 3: // Input card
					return layoutInputCard(gtx, th, s, prefixEditor, startBtn, coreSlider, maxCores, gpuToggle, netI2PBtn, netTorBtn)
				case 4: // Spacer between cards
					return layout.Spacer{Height: unit.Dp(14)}.Layout(gtx)
				case 5: // Results card
					return layoutResultsCard(gtx, th, s, saveBtn)
				case 6: // Bottom spacer
					return layout.Spacer{Height: unit.Dp(4)}.Layout(gtx)
				}
				return layout.Dimensions{}
			})
		})
	})
}

func layoutHeader(gtx layout.Context, th *material.Theme, projectLink, donateLink, stormyLink *gesture.Click) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
		// Title
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				lbl := material.H5(th, "Vanity Domain Generator")
				lbl.Color = colorText
				lbl.Font.Weight = font.Bold
				lbl.Alignment = text.Middle
				return lbl.Layout(gtx)
			})
		}),
		// Version (clickable) + donate row
		layout.Rigid(vspace(4)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Caption(th, version.Version)
						lbl.Color = colorAccent
						dims := lbl.Layout(gtx)
						area := clip.Rect{Max: dims.Size}.Push(gtx.Ops)
						pointer.CursorPointer.Add(gtx.Ops)
						projectLink.Add(gtx.Ops)
						area.Pop()
						return dims
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Caption(th, "  ·  ")
						lbl.Color = colorMuted
						return lbl.Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Caption(th, "Donate")
						lbl.Color = colorAccent
						dims := lbl.Layout(gtx)
						area := clip.Rect{Max: dims.Size}.Push(gtx.Ops)
						pointer.CursorPointer.Add(gtx.Ops)
						donateLink.Add(gtx.Ops)
						area.Pop()
						return dims
					}),
				)
			})
		}),
		// Powered by StormyCloud
		layout.Rigid(vspace(6)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Caption(th, "Powered by ")
						lbl.Color = colorMuted
						return lbl.Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Caption(th, "StormyCloud")
						lbl.Color = colorStormyBlue
						lbl.Font.Weight = font.SemiBold
						dims := lbl.Layout(gtx)
						area := clip.Rect{Max: dims.Size}.Push(gtx.Ops)
						pointer.CursorPointer.Add(gtx.Ops)
						stormyLink.Add(gtx.Ops)
						area.Pop()
						return dims
					}),
				)
			})
		}),
	)
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


func layoutInputCard(gtx layout.Context, th *material.Theme, s *state, prefixEditor *widget.Editor, startBtn *widget.Clickable, coreSlider *widget.Float, maxCores int, gpuToggle *widget.Bool, netI2PBtn, netTorBtn *widget.Clickable) layout.Dimensions {
	return cardWithBorder(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			// Network selector
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(sectionLabel(th, "NETWORK")),
					layout.Rigid(vspace(8)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layoutNetworkSelector(gtx, th, s, netI2PBtn, netTorBtn)
					}),
				)
			}),
			layout.Rigid(vspace(14)),

			// Target Prefix — force full width
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(sectionLabel(th, "TARGET PREFIX")),
					layout.Rigid(vspace(8)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return styledInput(gtx, th, prefixEditor, "e.g. hello or hello,hello2,hello3", !s.running)
					}),
				)
			}),
			// Validation
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if len(s.prefixes) == 0 {
					return layout.Dimensions{}
				}
				for _, p := range s.prefixes {
					if err := s.scheme.ValidatePrefix(p); err != nil {
						msg := fmt.Sprintf("%q: %s", p, err.Error())
						return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							lbl := material.Caption(th, msg)
							lbl.Color = color.NRGBA{R: 0xff, G: 0x44, B: 0x44, A: 0xff}
							return lbl.Layout(gtx)
						})
					}
				}
				return layout.Dimensions{}
			}),
			layout.Rigid(vspace(14)),

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

			// GPU Acceleration (only shown when GPU is available and scheme supports it)
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if !s.gpuAvailable || !s.scheme.SupportsGPU() {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: unit.Dp(18)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(sectionLabel(th, "GPU ACCELERATION")),
						layout.Rigid(vspace(8)),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									sw := material.Switch(th, gpuToggle, "Enable GPU")
									sw.Color.Enabled = colorAccent
									return sw.Layout(gtx)
								}),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									name := "Unknown GPU"
									if len(s.gpuDevices) > s.gpuDevice {
										name = s.gpuDevices[s.gpuDevice].Name
									}
									return layout.Inset{Left: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
										return badge(gtx, th, name)
									})
								}),
							)
						}),
					)
				})
			}),
			layout.Rigid(vspace(14)),

			// Start button — force full width
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				label := "Start Search"
				bg := colorAccent
				fg := color.NRGBA{R: 0x00, G: 0x00, B: 0x00, A: 0xff}
				if s.running {
					label = "Stop"
					bg = color.NRGBA{R: 0xdc, G: 0x26, B: 0x26, A: 0xff} // red
					fg = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
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

func layoutNetworkSelector(gtx layout.Context, th *material.Theme, s *state, i2pBtn, torBtn *widget.Clickable) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Flex{}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return segmentBtn(gtx, th, i2pBtn, "I2P (.b32.i2p)", s.network == address.NetworkI2P)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Spacer{Width: unit.Dp(8)}.Layout(gtx)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return segmentBtn(gtx, th, torBtn, "Tor v3 (.onion)", s.network == address.NetworkTorV3)
		}),
	)
}

func segmentBtn(gtx layout.Context, th *material.Theme, btn *widget.Clickable, label string, active bool) layout.Dimensions {
	bg := colorInputBg
	fg := colorLabel
	borderColor := colorInputBdr
	if active {
		bg = color.NRGBA{R: 0x00, G: 0x2a, B: 0x33, A: 0xff}
		fg = colorAccent
		borderColor = colorAccent
	}

	// Force fill allocated width
	gtx.Constraints.Min.X = gtx.Constraints.Max.X

	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			sz := gtx.Constraints.Min
			rr := gtx.Dp(6)
			rrect := clip.RRect{Rect: image.Rect(0, 0, sz.X, sz.Y), NE: rr, NW: rr, SE: rr, SW: rr}
			paint.FillShape(gtx.Ops, bg, rrect.Op(gtx.Ops))
			paint.FillShape(gtx.Ops, borderColor, clip.Stroke{Path: rrect.Path(gtx.Ops), Width: 1}.Op())
			return layout.Dimensions{Size: sz}
		}),
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return material.Clickable(gtx, btn, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(18), Bottom: unit.Dp(18)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						lbl := material.Body2(th, label)
						lbl.Color = fg
						lbl.Font.Weight = font.SemiBold
						lbl.Alignment = text.Middle
						return lbl.Layout(gtx)
					})
				})
			})
		}),
	)
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
	elapsed := s.elapsed
	checked := s.checked
	estimate := s.estimate
	result := s.result
	hasResult := s.lastResult != nil
	s.mu.Unlock()

	return cardWithBorder(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			// Row 1: STATUS | EST. TIME
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

			// Row 2: SPEED | ELAPSED
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if speed == "" && elapsed == "" {
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
							return statBox(gtx, th, "ELAPSED", elapsed)
						}),
					)
				})
			}),

			// Row 3: CHECKED (full width)
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if checked == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return statBox(gtx, th, "CHECKED", checked)
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
	// Find shortest valid prefix for estimate
	shortestLen := 0
	for _, p := range s.prefixes {
		if s.scheme.ValidatePrefix(p) == nil {
			if shortestLen == 0 || len(p) < shortestLen {
				shortestLen = len(p)
			}
		}
	}
	if shortestLen == 0 {
		s.mu.Lock()
		s.estimate = "Awaiting input..."
		s.mu.Unlock()
		return
	}
	attempts := s.scheme.EstimateAttempts(shortestLen)

	var keysPerSec float64
	switch s.scheme.Network() {
	case address.NetworkI2P:
		keysPerSec = 500_000.0 * float64(s.cores)
		if s.useGPU && s.gpuAvailable && s.scheme.SupportsGPU() {
			keysPerSec += 100_000_000.0
		}
	case address.NetworkTorV3:
		keysPerSec = 300_000.0 * float64(s.cores)
		if s.useGPU && s.gpuAvailable && s.scheme.SupportsGPU() {
			keysPerSec += 300_000.0 // GPU worker is CPU-precompute bound
		}
	}

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
	// Validate all prefixes
	if len(s.prefixes) == 0 {
		return
	}
	for _, p := range s.prefixes {
		if s.scheme.ValidatePrefix(p) != nil {
			return
		}
	}

	s.mu.Lock()
	s.running = true
	s.status = "Searching..."
	s.speed = ""
	s.elapsed = ""
	s.checked = ""
	s.result = ""
	s.lastResult = nil
	s.mu.Unlock()

	gen := generator.New(s.scheme, s.prefixes, s.cores, s.useGPU, s.gpuDevice)
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
			s.elapsed = formatDuration(stats.Elapsed)
			s.checked = formatUint(stats.Checked)
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
			s.mu.Unlock()
			s.updateEstimate()
			w.Invalidate()
		}
	}()
}

func (s *state) stop() {
	s.mu.Lock()
	s.running = false
	s.status = "Stopped"
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.updateEstimate()
}

func (s *state) save() {
	s.mu.Lock()
	r := s.lastResult
	network := s.network
	s.mu.Unlock()
	if r == nil {
		return
	}

	// Strip suffix to get a short name for the file/dir
	addr := r.Candidate.Address()
	if len(addr) > 16 {
		addr = addr[:16]
	}

	// Save next to the executable, not the working directory
	exePath, err := os.Executable()
	if err != nil {
		s.mu.Lock()
		s.status = "Save error: " + err.Error()
		s.mu.Unlock()
		return
	}
	exeDir := filepath.Dir(exePath)

	var savePath string
	switch network {
	case address.NetworkTorV3:
		// Tor v3: save as a hidden service directory
		savePath = filepath.Join(exeDir, "vanity_"+addr)
		if err := r.Candidate.SaveKeys(savePath); err != nil {
			s.mu.Lock()
			s.status = "Save error: " + err.Error()
			s.mu.Unlock()
			return
		}
		s.mu.Lock()
		s.status = "Keys saved to " + savePath
		s.mu.Unlock()
	default:
		// I2P: save as a .dat file
		savePath = filepath.Join(exeDir, "vanity_"+addr+".dat")
		if err := r.Candidate.SaveKeys(savePath); err != nil {
			s.mu.Lock()
			s.status = "Save error: " + err.Error()
			s.mu.Unlock()
			return
		}
		s.mu.Lock()
		s.status = "Keys saved to " + savePath
		s.mu.Unlock()
	}
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

func openURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
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
