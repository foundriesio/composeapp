package compose

import (
	"reflect"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestMergeByDevice(t *testing.T) {
	needs := []StorageNeed{
		{Path: "/var/sota/reset-apps", Required: 100},
		{Path: "/var/sota/compose-apps", Required: 10},
		{Path: "/var/lib/docker/containerd/daemon/io.containerd.content.v1.content", Required: 1000},
		{Path: "/var/lib/docker/containerd/daemon/io.containerd.snapshotter.v1.overlayfs", Required: 5000},
	}

	// Everything on one filesystem, as on the devices: one entry under the first path.
	got := mergeByDevice(needs, []uint64{7, 7, 7, 7})
	want := []StorageNeed{{Path: "/var/sota/reset-apps", Required: 6110}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("one filesystem: got %+v, want %+v", got, want)
	}

	// The store on its own filesystem, containerd on another.
	got = mergeByDevice(needs, []uint64{1, 1, 2, 2})
	want = []StorageNeed{
		{Path: "/var/sota/reset-apps", Required: 110},
		{Path: "/var/lib/docker/containerd/daemon/io.containerd.content.v1.content", Required: 6000},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("two filesystems: got %+v, want %+v", got, want)
	}

	// Four filesystems: the order of the input is kept.
	got = mergeByDevice(needs, []uint64{4, 3, 2, 1})
	if !reflect.DeepEqual(got, needs) {
		t.Fatalf("four filesystems: got %+v, want %+v", got, needs)
	}

	if got := mergeByDevice(nil, nil); len(got) != 0 {
		t.Fatalf("no needs: got %+v", got)
	}
}

func TestMergeStorageNeedsStatsPaths(t *testing.T) {
	dir := t.TempDir()
	got, err := MergeStorageNeeds([]StorageNeed{{Path: dir, Required: 1}, {Path: dir, Required: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != dir || got[0].Required != 3 {
		t.Fatalf("got %+v", got)
	}
	if _, err := MergeStorageNeeds([]StorageNeed{{Path: dir + "/missing", Required: 1}}); err == nil {
		t.Fatal("expected an error for a path that does not exist")
	}
}

func TestInstallNeedsAddImage(t *testing.T) {
	layer := func(name string, size int64) ocispec.Descriptor {
		return ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromString(name), Size: size}
	}
	chain := func(names ...string) []digest.Digest {
		var ids []digest.Digest
		for _, n := range names {
			ids = append(ids, digest.FromString("chain:"+n))
		}
		return ids
	}
	base, a, b, other := layer("base", 100), layer("a", 10), layer("b", 20), layer("other", 1000)
	unpacked := func(l *ocispec.Descriptor) int64 { return l.Size * 3 }

	needs := newInstallNeeds()
	// Two images on the same base: the base blob is streamed once and its snapshot counted once.
	needs.addImage(&ctrdImage{layers: []ocispec.Descriptor{base, a}, chainIDs: chain("base", "base/a")}, 0, unpacked)
	needs.addImage(&ctrdImage{layers: []ocispec.Descriptor{base, b}, chainIDs: chain("base", "base/b")}, 0, unpacked)
	if needs.peak != 130 || needs.unpacked != 390 {
		t.Fatalf("shared base: peak %d (want 130), unpacked %d (want 390)", needs.peak, needs.unpacked)
	}
	// The same base blob above another parent: no new blob, but a new snapshot.
	needs.addImage(&ctrdImage{layers: []ocispec.Descriptor{other, base}, chainIDs: chain("other", "other/base")}, 0, unpacked)
	if needs.peak != 1130 || needs.unpacked != 3690 {
		t.Fatalf("other parent: peak %d (want 1130), unpacked %d (want 3690)", needs.peak, needs.unpacked)
	}
	// Layers before the first missing snapshot are neither streamed nor counted.
	needs = newInstallNeeds()
	needs.addImage(&ctrdImage{layers: []ocispec.Descriptor{base, a}, chainIDs: chain("base", "base/a")}, 1, unpacked)
	if needs.peak != 10 || needs.unpacked != 30 {
		t.Fatalf("first=1: peak %d (want 10), unpacked %d (want 30)", needs.peak, needs.unpacked)
	}
	needs.addImage(&ctrdImage{layers: []ocispec.Descriptor{base, a}, chainIDs: chain("base", "base/a")}, 2, unpacked)
	if needs.peak != 10 || needs.unpacked != 30 {
		t.Fatalf("nothing missing: peak %d (want 10), unpacked %d (want 30)", needs.peak, needs.unpacked)
	}
}

// An image whose config could not be read has no chain IDs: every layer is streamed (once by
// digest) and every layer occurrence is a snapshot, because the same layer above another parent
// is another snapshot and the parents are unknown.
func TestInstallNeedsAddImageWithoutChainIDs(t *testing.T) {
	layer := func(name string, size int64) ocispec.Descriptor {
		return ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromString(name), Size: size}
	}
	base, a, b, x, other := layer("base", 100), layer("a", 10), layer("b", 20), layer("x", 50), layer("other", 1000)
	unpacked := func(l *ocispec.Descriptor) int64 { return l.Size * 3 }

	needs := newInstallNeeds()
	needs.addImage(&ctrdImage{layers: []ocispec.Descriptor{base, a}}, 0, unpacked)
	if needs.peak != 110 || needs.unpacked != 330 {
		t.Fatalf("unknown chain: peak %d (want 110), unpacked %d (want 330)", needs.peak, needs.unpacked)
	}
	// [a, x] and [b, x]: x is one blob but two snapshots, one above each parent.
	needs = newInstallNeeds()
	needs.addImage(&ctrdImage{layers: []ocispec.Descriptor{a, x}}, 0, unpacked)
	needs.addImage(&ctrdImage{layers: []ocispec.Descriptor{b, x}}, 0, unpacked)
	if needs.peak != 80 || needs.unpacked != 390 {
		t.Fatalf("x above two parents: peak %d (want 80), unpacked %d (want 390)", needs.peak, needs.unpacked)
	}
	// The three images of the known-chain test, without chain IDs: with chain IDs they need 5
	// snapshots (3690 unpacked bytes); without, each of the 6 layer occurrences is counted (3990),
	// never less than the known-chain figure.
	needs = newInstallNeeds()
	needs.addImage(&ctrdImage{layers: []ocispec.Descriptor{base, a}}, 0, unpacked)
	needs.addImage(&ctrdImage{layers: []ocispec.Descriptor{base, b}}, 0, unpacked)
	needs.addImage(&ctrdImage{layers: []ocispec.Descriptor{other, base}}, 0, unpacked)
	if needs.peak != 1130 || needs.unpacked != 3990 {
		t.Fatalf("known-chain images without chains: peak %d (want 1130), unpacked %d (want 3990)", needs.peak, needs.unpacked)
	}
}

func TestTreeImageOf(t *testing.T) {
	desc := func(mediaType, name string, size int64) *ocispec.Descriptor {
		return &ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromString(name), Size: size}
	}
	config := &TreeNode{Descriptor: desc(ocispec.MediaTypeImageConfig, "config", 7), Type: BlobTypeImageConfig}
	layers := []*TreeNode{
		{Descriptor: desc(ocispec.MediaTypeImageLayerGzip, "l1", 100), Type: BlobTypeImageLayer},
		{Descriptor: desc(ocispec.MediaTypeImageLayerGzip, "l2", 200), Type: BlobTypeImageLayer},
	}
	manifest := &TreeNode{Descriptor: desc(ocispec.MediaTypeImageManifest, "manifest", 500), Type: BlobTypeImageManifest,
		Children: append([]*TreeNode{config}, layers...)}
	manifest.Descriptor.URLs = []string{"registry/app@" + manifest.Descriptor.Digest.String()}
	index := &TreeNode{Descriptor: desc(ocispec.MediaTypeImageIndex, "index", 300), Type: BlobTypeImageIndex,
		Children: []*TreeNode{manifest}}
	index.Descriptor.URLs = []string{"registry/app@" + index.Descriptor.Digest.String()}

	for _, root := range []*TreeNode{manifest, index} {
		img := treeImageOf(root)
		if img == nil || len(img.layers) != 2 || img.layers[0].Size != 100 || img.layers[1].Size != 200 {
			t.Fatalf("unexpected image from the tree: %+v", img)
		}
		if img.chainIDs != nil {
			t.Fatal("an image from the tree must have no chain IDs")
		}
	}
	noLayers := &TreeNode{Descriptor: manifest.Descriptor, Type: BlobTypeImageManifest, Children: []*TreeNode{config}}
	if treeImageOf(noLayers) != nil {
		t.Fatal("a manifest node without layers must not describe an image")
	}
}
