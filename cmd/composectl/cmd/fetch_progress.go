package composectl

import (
	"fmt"
	"io"
	"sort"
	"sync/atomic"
	"time"

	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/opencontainers/go-digest"
)

// fetchProgressRenderer renders per-blob download progress. In a TTY it keeps
// only the in-flight blobs in an in-place redrawn region (bounded by the worker
// count) and "graduates" a blob to a single permanent line above that region
// once it completes. Because the live region never exceeds the worker count it
// cannot outgrow the terminal height, so the cursor-up redraw never clamps at
// the top of the screen (the defect that caused the same blob to be reprinted
// on several lines). Without a TTY each blob gets a line when it crosses each
// 10% of its size and a final completion line, so log collectors such as
// journalctl still depict download progress.
//
// Render is invoked serially from the progress reporter goroutine, so the
// renderer state needs no locking.
type fetchProgressRenderer struct {
	out       io.Writer
	isTty     bool
	live      []*compose.BlobFetchProgress // in-flight blobs currently redrawn, in stable order
	seen      map[digest.Digest]bool       // blobs ever added to the live set
	liveCount int                          // number of live lines drawn in the previous frame
	reported  map[digest.Digest]int64      // last reported progress decile per in-flight blob (non-TTY)
}

func NewFetchProgressRenderer(out io.Writer, isTty bool) *fetchProgressRenderer {
	return &fetchProgressRenderer{
		out:      out,
		isTty:    isTty,
		seen:     map[digest.Digest]bool{},
		reported: map[digest.Digest]int64{},
	}
}

// Render consumes one progress snapshot and updates the terminal accordingly.
func (r *fetchProgressRenderer) Render(p *compose.FetchProgress) {
	done, live := r.track(p)
	if r.isTty {
		r.renderTty(done, live)
		return
	}
	r.renderPlain(done, live)
}

// track folds the latest snapshot into the live set: newly observed in-flight
// blobs are appended (deterministically ordered), and the live set is split into
// blobs that have just completed and those still in flight. r.live is left
// holding only the still-in-flight blobs.
func (r *fetchProgressRenderer) track(p *compose.FetchProgress) (done, live []*compose.BlobFetchProgress) {
	var newcomers []*compose.BlobFetchProgress
	for _, b := range p.Blobs {
		if r.seen[b.Descriptor.Digest] {
			continue
		}
		if b.State == compose.BlobFetching || b.State == compose.BlobOk {
			r.seen[b.Descriptor.Digest] = true
			newcomers = append(newcomers, b)
		}
	}
	sort.Slice(newcomers, func(i, j int) bool {
		if !newcomers[i].FetchStartTime.Equal(newcomers[j].FetchStartTime) {
			return newcomers[i].FetchStartTime.Before(newcomers[j].FetchStartTime)
		}
		return newcomers[i].Descriptor.Digest < newcomers[j].Descriptor.Digest
	})
	r.live = append(r.live, newcomers...)

	for _, b := range r.live {
		if atomic.LoadInt64(&b.BytesFetched) >= b.Descriptor.Size {
			done = append(done, b)
		} else {
			live = append(live, b)
		}
	}
	r.live = live
	return done, live
}

// renderTty graduates completed blobs to permanent lines and redraws the live
// region in place. The leading cursor-up only covers the live region, so a
// terminal scroll of the permanent history above it is harmless.
func (r *fetchProgressRenderer) renderTty(done, live []*compose.BlobFetchProgress) {
	if r.liveCount > 0 {
		fmt.Fprintf(r.out, "\033[%dA", r.liveCount)
	}
	// Return to column 0 and clear from here to the end of the screen so stale
	// live lines do not linger.
	fmt.Fprint(r.out, "\r\033[J")
	for _, b := range done {
		fmt.Fprintf(r.out, " %s\n", formatBlobLine(b))
	}
	for _, b := range live {
		fmt.Fprintf(r.out, " %s\n", formatBlobLine(b))
	}
	r.liveCount = len(live)
}

// renderPlain handles non-TTY output (pipes, logs): cursor control is
// meaningless, so each in-flight blob gets a line when it first appears and
// then whenever it crosses another 10% of its size, plus one final line when
// it completes.
func (r *fetchProgressRenderer) renderPlain(done, live []*compose.BlobFetchProgress) {
	for _, b := range done {
		delete(r.reported, b.Descriptor.Digest)
		fmt.Fprintf(r.out, " %s\n", formatBlobLine(b))
	}
	for _, b := range live {
		if r.crossedDecile(b) {
			fmt.Fprintf(r.out, " %s\n", formatBlobLine(b))
		}
	}
}

// crossedDecile reports whether the blob has entered a 10%-of-size bucket that
// has not been reported yet, recording the new bucket if so. The first call for
// a blob always reports, so its download start shows up in the log.
func (r *fetchProgressRenderer) crossedDecile(b *compose.BlobFetchProgress) bool {
	if b.Descriptor.Size <= 0 {
		return false
	}
	decile := 100 * atomic.LoadInt64(&b.BytesFetched) / b.Descriptor.Size / 10
	last, ok := r.reported[b.Descriptor.Digest]
	if ok && decile <= last {
		return false
	}
	r.reported[b.Descriptor.Digest] = decile
	return true
}

// formatBlobLine renders a single blob's status line. All cross-goroutine
// counters are read via atomic loads since the fetch workers update them while
// this runs on the reporter goroutine.
func formatBlobLine(b *compose.BlobFetchProgress) string {
	fetched := atomic.LoadInt64(&b.BytesFetched)
	line := fmt.Sprintf("[%-12s] %.12s start: %s from: %10s ",
		b.Type,
		b.Descriptor.Digest.Encoded(),
		b.FetchStartTime.UTC().Format(time.TimeOnly),
		compose.FormatBytesInt64(b.BlobInfo.BytesFetched))
	line += fmt.Sprintf("progress: %10s / %10s (%3.0f%%) avg: %10s/s cur: %10s/s",
		compose.FormatBytesInt64(fetched),
		compose.FormatBytesInt64(b.Descriptor.Size),
		100*float64(fetched)/float64(b.Descriptor.Size),
		compose.FormatBytesInt64(atomic.LoadInt64(&b.ReadSpeedAvg)),
		compose.FormatBytesInt64(atomic.LoadInt64(&b.ReadSpeedCur)))
	if fetched >= b.Descriptor.Size {
		line += "; done at " + time.Now().UTC().Format(time.TimeOnly)
	}
	return line
}
