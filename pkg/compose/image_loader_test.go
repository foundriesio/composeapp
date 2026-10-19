package compose

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/docker/docker/pkg/jsonmessage"
)

func TestLoadImages(t *testing.T) {
	t.Skip("TODO: we need to have the docker client and daemon mocks to test this function")
}

// collectingReporter keeps every update, for tests of what the parsers emit.
type collectingReporter struct{ got []LoadImageProgress }

func (r *collectingReporter) Start(func(*LoadImageProgress))              {}
func (r *collectingReporter) Update(p LoadImageProgress) bool             { r.got = append(r.got, p); return true }
func (r *collectingReporter) Send(_ context.Context, p LoadImageProgress) { r.got = append(r.got, p) }
func (r *collectingReporter) Stop(bool)                                   {}

func dockerLoadStream(t *testing.T, msgs []jsonmessage.JSONMessage) io.ReadCloser {
	t.Helper()
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			t.Fatal(err)
		}
	}
	return io.NopCloser(strings.NewReader(b.String()))
}

// TestReportProgressIfDockerLoadedCarriesNoBytes: the bytes dockerd reports for a layer end with
// its syncing event; the loaded event, like the image loaded one, carries none, so that a reader
// of the loaded event does not take the last byte count for a time.
func TestReportProgressIfDockerLoadedCarriesNoBytes(t *testing.T) {
	progress := func(id string, cur, total int64) jsonmessage.JSONMessage {
		return jsonmessage.JSONMessage{ID: id, Progress: &jsonmessage.JSONProgress{Current: cur, Total: total}}
	}
	body := dockerLoadStream(t, []jsonmessage.JSONMessage{
		progress("l1", 10, 100), progress("l1", 100, 100),
		progress("l2", 5, 50), progress("l2", 50, 50),
		{Stream: "Loaded image: img\n"},
	})
	reporter := &collectingReporter{}
	layers := map[string]string{"l1": "aaaaaaaaaaaa", "l2": "bbbbbbbbbbbb"}
	err := reportProgressIfDocker(body, []imageURI2RefCounter{{URI: "img", refCounter: 1}}, layers, &LoadImageOptions{ProgressReporter: reporter})
	if err != nil {
		t.Fatal(err)
	}
	type ev struct {
		state      ImageLoadState
		id         string
		cur, total int64
	}
	var got []ev
	for _, p := range reporter.got {
		got = append(got, ev{p.State, p.ID, p.Current, p.Total})
	}
	want := []ev{
		{ImageLoadStateLayerLoading, "aaaaaaa", 10, 100},
		{ImageLoadStateLayerLoading, "aaaaaaa", 100, 100},
		{ImageLoadStateLayerSyncing, "aaaaaaa", 100, 100},
		{ImageLoadStateLayerLoaded, "aaaaaaa", 0, 0},
		{ImageLoadStateLayerLoading, "bbbbbbb", 5, 50},
		{ImageLoadStateLayerLoading, "bbbbbbb", 50, 50},
		{ImageLoadStateLayerSyncing, "bbbbbbb", 50, 50},
		{ImageLoadStateImageLoaded, "bbbbbbb", 0, 0},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events %+v, want %+v", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}
