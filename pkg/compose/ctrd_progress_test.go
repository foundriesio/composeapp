package compose

import (
	"bytes"
	"compress/gzip"
	"errors"
	"path/filepath"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// gzipBlob writes the gzip of tar into the store and returns its descriptor as a gzip layer.
func gzipBlob(t *testing.T, blobsRoot string, tar []byte) ocispec.Descriptor {
	t.Helper()
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	if _, err := w.Write(tar); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	desc := writeStoreBlob(t, blobsRoot, gz.Bytes())
	desc.MediaType = "application/vnd.oci.image.layer.v1.tar+gzip"
	return desc
}

// TestUncompressedSize: the size the extraction bar counts against is read from the gzip trailer,
// is the blob's size for an uncompressed layer, and is unknown for other compressions.
func TestUncompressedSize(t *testing.T) {
	blobsRoot := t.TempDir()
	tar := bytes.Repeat([]byte("tar bytes "), 10000)
	gzDesc := gzipBlob(t, blobsRoot, tar)
	for _, mt := range []string{"application/vnd.oci.image.layer.v1.tar+gzip", "application/vnd.docker.image.rootfs.diff.tar.gzip"} {
		gzDesc.MediaType = mt
		if got := uncompressedSize(blobsRoot, gzDesc); got != int64(len(tar)) {
			t.Errorf("%s: got %d, want %d", mt, got, len(tar))
		}
	}

	plain := writeStoreBlob(t, blobsRoot, tar)
	plain.MediaType = "application/vnd.oci.image.layer.v1.tar"
	if got := uncompressedSize(blobsRoot, plain); got != int64(len(tar)) {
		t.Errorf("plain tar: got %d, want %d", got, len(tar))
	}

	// Only the media type matters: a zstd frame carries no size the bar could use.
	zstdDesc := writeStoreBlob(t, blobsRoot, bytes.Repeat([]byte{0x28, 0xb5, 0x2f, 0xfd}, 8))
	zstdDesc.MediaType = "application/vnd.oci.image.layer.v1.tar+zstd"
	if got := uncompressedSize(blobsRoot, zstdDesc); got != 0 {
		t.Errorf("zstd: got %d, want 0 (unknown)", got)
	}

	missing := ocispec.Descriptor{MediaType: gzDesc.MediaType, Digest: gzDesc.Digest, Size: gzDesc.Size}
	if got := uncompressedSize(filepath.Join(blobsRoot, "nowhere"), missing); got != 0 {
		t.Errorf("missing blob: got %d, want 0 (unknown)", got)
	}
	tiny := writeStoreBlob(t, blobsRoot, []byte("x"))
	tiny.MediaType = gzDesc.MediaType
	if got := uncompressedSize(blobsRoot, tiny); got != 0 {
		t.Errorf("blob shorter than a gzip stream: got %d, want 0 (unknown)", got)
	}
}

// TestUnpackWatchSample: the bytes reported for a layer never go backwards when space is freed
// elsewhere and never exceed its tar size; a failed measure turns the layer over to the elapsed
// time for good; the next layer starts its own measure.
func TestUnpackWatchSample(t *testing.T) {
	blobsRoot := t.TempDir()
	tar := bytes.Repeat([]byte("x"), 100000)
	layers := []ocispec.Descriptor{gzipBlob(t, blobsRoot, tar), gzipBlob(t, blobsRoot, tar[:50000])}
	free, failNext := int64(1000000), false
	orig := freeBytes
	t.Cleanup(func() { freeBytes = orig })
	freeBytes = func(string) (int64, error) {
		if failNext {
			failNext = false
			return 0, errors.New("statfs failed")
		}
		return free, nil
	}
	w := &unpackWatch{c: &ctrdClient{snapshotterRoot: "snapshots"}, img: &ctrdImage{layers: layers}, blobsRoot: blobsRoot}

	w.startLayer(0)
	free = 940000
	if cur, total := w.sample(); cur != 60000 || total != 100000 {
		t.Fatalf("got %d/%d, want 60000/100000", cur, total)
	}
	free = 980000 // space freed elsewhere
	if cur, total := w.sample(); cur != 60000 || total != 100000 {
		t.Fatalf("after space was freed: got %d/%d, want 60000/100000", cur, total)
	}
	free = 850000 // more written than the tar holds (block rounding)
	if cur, total := w.sample(); cur != 100000 || total != 100000 {
		t.Fatalf("beyond the tar size: got %d/%d, want 100000/100000", cur, total)
	}
	failNext = true
	if cur, total := w.sample(); total != 0 || cur < 0 {
		t.Fatalf("after a failed measure: got %d/%d, want milliseconds/0", cur, total)
	}
	free = 800000
	if cur, total := w.sample(); total != 0 {
		t.Fatalf("the bar must stay off for the rest of the layer: got %d/%d", cur, total)
	}

	w.startLayer(1)
	free = 790000
	if cur, total := w.sample(); cur != 10000 || total != 50000 {
		t.Fatalf("next layer: got %d/%d, want 10000/50000", cur, total)
	}

	// A baseline that cannot be measured leaves the bar off.
	failNext = true
	w.startLayer(0)
	if cur, total := w.sample(); total != 0 {
		t.Fatalf("without a baseline: got %d/%d, want milliseconds/0", cur, total)
	}
}
