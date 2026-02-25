package ui

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"github.com/go-i2p/i2p-vanitygen/internal/destination"
	"github.com/go-i2p/i2p-vanitygen/internal/generator"
)

// App holds the UI state.
type App struct {
	window    fyne.Window
	generator *generator.Generator
	cancel    context.CancelFunc
	running   bool
	lastResult *generator.Result
}

// New creates and returns the main application window.
func New(app fyne.App) *App {
	w := app.NewWindow("I2P Vanity Address Generator")
	w.Resize(fyne.NewSize(520, 480))
	a := &App{window: w}
	a.buildUI()
	return a
}

// Show displays the window.
func (a *App) Show() {
	a.window.ShowAndRun()
}

func (a *App) buildUI() {
	// --- Prefix input ---
	prefixEntry := widget.NewEntry()
	prefixEntry.SetPlaceHolder("e.g. hello")

	prefixValidation := widget.NewLabel("")

	// --- Core selector ---
	maxCores := runtime.NumCPU()
	coreOptions := make([]string, maxCores)
	for i := 0; i < maxCores; i++ {
		coreOptions[i] = strconv.Itoa(i + 1)
	}
	coreSelect := widget.NewSelect(coreOptions, nil)
	coreSelect.SetSelected(strconv.Itoa(maxCores))

	// --- Estimate label ---
	estimateLabel := widget.NewLabel("Enter a prefix to see time estimate")

	// --- Progress labels ---
	statusLabel := widget.NewLabel("Idle")
	speedLabel := widget.NewLabel("")
	checkedLabel := widget.NewLabel("")

	// --- Result display ---
	resultLabel := widget.NewLabel("")
	resultLabel.Wrapping = fyne.TextWrapBreak

	// --- Save button ---
	saveBtn := widget.NewButton("Save Keys", nil)
	saveBtn.Disable()

	// --- Start/Stop button ---
	startBtn := widget.NewButton("Start", nil)

	// Update estimate when prefix changes
	prefixEntry.OnChanged = func(s string) {
		s = strings.ToLower(s)
		if len(s) == 0 {
			prefixValidation.SetText("")
			estimateLabel.SetText("Enter a prefix to see time estimate")
			return
		}
		if err := destination.ValidatePrefix(s); err != nil {
			prefixValidation.SetText(err.Error())
			estimateLabel.SetText("")
			return
		}
		prefixValidation.SetText("")

		cores, _ := strconv.Atoi(coreSelect.Selected)
		if cores == 0 {
			cores = maxCores
		}
		updateEstimate(estimateLabel, len(s), cores)
	}

	coreSelect.OnChanged = func(s string) {
		prefix := strings.ToLower(prefixEntry.Text)
		if len(prefix) == 0 || destination.ValidatePrefix(prefix) != nil {
			return
		}
		cores, _ := strconv.Atoi(s)
		if cores == 0 {
			cores = maxCores
		}
		updateEstimate(estimateLabel, len(prefix), cores)
	}

	// Save button handler
	saveBtn.OnTapped = func() {
		if a.lastResult == nil {
			return
		}
		d := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
			if err != nil || writer == nil {
				return
			}
			path := writer.URI().Path()
			writer.Close()
			if saveErr := a.lastResult.Destination.SaveKeys(path); saveErr != nil {
				dialog.ShowError(saveErr, a.window)
				return
			}
			dialog.ShowInformation("Saved", "Keys saved to "+path, a.window)
		}, a.window)
		d.SetFileName("vanitygen.dat")
		d.SetFilter(storage.NewExtensionFileFilter([]string{".dat"}))
		d.Show()
	}

	// Start/Stop button handler
	startBtn.OnTapped = func() {
		if a.running {
			a.stop()
			startBtn.SetText("Start")
			statusLabel.SetText("Stopped")
			return
		}

		prefix := strings.ToLower(prefixEntry.Text)
		if err := destination.ValidatePrefix(prefix); err != nil {
			prefixValidation.SetText(err.Error())
			return
		}

		cores, _ := strconv.Atoi(coreSelect.Selected)
		if cores == 0 {
			cores = maxCores
		}

		a.lastResult = nil
		saveBtn.Disable()
		resultLabel.SetText("")
		startBtn.SetText("Stop")
		statusLabel.SetText("Searching...")
		speedLabel.SetText("")
		checkedLabel.SetText("")
		prefixEntry.Disable()
		coreSelect.Disable()

		a.start(prefix, cores, statusLabel, speedLabel, checkedLabel, resultLabel, saveBtn, startBtn, prefixEntry, coreSelect)
	}

	// --- Layout ---
	form := container.NewVBox(
		widget.NewLabelWithStyle("I2P Vanity Address Generator", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),

		widget.NewForm(
			widget.NewFormItem("Vanity Prefix", prefixEntry),
			widget.NewFormItem("", prefixValidation),
			widget.NewFormItem("CPU Cores", coreSelect),
			widget.NewFormItem("Est. Time", estimateLabel),
		),

		widget.NewSeparator(),
		container.NewHBox(startBtn, layout.NewSpacer(), saveBtn),
		widget.NewSeparator(),

		widget.NewLabelWithStyle("Status", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		statusLabel,
		speedLabel,
		checkedLabel,

		widget.NewSeparator(),
		widget.NewLabelWithStyle("Result", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		resultLabel,
	)

	a.window.SetContent(container.NewPadded(form))
}

func (a *App) start(prefix string, cores int, statusLabel, speedLabel, checkedLabel, resultLabel *widget.Label, saveBtn, startBtn *widget.Button, prefixEntry *widget.Entry, coreSelect *widget.Select) {
	a.running = true
	gen := generator.New(prefix, cores)
	a.generator = gen

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	resultCh, statsCh := gen.Start(ctx)

	// Listen for stats updates
	go func() {
		for stats := range statsCh {
			s := stats
			speedLabel.SetText(fmt.Sprintf("Speed: %s keys/sec", formatNumber(s.KeysPerSec)))
			checkedLabel.SetText(fmt.Sprintf("Checked: %s | Elapsed: %s", formatUint(s.Checked), formatDuration(s.Elapsed)))
		}
	}()

	// Listen for result
	go func() {
		for result := range resultCh {
			r := result
			a.lastResult = &r
			resultLabel.SetText(r.Address)
			statusLabel.SetText(fmt.Sprintf("Found in %s (%s attempts)", formatDuration(r.Duration), formatUint(r.Attempts)))
			saveBtn.Enable()
			startBtn.SetText("Start")
			prefixEntry.Enable()
			coreSelect.Enable()
			a.running = false
		}
	}()
}

func (a *App) stop() {
	a.running = false
	if a.cancel != nil {
		a.cancel()
	}
	if a.generator != nil {
		a.generator.Stop()
	}
}

func updateEstimate(label *widget.Label, prefixLen, cores int) {
	attempts := destination.EstimateAttempts(prefixLen)
	// Rough estimate: ~500K keys/sec per core (conservative)
	keysPerSec := 500_000.0 * float64(cores)
	seconds := attempts / keysPerSec

	if seconds < 1 {
		label.SetText("< 1 second")
	} else {
		label.SetText("~" + formatDuration(time.Duration(seconds*float64(time.Second))))
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

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return "< 1s"
	}
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60

	if h > 24 {
		days := h / 24
		h = h % 24
		return fmt.Sprintf("%dd %dh %dm", days, h, m)
	}
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
