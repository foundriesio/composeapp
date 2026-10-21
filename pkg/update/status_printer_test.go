package update

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/foundriesio/composeapp/pkg/compose"
)

const cl = "\r\033[K"

// render feeds the events to the printer, on a terminal of the width when tty (0 = unknown),
// with the clock advancing 100 ms on every read, and returns what it printed.
func render(t *testing.T, tty bool, width int, events []compose.InstallProgress) string {
	t.Helper()
	wasTty, wasWidth, wasNow := isTty, ttyWidth, now
	isTty, ttyWidth = tty, width
	var tick time.Duration
	now = func() time.Time {
		tick += 100 * time.Millisecond
		return time.Unix(0, 0).Add(tick)
	}
	defer func() { isTty, ttyWidth, now = wasTty, wasWidth, wasNow }()
	stdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = stdout }()
	ctx := &imageLoadingContext{}
	for i := range events {
		renderImageLoadingProgress(ctx, &events[i], 0)
	}
	w.Close()
	var out bytes.Buffer
	if _, err := io.Copy(&out, r); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func layer(state compose.ImageLoadState, image, id string, current, total, size int64) compose.InstallProgress {
	return compose.InstallProgress{AppInstallState: compose.AppInstallStateImagesLoading, ImageLoadState: state,
		ImageID: image, ID: id, Current: current, Total: total, Size: size}
}

// table is the redraw of a table on a TTY: the cursor up to the first row drawn before, then
// every row cleared and printed.
func table(drawn int, rows ...string) string {
	var b strings.Builder
	if drawn > 0 {
		b.WriteString(fmt.Sprintf("\033[%dA", drawn))
	}
	for _, row := range rows {
		b.WriteString(cl + row + "\n")
	}
	return b.String()
}

const (
	kib = 1024
	mib = 1024 * kib
)

// The containerd route: an image whose layers are all announced up front, the first existing;
// one extracted with a bar against its tar size, one with an unknown tar size, one loaded
// within a poll without ever being reported extracting; the layers announced once more, as the
// second unpack attempt would after a snapshot went away, which changes nothing; then an image
// of one layer with no size, loaded at once.
var ctrdEvents = []compose.InstallProgress{
	layer(compose.ImageLoadStateImageLoading, "img1", "", 0, 0, 0),
	layer(compose.ImageLoadStateLayerExist, "img1", "aaaa", 0, 0, 1887437),
	layer(compose.ImageLoadStateLayerPending, "img1", "bbbb", 0, 0, 16*mib),
	layer(compose.ImageLoadStateLayerPending, "img1", "cccc", 0, 0, 500),
	layer(compose.ImageLoadStateLayerPending, "img1", "dddd", 0, 0, 2*kib),
	layer(compose.ImageLoadStateLayerSyncing, "img1", "bbbb", 0, 100, 16*mib),
	layer(compose.ImageLoadStateLayerSyncing, "img1", "bbbb", 40, 100, 16*mib),
	layer(compose.ImageLoadStateLayerLoaded, "img1", "bbbb", 1500, 0, 16*mib),
	layer(compose.ImageLoadStateLayerPending, "img1", "bbbb", 0, 0, 16*mib),
	layer(compose.ImageLoadStateLayerSyncing, "img1", "cccc", 0, 0, 500),
	layer(compose.ImageLoadStateLayerSyncing, "img1", "cccc", 700, 0, 500),
	layer(compose.ImageLoadStateLayerLoaded, "img1", "cccc", 700, 0, 500),
	layer(compose.ImageLoadStateLayerExist, "img1", "aaaa", 0, 0, 1887437),
	layer(compose.ImageLoadStateLayerLoaded, "img1", "dddd", 0, 0, 2*kib),
	layer(compose.ImageLoadStateImageLoaded, "img1", "", 0, 0, 0),
	layer(compose.ImageLoadStateImageLoading, "img2", "", 0, 0, 0),
	layer(compose.ImageLoadStateLayerLoaded, "img2", "eeee", 0, 0, 0),
	layer(compose.ImageLoadStateImageLoaded, "img2", "", 0, 0, 0),
}

const (
	aExists  = "\taaaa  [=========================] 100%    1.8 MiB  exists"
	bPending = "\tbbbb  [                         ]   0%   16.0 MiB"
	bAt0     = "\tbbbb  [                         ]   0%   16.0 MiB  0.0s"
	bAt40    = "\tbbbb  [==========               ]  40%   16.0 MiB  0.1s"
	bLoaded  = "\tbbbb  [=========================] 100%   16.0 MiB  1.5s"
	cPending = "\tcccc  [                         ]   0%      500 B"
	cAt0     = "\tcccc  [                         ]   0%      500 B  0.0s"
	cAt1     = "\tcccc  [                         ]   0%      500 B  0.1s"
	cLoaded  = "\tcccc  [=========================] 100%      500 B  0.7s"
	dPending = "\tdddd  [                         ]   0%    2.0 KiB"
	dLoaded  = "\tdddd  [=========================] 100%    2.0 KiB"
	eLoaded  = "\teeee  [=========================] 100%"
)

func TestRenderImageLoadingWithoutTTY(t *testing.T) {
	want := strings.Join([]string{
		"Loading image img1",
		aExists,
		bLoaded,
		cLoaded,
		dLoaded,
		"Image loaded: img1  (4 layers, 1 existed, 0.8s)",
		"Loading image img2",
		eLoaded,
		"Image loaded: img2  (1 layer, 0.1s)",
		"",
	}, "\n")
	if got := render(t, false, 0, ctrdEvents); got != want {
		t.Fatalf("unexpected output:\n%q\nwant:\n%q", got, want)
	}
}

func TestRenderImageLoadingWithTTY(t *testing.T) {
	want := "Loading image img1\n" +
		table(0, aExists) +
		table(1, aExists, bPending) +
		table(2, aExists, bPending, cPending) +
		table(3, aExists, bPending, cPending, dPending) +
		table(4, aExists, bAt0, cPending, dPending) +
		table(4, aExists, bAt40, cPending, dPending) +
		table(4, aExists, bLoaded, cPending, dPending) +
		table(4, aExists, bLoaded, cPending, dPending) +
		table(4, aExists, bLoaded, cAt0, dPending) +
		table(4, aExists, bLoaded, cAt1, dPending) +
		table(4, aExists, bLoaded, cLoaded, dPending) +
		table(4, aExists, bLoaded, cLoaded, dPending) +
		table(4, aExists, bLoaded, cLoaded, dLoaded) +
		"Image loaded: img1  (4 layers, 1 existed, 0.8s)\n" +
		"Loading image img2\n" +
		table(0, eLoaded) +
		"Image loaded: img2  (1 layer, 0.1s)\n"
	if got := render(t, true, 0, ctrdEvents); got != want {
		t.Fatalf("unexpected output:\n%q\nwant:\n%q", got, want)
	}
}

// The docker load route reports loading, syncing and loaded per layer, in sequence, without
// the layer's size: its syncing events carry the bytes dockerd has extracted, all of them, and
// its loaded events carry no time.
var dockerEvents = []compose.InstallProgress{
	layer(compose.ImageLoadStateLayerLoading, "img", "aaaa", 10, 100, 0),
	layer(compose.ImageLoadStateLayerLoading, "img", "aaaa", 100, 100, 0),
	layer(compose.ImageLoadStateLayerSyncing, "img", "aaaa", 100, 100, 0),
	layer(compose.ImageLoadStateLayerLoaded, "img", "aaaa", 0, 0, 0),
	layer(compose.ImageLoadStateLayerLoading, "img", "bbbb", 50, 50, 0),
	layer(compose.ImageLoadStateLayerSyncing, "img", "bbbb", 50, 50, 0),
	layer(compose.ImageLoadStateLayerLoaded, "img", "bbbb", 0, 0, 0),
	layer(compose.ImageLoadStateImageLoaded, "img", "", 0, 0, 0),
}

const (
	dockerA10     = "\taaaa  [==                       ]  10%             0.0s"
	dockerA100    = "\taaaa  [=========================] 100%             0.1s"
	dockerASync   = "\taaaa  [=========================] 100%             0.2s"
	dockerALoaded = "\taaaa  [=========================] 100%"
	dockerB100    = "\tbbbb  [=========================] 100%             0.0s"
	dockerBSync   = "\tbbbb  [=========================] 100%             0.1s"
	dockerBLoaded = "\tbbbb  [=========================] 100%"
)

func TestRenderDockerLoadRouteWithTTY(t *testing.T) {
	want := "Loading image img\n" +
		table(0, dockerA10) +
		table(1, dockerA100) +
		table(1, dockerASync) +
		table(1, dockerALoaded) +
		table(1, dockerALoaded, dockerB100) +
		table(2, dockerALoaded, dockerBSync) +
		table(2, dockerALoaded, dockerBLoaded) +
		"Image loaded: img  (2 layers, 0.7s)\n"
	if got := render(t, true, 0, dockerEvents); got != want {
		t.Fatalf("unexpected output:\n%q\nwant:\n%q", got, want)
	}
}

func TestRenderDockerLoadRoute(t *testing.T) {
	want := strings.Join([]string{
		"Loading image img",
		dockerALoaded,
		dockerBLoaded,
		"Image loaded: img  (2 layers, 0.7s)",
		"",
	}, "\n")
	if got := render(t, false, 0, dockerEvents); got != want {
		t.Fatalf("unexpected output:\n%q\nwant:\n%q", got, want)
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 500: "500 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB",
		16 * mib: "16.0 MiB", 1887437: "1.8 MiB", 3 * 1024 * mib: "3.0 GiB", 2048 * 1024 * mib: "2048.0 GiB"} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

// The install into containerd reports every image loaded after the last one is unpacked: the
// summary of each image is its own, measured to its last layer loaded.
func TestRenderImagesLoadedAfterAll(t *testing.T) {
	events := []compose.InstallProgress{
		layer(compose.ImageLoadStateImageLoading, "img1", "", 0, 0, 0),
		layer(compose.ImageLoadStateLayerPending, "img1", "aaaa", 0, 0, 2*kib),
		layer(compose.ImageLoadStateLayerLoaded, "img1", "aaaa", 300, 0, 2*kib),
		layer(compose.ImageLoadStateImageLoading, "img2", "", 0, 0, 0),
		layer(compose.ImageLoadStateLayerExist, "img2", "bbbb", 0, 0, 16*mib),
		layer(compose.ImageLoadStateImageLoaded, "img1", "", 0, 0, 0),
		layer(compose.ImageLoadStateImageLoaded, "img2", "", 0, 0, 0),
	}
	want := strings.Join([]string{
		"Loading image img1",
		"\taaaa  [=========================] 100%    2.0 KiB  0.3s",
		"Loading image img2",
		"\tbbbb  [=========================] 100%   16.0 MiB  exists",
		"Image loaded: img1  (1 layer, 0.1s)",
		"Image loaded: img2  (1 layer, 1 existed, 0.1s)",
		"",
	}, "\n")
	if got := render(t, false, 0, events); got != want {
		t.Fatalf("unexpected output:\n%q\nwant:\n%q", got, want)
	}
}

// A terminal too narrow for a row gets the final rows only, as a pipe does; the tab of a row
// takes eight columns.
func TestRenderNarrowTTY(t *testing.T) {
	want := strings.Join([]string{
		"Loading image img",
		dockerALoaded,
		dockerBLoaded,
		"Image loaded: img  (2 layers, 0.7s)",
		"",
	}, "\n")
	width := len(dockerASync) + 7
	if got := render(t, true, width-1, dockerEvents); got != want {
		t.Fatalf("unexpected output:\n%q\nwant:\n%q", got, want)
	}
	if got := render(t, true, width, dockerEvents); !strings.Contains(got, cl) {
		t.Fatalf("a terminal of %d columns did not get the table redrawn in place:\n%q", width, got)
	}
}

// A row that grows wider than the terminal stops the redraws: the rows drawn so far stay and the
// final rows follow them.
func TestRenderRowOutgrowsTTY(t *testing.T) {
	const (
		aAt0     = "\taaaaaaaaaaaa  [                         ]   0%  2048.0 GiB  0.0s"
		aLoaded  = "\taaaaaaaaaaaa  [=========================] 100%  2048.0 GiB  1000.0s"
		bPending = "\tbbbbbbbbbbbb  [                         ]   0%      500 B"
		bLoaded  = "\tbbbbbbbbbbbb  [=========================] 100%      500 B"
	)
	events := []compose.InstallProgress{
		layer(compose.ImageLoadStateImageLoading, "img", "", 0, 0, 0),
		layer(compose.ImageLoadStateLayerPending, "img", "aaaaaaaaaaaa", 0, 0, 2048*1024*mib),
		layer(compose.ImageLoadStateLayerPending, "img", "bbbbbbbbbbbb", 0, 0, 500),
		layer(compose.ImageLoadStateLayerSyncing, "img", "aaaaaaaaaaaa", 0, 100, 2048*1024*mib),
		layer(compose.ImageLoadStateLayerLoaded, "img", "aaaaaaaaaaaa", 1000000, 0, 2048*1024*mib),
		layer(compose.ImageLoadStateLayerLoaded, "img", "bbbbbbbbbbbb", 0, 0, 500),
		layer(compose.ImageLoadStateImageLoaded, "img", "", 0, 0, 0),
	}
	want := "Loading image img\n" +
		table(0, "\taaaaaaaaaaaa  [                         ]   0%  2048.0 GiB") +
		table(1, "\taaaaaaaaaaaa  [                         ]   0%  2048.0 GiB", bPending) +
		table(2, aAt0, bPending) +
		aLoaded + "\n" + bLoaded + "\n" +
		"Image loaded: img  (2 layers, 0.3s)\n"
	if got := render(t, true, len(aAt0)+7, events); got != want {
		t.Fatalf("unexpected output:\n%q\nwant:\n%q", got, want)
	}
}

// A layer that existed and is loaded once more, as after a base snapshot went away, is no
// longer counted as existing, whether it was seen loading or not; a progress sample out of
// range keeps the bar in range.
func TestRenderExistingLayerLoadedAgain(t *testing.T) {
	events := []compose.InstallProgress{
		layer(compose.ImageLoadStateImageLoading, "img", "", 0, 0, 0),
		layer(compose.ImageLoadStateLayerExist, "img", "aaaa", 0, 0, 2*kib),
		layer(compose.ImageLoadStateLayerExist, "img", "bbbb", 0, 0, 0),
		layer(compose.ImageLoadStateLayerSyncing, "img", "aaaa", -5, 100, 2*kib),
		layer(compose.ImageLoadStateLayerSyncing, "img", "aaaa", 150, 100, 2*kib),
		layer(compose.ImageLoadStateLayerLoaded, "img", "aaaa", 200, 0, 2*kib),
		layer(compose.ImageLoadStateLayerLoaded, "img", "bbbb", 0, 0, 0),
		layer(compose.ImageLoadStateImageLoaded, "img", "", 0, 0, 0),
	}
	const bExists = "\tbbbb  [=========================] 100%             exists"
	want := "Loading image img\n" +
		table(0, "\taaaa  [=========================] 100%    2.0 KiB  exists") +
		table(1, "\taaaa  [=========================] 100%    2.0 KiB  exists", bExists) +
		table(2, "\taaaa  [                         ]   0%    2.0 KiB  0.0s", bExists) +
		table(2, "\taaaa  [=========================] 100%    2.0 KiB  0.1s", bExists) +
		table(2, "\taaaa  [=========================] 100%    2.0 KiB  0.2s", bExists) +
		table(2, "\taaaa  [=========================] 100%    2.0 KiB  0.2s", "\tbbbb  [=========================] 100%") +
		"Image loaded: img  (2 layers, 0.6s)\n"
	if got := render(t, true, 0, events); got != want {
		t.Fatalf("unexpected output:\n%q\nwant:\n%q", got, want)
	}
}
