package composectl

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

var (
	ansiRe     = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")
	cursorUpRe = regexp.MustCompile("\x1b\\[([0-9]+)A")
)

// newProgressBlob builds a blob whose digest is unique per id, sized so progress
// can be advanced across several frames.
func newProgressBlob(t *testing.T, id string, size int64, start time.Time) *compose.BlobFetchProgress {
	t.Helper()
	dgst := digest.FromString(id)
	return &compose.BlobFetchProgress{
		BlobInfo: compose.BlobInfo{
			Descriptor: &ocispec.Descriptor{Digest: dgst, Size: size},
			Type:       compose.BlobTypeImageLayer,
			State:      compose.BlobFetching,
		},
		FetchStartTime: start,
	}
}

func shortDigest(b *compose.BlobFetchProgress) string {
	enc := b.Descriptor.Digest.Encoded()
	return enc[:12]
}

func snapshot(blobs ...*compose.BlobFetchProgress) *compose.FetchProgress {
	m := compose.BlobsFetchProgress{}
	for _, b := range blobs {
		m[b.Descriptor.Digest] = b
	}
	return &compose.FetchProgress{Blobs: m}
}

// advance sets a blob's fetched bytes and flips its state to done once complete.
func advance(b *compose.BlobFetchProgress, fetched int64) {
	b.BytesFetched = fetched
	if fetched >= b.Descriptor.Size {
		b.State = compose.BlobOk
	}
}

func doneLineCount(output string, b *compose.BlobFetchProgress) int {
	count := 0
	for _, line := range strings.Split(ansiRe.ReplaceAllString(output, ""), "\n") {
		if strings.Contains(line, shortDigest(b)) && strings.Contains(line, "; done at") {
			count++
		}
	}
	return count
}

func anyLineFor(output string, b *compose.BlobFetchProgress) int {
	count := 0
	for _, line := range strings.Split(ansiRe.ReplaceAllString(output, ""), "\n") {
		if strings.Contains(line, shortDigest(b)) {
			count++
		}
	}
	return count
}

// runFrames feeds a fixed multi-frame scenario into the renderer: blob A finishes
// in frame 1, blob B spans all three frames, blob C appears in frame 2 and
// finishes in frame 3.
func runFrames(r *fetchProgressRenderer) (a, b, c *compose.BlobFetchProgress) {
	t0 := time.Unix(0, 0).UTC()
	a = &compose.BlobFetchProgress{
		BlobInfo: compose.BlobInfo{
			Descriptor: &ocispec.Descriptor{Digest: digest.FromString("blob-a"), Size: 10},
			Type:       compose.BlobTypeImageManifest,
			State:      compose.BlobOk,
		},
		FetchStartTime: t0,
		BytesFetched:   10,
	}
	b = &compose.BlobFetchProgress{
		BlobInfo: compose.BlobInfo{
			Descriptor: &ocispec.Descriptor{Digest: digest.FromString("blob-b"), Size: 100},
			Type:       compose.BlobTypeImageLayer,
			State:      compose.BlobFetching,
		},
		FetchStartTime: t0.Add(time.Second),
		BytesFetched:   30,
	}
	c = &compose.BlobFetchProgress{
		BlobInfo: compose.BlobInfo{
			Descriptor: &ocispec.Descriptor{Digest: digest.FromString("blob-c"), Size: 50},
			Type:       compose.BlobTypeImageLayer,
			State:      compose.BlobFetching,
		},
		FetchStartTime: t0.Add(2 * time.Second),
	}

	r.Render(snapshot(a, b))    // frame 1: A done, B at 30%
	advance(b, 70)              // frame 2: B at 70%, C appears at 0%
	r.Render(snapshot(a, b, c)) //
	advance(b, 100)             // frame 3: B and C both complete
	advance(c, 50)              //
	r.Render(snapshot(a, b, c)) //
	return a, b, c
}

func TestFetchProgressRendererPlainReportsProgressAndCompletion(t *testing.T) {
	var out bytes.Buffer
	r := NewFetchProgressRenderer(&out, false)
	a, b, c := runFrames(r)

	for _, blob := range []*compose.BlobFetchProgress{a, b, c} {
		if got := doneLineCount(out.String(), blob); got != 1 {
			t.Errorf("blob %s: expected exactly one completion line in non-TTY mode, got %d\n%s",
				shortDigest(blob), got, out.String())
		}
	}
	// A completes within its first frame: just the completion line.
	// B is seen at 30% and 70% before completing: two progress lines + done.
	// C is seen at 0% before completing: one progress line + done.
	for blob, want := range map[*compose.BlobFetchProgress]int{a: 1, b: 3, c: 2} {
		if got := anyLineFor(out.String(), blob); got != want {
			t.Errorf("blob %s: expected %d printed lines in non-TTY mode, got %d\n%s",
				shortDigest(blob), want, got, out.String())
		}
	}
}

func TestFetchProgressRendererTtyGraduatesEachBlobOnce(t *testing.T) {
	var out bytes.Buffer
	r := NewFetchProgressRenderer(&out, true)
	a, b, c := runFrames(r)

	for _, blob := range []*compose.BlobFetchProgress{a, b, c} {
		if got := doneLineCount(out.String(), blob); got != 1 {
			t.Errorf("blob %s: expected exactly one completion line in TTY mode, got %d\n%s",
				shortDigest(blob), got, out.String())
		}
	}

	// The live region (and thus every cursor-up move) must never exceed the peak
	// number of concurrently in-flight blobs (here B and C in frame 2 => 2).
	const maxConcurrent = 2
	for _, m := range cursorUpRe.FindAllStringSubmatch(out.String(), -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("failed to parse cursor-up count %q: %s", m[1], err)
		}
		if n > maxConcurrent {
			t.Errorf("cursor moved up %d lines, exceeding the in-flight bound %d; "+
				"the redraw region is growing unbounded again", n, maxConcurrent)
		}
	}
}

// Ensures the non-TTY decile reporting prints a line only when a blob enters a
// new 10% bucket, and never marks an in-flight blob as completed.
func TestFetchProgressRendererPlainReportsOncePerDecile(t *testing.T) {
	var out bytes.Buffer
	r := NewFetchProgressRenderer(&out, false)
	b := newProgressBlob(t, "incomplete", 100, time.Unix(0, 0).UTC())

	advance(b, 40)
	r.Render(snapshot(b)) // first observation at 40% reports
	advance(b, 49)
	r.Render(snapshot(b)) // still in the 40% bucket: no new line
	advance(b, 50)
	r.Render(snapshot(b)) // crossed into the 50% bucket: reports

	if got := anyLineFor(out.String(), b); got != 2 {
		t.Errorf("expected one line per crossed decile (2), got %d:\n%s", got, out.String())
	}
	if got := doneLineCount(out.String(), b); got != 0 {
		t.Errorf("expected no completion line for an in-flight blob, got %d:\n%s", got, out.String())
	}
}
