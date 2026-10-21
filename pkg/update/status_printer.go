package update

import (
	"fmt"
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/moby/term"
	"github.com/opencontainers/go-digest"
	"os"
	"strings"
	"time"
)

type (
	// layerRow is one line of the table of an image being loaded.
	layerRow struct {
		id     string
		size   int64     // the compressed blob, 0 when unknown
		pct    float64   // 0..1
		status string    // "exists", the seconds it has been loading or took, or blank
		since  time.Time // when its loading was first reported
	}

	// imageTable is the table of an image: its rows and what the summary of the image needs.
	imageTable struct {
		rows     []layerRow
		index    map[string]int // row of a layer id
		started  time.Time
		finished time.Time // when the last row reached its final form
		existed  int
		drawn    int  // rows on the terminal, redrawn in place on the next event
		plain    bool // a row did not fit the terminal: final rows only from then on
	}

	// imageLoadingContext keeps the table of every image reported loading and not yet loaded:
	// the install into containerd reports every image loaded only after the last one is unpacked.
	imageLoadingContext struct {
		images map[string]*imageTable
		cur    string // the image whose table is drawn in place
	}

	ProgressPrinterOpts struct {
		// Indentation specifies the number of spaces to indent each line
		Indentation int
	}
	ProgressPrinterOption func(*ProgressPrinterOpts)
)

var (
	isTty    = isTTY()
	ttyWidth = terminalWidth()
	now      = time.Now
)

func WithIndentation(indentation int) ProgressPrinterOption {
	return func(opts *ProgressPrinterOpts) {
		opts.Indentation = indentation
	}
}

func GetInitProgressPrinter() func(status *InitProgress) {
	var stateSwitch bool
	return func(status *InitProgress) {
		switch status.State {
		case UpdateInitStateLoadingTree:
			{
				pct := float64(status.Current) / float64(status.Total)
				printf("Loading app metadata:\t\t\t %4.0f%%  %s %d/%d",
					pct*100, renderBar(pct, 25), status.Current, status.Total)
			}
		case UpdateInitStateCheckingBlobs:
			{
				if !stateSwitch {
					fmt.Println()
					stateSwitch = true
				}
				pct := float64(status.Current) / float64(status.Total)
				printf("Checking app blobs & calculating diff:\t %4.0f%%  %s %d/%d",
					pct*100, renderBar(pct, 25), status.Current, status.Total)
				if status.Current == status.Total {
					fmt.Println()
				}
			}
		}
	}
}

func GetFetchProgressPrinter(options ...ProgressPrinterOption) func(status *compose.FetchProgress) {
	opts := parseProgressPrinterOptions(options...)
	const (
		na = "--"
	)
	var (
		start        time.Time
		startBytes   int64
		etaStr       = na
		curSpeedStr  = na
		avgSpeedStr  = na
		done         = false
		fetchedBlobs = make(map[digest.Digest]interface{})
	)

	return func(status *compose.FetchProgress) {
		if done {
			return
		}
		var blobsBeingFetched int
		var curSpeedTotal int64
		var avgSpeedTotal int64
		for d, b := range status.Blobs {
			if _, ok := fetchedBlobs[d]; ok {
				// blob has already been fetched, skip it
				continue
			}
			// Count blobs that are currently being fetched or have been completely fetched since the last print
			blobsBeingFetched++
			// Calculate the total current speed of all blobs being fetched or have been completely fetched since the last print
			curSpeedTotal += b.ReadSpeedCur
			// Calculate the total average speed of all blobs being fetched or have been completely fetched since the last print
			avgSpeedTotal += b.ReadSpeedAvg
			// Set the fetch start time to the start time of the first blob if not set
			if start.IsZero() || (!b.FetchStartTime.IsZero() && b.FetchStartTime.Before(start)) {
				start = b.FetchStartTime
				startBytes = status.CurrentBytes
			}
			if b.BytesFetched >= b.Descriptor.Size {
				fetchedBlobs[d] = struct{}{} // mark blob as fetched
			}
		}
		var curSpeed int64
		if blobsBeingFetched > 0 {
			curSpeed = curSpeedTotal / int64(blobsBeingFetched)
		}

		// progress in percentage
		pct := float64(status.CurrentBytes) / float64(status.TotalBytes)

		// calculate elapsed time if fetch of at least one blob has started
		var elapsed time.Duration
		var avgSpeed int64
		if !start.IsZero() {
			elapsed = time.Since(start).Round(time.Second)
			if elapsed > 0 {
				avgSpeed = (status.CurrentBytes - startBytes) / int64(elapsed.Seconds())
			}
		}
		var eta time.Duration

		if avgSpeed > 0 {
			estimatedSpeed := avgSpeed
			if curSpeed > 0 {
				estimatedSpeed = int64(float64(avgSpeed)*0.6 + float64(curSpeed)*0.4)
			}
			eta = time.Duration((status.TotalBytes-status.CurrentBytes)/estimatedSpeed) * time.Second
		}

		if status.CurrentBytes >= status.TotalBytes {
			etaStr = time.Now().UTC().Format(time.TimeOnly) + " (done)\n"
			done = true
		} else if avgSpeed > 0 {
			etaStr = eta.Round(time.Second).String()
		} else {
			etaStr = "∞"
		}
		curSpeedStr = fmt.Sprintf("%s/s", compose.FormatBytesInt64(int64(curSpeed)))
		if avgSpeed > 0 {
			avgSpeedStr = fmt.Sprintf("%s/s", compose.FormatBytesInt64(int64(avgSpeed)))
		}

		// Print the progress line
		printf("%*s%4.0f%%  %s  %9s / %-9s | %d/%d blobs | Cur: %11s | Avg: %11s | Time: %s | ETA: %s",
			opts.Indentation, "",
			pct*100,
			renderBar(pct, 25),
			compose.FormatBytesInt64(status.CurrentBytes),
			compose.FormatBytesInt64(status.TotalBytes),
			status.FetchedCount,
			len(status.Blobs),
			curSpeedStr,
			avgSpeedStr,
			elapsed.Round(time.Second),
			etaStr,
		)
	}
}

func GetInstallProgressPrinter(options ...ProgressPrinterOption) func(status *compose.InstallProgress) {
	opts := parseProgressPrinterOptions(options...)

	ctx := &imageLoadingContext{}

	return func(p *compose.InstallProgress) {
		// TODO: handle and render info about the compose.AppInstallStateComposeChecking state
		switch p.AppInstallState {
		case compose.AppInstallStateComposeInstalling:
			{
				fmt.Printf("%*sInstalling app %s\n", opts.Indentation, "", p.AppID)
			}
		case compose.AppInstallStateImagesLoading:
			{
				renderImageLoadingProgress(ctx, p, opts.Indentation)
			}
		}
	}
}

// renderImageLoadingProgress prints the images as the install reports them: a header per image,
// then a table with one row per layer and, once the image is loaded, a line with the count of its
// layers, how many of them existed and the time its loading took. On a TTY wide enough for its
// rows the table of the image reported last is redrawn in place on every event, so the rows of
// the layers announced up front fill as they load; otherwise only the final row of each layer is
// printed.
func renderImageLoadingProgress(ctx *imageLoadingContext, p *compose.InstallProgress, indentation int) {
	switch p.ImageLoadState {
	case compose.ImageLoadStateImageExist:
		fmt.Printf("%*sAlready exists: %s\n", indentation, "", p.ImageID)
	case compose.ImageLoadStateImageLoading:
		ctx.table(p.ImageID, indentation)
	case compose.ImageLoadStateImageLoaded:
		t := ctx.table(p.ImageID, indentation)
		fmt.Printf("%*sImage loaded: %s  (%s)\n", indentation, "", p.ImageID, t.summary())
		delete(ctx.images, p.ImageID)
	case compose.ImageLoadStateLayerExist, compose.ImageLoadStateLayerPending, compose.ImageLoadStateLayerLoading,
		compose.ImageLoadStateLayerSyncing, compose.ImageLoadStateLayerLoaded:
		t := ctx.table(p.ImageID, indentation)
		final := t.updateRow(p)
		if isTty && p.ImageID == ctx.cur && t.redraw(indentation) {
			return
		}
		if final {
			fmt.Println(formatRow(&t.rows[t.index[p.ID]], indentation))
		}
	default:
		fmt.Printf("  Unknown state %s\n", p.ImageLoadState)
	}
}

// table returns the table of an image, printing the header of the image and starting its table
// when it has none.
func (ctx *imageLoadingContext) table(imageID string, indentation int) *imageTable {
	if t := ctx.images[imageID]; t != nil {
		return t
	}
	fmt.Printf("%*sLoading image %s\n", indentation, "", imageID)
	if ctx.images == nil {
		ctx.images = map[string]*imageTable{}
	}
	t := &imageTable{index: map[string]int{}, started: now()}
	ctx.images[imageID], ctx.cur = t, imageID
	return t
}

// updateRow applies a layer event to the layer's row, adding the row when the layer is new, and
// reports whether the row reached its final form. A layer announced as existing or pending more
// than once keeps the row it has; one announced as existing and then loaded once more is no
// longer counted as existing. A row shows how long its layer has been loading until the layer
// is loaded with the time that took; a loaded layer without a time shows none.
func (t *imageTable) updateRow(p *compose.InstallProgress) bool {
	i, known := t.index[p.ID]
	if !known {
		i = len(t.rows)
		t.rows = append(t.rows, layerRow{id: p.ID})
		t.index[p.ID] = i
	}
	row := &t.rows[i]
	if p.Size > 0 {
		row.size = p.Size
	}
	switch p.ImageLoadState {
	case compose.ImageLoadStateLayerExist:
		if known {
			return false
		}
		row.pct, row.status = 1, "exists"
		t.existed++
		t.finished = now()
		return true
	case compose.ImageLoadStateLayerPending:
		return false
	}
	if row.status == "exists" {
		t.existed--
	}
	switch p.ImageLoadState {
	case compose.ImageLoadStateLayerLoading, compose.ImageLoadStateLayerSyncing:
		now := now()
		if row.since.IsZero() {
			row.since = now
		}
		if p.Total > 0 {
			row.pct = float64(p.Current) / float64(p.Total)
		}
		row.status = seconds(now.Sub(row.since))
		return false
	case compose.ImageLoadStateLayerLoaded:
		row.pct, row.status = 1, ""
		if p.Current > 0 {
			row.status = seconds(time.Duration(p.Current) * time.Millisecond)
		}
		t.finished = now()
		return true
	}
	return false
}

// redraw prints the table over the rows drawn before, the cursor up to the first of them and
// every row cleared and printed again, and reports whether it did: a row wider than the terminal
// would wrap and break the count of the rows to go up, so from the first such row on the table
// is not drawn at all.
func (t *imageTable) redraw(indentation int) bool {
	if t.plain {
		return false
	}
	lines := make([]string, len(t.rows))
	for i := range t.rows {
		lines[i] = formatRow(&t.rows[i], indentation)
		// The tab of a row takes eight columns.
		if ttyWidth > 0 && len(lines[i])+7 > ttyWidth {
			t.plain = true
			return false
		}
	}
	if t.drawn > 0 {
		fmt.Printf("\033[%dA", t.drawn)
	}
	for _, line := range lines {
		fmt.Printf("\r\033[K%s\n", line)
	}
	t.drawn = len(lines)
	return true
}

// summary describes the loaded image: its layers, those that existed when there were any, and
// the time from its loading being reported to its last layer loaded.
func (t *imageTable) summary() string {
	layers := "layers"
	if len(t.rows) == 1 {
		layers = "layer"
	}
	existed := ""
	if t.existed > 0 {
		existed = fmt.Sprintf("%d existed, ", t.existed)
	}
	finished := t.finished
	if finished.IsZero() {
		finished = now()
	}
	return fmt.Sprintf("%d %s, %s%s", len(t.rows), layers, existed, seconds(finished.Sub(t.started)))
}

// formatRow renders a layer's row: its id, a bar with the percent, the size of its blob when
// known and its status, without trailing blanks.
func formatRow(row *layerRow, indentation int) string {
	size := ""
	if row.size > 0 {
		size = humanSize(row.size)
	}
	pct := max(0, min(row.pct, 1))
	line := fmt.Sprintf("%*s\t%s  %s %3.0f%%  %9s  %s", indentation, "", row.id, renderBar(pct, 25), pct*100, size, row.status)
	return strings.TrimRight(line, " ")
}

func seconds(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// humanSize renders a size in bytes with the largest binary unit that fits, with one decimal
// from KiB on.
func humanSize(n int64) string {
	units := []string{"KiB", "MiB", "GiB"}
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	size, unit := float64(n)/1024, units[0]
	for i := 1; i < len(units) && size >= 1024; i++ {
		size, unit = size/1024, units[i]
	}
	return fmt.Sprintf("%.1f %s", size, unit)
}

func printf(format string, a ...any) {
	if isTty {
		fmt.Printf("\r\033[K") // clear the current line if we are in a TTY
	} else {
		fmt.Print("\n") // just print a new line if not in a TTY
	}
	fmt.Printf(format, a...)
}

func renderBar(pct float64, width int) string {
	if pct > 1 {
		pct = 1
	}
	filled := int(pct * float64(width))
	return "[" + strings.Repeat("=", filled) + strings.Repeat(" ", width-filled) + "]"
}

func isTTY() bool {
	return term.IsTerminal(os.Stdout.Fd()) || os.Getenv("PARENT_HAS_TTY") == "1"
}

// terminalWidth returns the columns of the terminal on stdout, 0 when there is none or it is unknown.
func terminalWidth() int {
	ws, err := term.GetWinsize(os.Stdout.Fd())
	if err != nil {
		return 0
	}
	return int(ws.Width)
}

func parseProgressPrinterOptions(options ...ProgressPrinterOption) *ProgressPrinterOpts {
	opts := &ProgressPrinterOpts{}
	for _, option := range options {
		option(opts)
	}
	return opts
}
