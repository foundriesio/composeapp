package compose

import (
	"context"
	"testing"
	"time"
)

// TestInstallProgressDeliversStateChanges: a burst of state changes larger than the reporter's
// buffer reaches a slow handler completely and in order, and the samples between them may not.
func TestInstallProgressDeliversStateChanges(t *testing.T) {
	var got []LoadImageProgress
	opts := InstallOptions{}
	WithInstallProgress(func(p *InstallProgress) {
		time.Sleep(2 * time.Millisecond)
		got = append(got, LoadImageProgress{State: p.ImageLoadState, ID: p.ID, Current: p.Current, Total: p.Total})
	})(&opts)
	report := imageProgressCallback(context.Background(), &opts, testApp{})

	report(&LoadImageProgress{State: ImageLoadStateImageLoading})
	for i := 0; i < 6; i++ {
		report(&LoadImageProgress{State: ImageLoadStateLayerLoaded, ID: string(rune('a' + i))})
	}
	report(&LoadImageProgress{State: ImageLoadStateLayerLoading, ID: "s", Current: 0, Total: 100})
	for i := 1; i < 100; i++ {
		report(&LoadImageProgress{State: ImageLoadStateLayerLoading, ID: "s", Current: int64(i), Total: 100})
	}
	report(&LoadImageProgress{State: ImageLoadStateLayerLoading, ID: "s", Current: 100, Total: 100})
	report(&LoadImageProgress{State: ImageLoadStateImageLoaded})
	opts.ProgressReporter.Stop(true)

	var changes []LoadImageProgress
	for _, p := range got {
		if p.State == ImageLoadStateLayerLoading && p.Current > 0 && p.Current < 100 {
			continue // a sample
		}
		changes = append(changes, p)
	}
	want := []LoadImageProgress{{State: ImageLoadStateImageLoading}}
	for i := 0; i < 6; i++ {
		want = append(want, LoadImageProgress{State: ImageLoadStateLayerLoaded, ID: string(rune('a' + i))})
	}
	want = append(want,
		LoadImageProgress{State: ImageLoadStateLayerLoading, ID: "s", Current: 0, Total: 100},
		LoadImageProgress{State: ImageLoadStateLayerLoading, ID: "s", Current: 100, Total: 100},
		LoadImageProgress{State: ImageLoadStateImageLoaded})
	if len(changes) != len(want) {
		t.Fatalf("state changes lost or duplicated:\n got %+v\nwant %+v", changes, want)
	}
	for i := range want {
		if changes[i] != want[i] {
			t.Fatalf("state change %d: got %+v, want %+v", i, changes[i], want[i])
		}
	}
}

type testApp struct{ App }

func (testApp) Ref() *AppRef { return &AppRef{} }
