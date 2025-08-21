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
	imageLoadingContext struct {
		curImageID string
		curLayerID string
	}
)

var (
	isTty = isTTY()
)

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

func GetFetchProgressPrinter() func(status *compose.FetchProgress) {
	const (
		etaAlpha = 0.5 // smoothing factor: 0=no change, 1=instant change
		na       = "--"
	)
	var (
		start        time.Time
		smoothedETA  time.Duration
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
			}
			if b.BytesFetched >= b.Descriptor.Size {
				fetchedBlobs[d] = struct{}{} // mark blob as fetched
			}
		}
		var curSpeed int64
		var avgSpeed int64
		if blobsBeingFetched > 0 {
			curSpeed = curSpeedTotal / int64(blobsBeingFetched)
			avgSpeed = avgSpeedTotal / int64(blobsBeingFetched)
		}

		// progress in percentage
		pct := float64(status.CurrentBytes) / float64(status.TotalBytes)

		// calculate elapsed time if fetch of at least one blob has started
		var elapsed time.Duration
		if !start.IsZero() {
			elapsed = time.Since(start).Round(time.Second)
		}
		var eta time.Duration
		if curSpeed > 0 {
			eta = time.Duration((status.TotalBytes-status.CurrentBytes)/curSpeed) * time.Second
			// apply smoothing if we already have a previous value
			if smoothedETA != 0 {
				eta = time.Duration(float64(smoothedETA)*(1-etaAlpha) + float64(eta)*etaAlpha)
			}
			smoothedETA = eta
		}

		if status.CurrentBytes >= status.TotalBytes {
			etaStr = time.Now().UTC().Format(time.TimeOnly) + " (done)\n"
			done = true
		} else if curSpeed > 0 {
			etaStr = smoothedETA.Round(time.Second).String()
		} else {
			etaStr = "∞"
		}
		curSpeedStr = fmt.Sprintf("%s/s", compose.FormatBytesInt64(int64(curSpeed)))
		if avgSpeed > 0 {
			avgSpeedStr = fmt.Sprintf("%s/s", compose.FormatBytesInt64(int64(avgSpeed)))
		}

		// Print the progress line
		printf("%4.0f%%  %s  %9s / %-9s | %d/%d blobs | Cur: %11s | Avg: %11s | Time: %s | ETA: %s",
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

func GetInstallProgressPrinter() func(status *compose.InstallProgress) {

	ctx := &imageLoadingContext{}

	return func(p *compose.InstallProgress) {
		// TODO: handle and render info about the compose.AppInstallStateComposeChecking state
		switch p.AppInstallState {
		case compose.AppInstallStateComposeInstalling:
			{
				fmt.Printf("Installing app %s\n", p.AppID)
			}
		case compose.AppInstallStateImagesLoading:
			{
				renderImageLoadingProgress(ctx, p)
			}
		}
	}
}

func renderImageLoadingProgress(ctx *imageLoadingContext, p *compose.InstallProgress) {
	switch p.ImageLoadState {
	case compose.ImageLoadStateLayerLoading:
		{
			if ctx.curImageID != p.ImageID {
				fmt.Printf("  Loading image %s", p.ImageID)
				ctx.curImageID = p.ImageID
				ctx.curLayerID = ""
			}
			if ctx.curLayerID != p.ID {
				ctx.curLayerID = p.ID
				fmt.Println()
			}

			pct := float64(p.Current) / float64(p.Total)
			printf("\t%s %4.0f%%  %s", p.ID, pct*100, renderBar(pct, 25))
		}
	case compose.ImageLoadStateLayerSyncing:
		{
			// TODO: render layer syncing progress
		}
	case compose.ImageLoadStateLayerLoaded:
		{
			ctx.curLayerID = ""
		}
	case compose.ImageLoadStateImageLoaded:
		{
			fmt.Printf("\n  Image loaded: %s\n", p.ImageID)
		}
	case compose.ImageLoadStateImageExist:
		{
			fmt.Printf("  Already exists: %s\n", p.ImageID)
		}
	default:
		fmt.Printf("  Unknown state %s\n", p.ImageLoadState)
	}
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
