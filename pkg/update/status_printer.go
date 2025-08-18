package update

import (
	"fmt"
	"github.com/foundriesio/composeapp/pkg/compose"
	"strings"
	"time"
)

func GetInitProgressPrinter() func(status *InitProgress) {
	var stateSwitch bool
	return func(status *InitProgress) {
		switch status.State {
		case UpdateInitStateLoadingTree:
			{
				pct := float64(status.Current) / float64(status.Total)
				fmt.Printf("\r\033[KLoading app metadata:\t\t\t %4.0f%%  %s %d/%d",
					pct*100, renderBar(pct, 25), status.Current, status.Total)
			}
		case UpdateInitStateCheckingBlobs:
			{
				if !stateSwitch {
					fmt.Println()
					stateSwitch = true
				}
				pct := float64(status.Current) / float64(status.Total)
				fmt.Printf("\r\033[KChecking app blobs & calculating diff:\t %4.0f%%  %s %d/%d",
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
		start       time.Time
		smoothedETA time.Duration
		etaStr      = na
		curSpeedStr = na
		avgSpeedStr = na
	)

	return func(status *compose.FetchProgress) {
		var blobsBeingFetched int
		var curSpeedTotal int64
		var avgSpeedTotal int64
		for _, b := range status.Blobs {
			if b.State == compose.BlobOk || b.FetchStartTime.IsZero() {
				// Blob is already fetched or has not being started yet
				continue
			}
			// Count blobs that are currently being fetched
			blobsBeingFetched++
			// Calculate the total current speed of all blobs being fetched
			curSpeedTotal += b.ReadSpeedCur
			// Calculate the total average speed of all blobs being fetched
			avgSpeedTotal += b.ReadSpeedAvg
			// Set the fetch start time to the start time of the first blob if not set
			if start.IsZero() || (!b.FetchStartTime.IsZero() && b.FetchStartTime.Before(start)) {
				start = b.FetchStartTime
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
		} else if smoothedETA > 0 {
			etaStr = smoothedETA.Round(time.Second).String()
		}
		if curSpeed > 0 {
			curSpeedStr = fmt.Sprintf("%s/s", compose.FormatBytesInt64(int64(curSpeed)))
		}
		if avgSpeed > 0 {
			avgSpeedStr = fmt.Sprintf("%s/s", compose.FormatBytesInt64(int64(avgSpeed)))
		}

		// Print the progress line
		fmt.Printf("\r\033[K%4.0f%%  %s  %9s / %-9s | %d/%d blobs | Cur: %11s | Avg: %11s | Time: %s | ETA: %s",
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

func renderBar(pct float64, width int) string {
	if pct > 1 {
		pct = 1
	}
	filled := int(pct * float64(width))
	return "[" + strings.Repeat("=", filled) + strings.Repeat(" ", width-filled) + "]"
}
