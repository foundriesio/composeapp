package compose

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// sizingApp is an App with a compose root of images whose layers unpack to three times their size.
type sizingApp struct {
	App
	root *TreeNode
}

func (a *sizingApp) GetComposeRoot() *TreeNode { return a.root }

func (a *sizingApp) GetBlobRuntimeSize(desc *ocispec.Descriptor, _ string, _ int64) int64 {
	return desc.Size * 3
}

func sizingLayer(name string, size int64) ocispec.Descriptor {
	return ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromString(name), Size: size}
}

func diffID(layer ocispec.Descriptor) digest.Digest {
	return digest.FromString("diff:" + layer.Digest.String())
}

// sizingImage puts the manifest and, when withConfig is set, the config of an image into blobs and
// returns the image's node in the app tree.
func sizingImage(t *testing.T, blobs map[digest.Digest][]byte, name string, withConfig bool,
	layers ...ocispec.Descriptor) *TreeNode {
	t.Helper()
	var config ocispec.Image
	for _, l := range layers {
		config.RootFS.DiffIDs = append(config.RootFS.DiffIDs, diffID(l))
	}
	configBytes, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig,
		Digest: digest.FromBytes(configBytes), Size: int64(len(configBytes))}
	manifestBytes, err := json.Marshal(ocispec.Manifest{Config: configDesc, Layers: layers})
	if err != nil {
		t.Fatal(err)
	}
	manifestDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest,
		Digest: digest.FromBytes(manifestBytes), Size: int64(len(manifestBytes))}
	blobs[manifestDesc.Digest] = manifestBytes
	if withConfig {
		blobs[configDesc.Digest] = configBytes
	}
	ref := func(d digest.Digest) []string { return []string{"registry.example/factory/" + name + "@" + d.String()} }
	configDesc.URLs = ref(configDesc.Digest)
	manifestDesc.URLs = ref(manifestDesc.Digest)
	node := &TreeNode{Descriptor: &manifestDesc, Type: BlobTypeImageManifest,
		Children: []*TreeNode{{Descriptor: &configDesc, Type: BlobTypeImageConfig}}}
	for i := range layers {
		node.Children = append(node.Children, &TreeNode{Descriptor: &layers[i], Type: BlobTypeImageLayer})
	}
	return node
}

// snapshotsOf is a snapshot lookup in which the snapshots of the given layer chains exist, each
// with its parents.
func snapshotsOf(chains ...[]ocispec.Descriptor) func(context.Context, []digest.Digest) (int, error) {
	exist := map[digest.Digest]bool{}
	for _, chain := range chains {
		var diffIDs []digest.Digest
		for _, l := range chain {
			diffIDs = append(diffIDs, diffID(l))
		}
		for _, id := range identity.ChainIDs(diffIDs) {
			exist[id] = true
		}
	}
	return func(_ context.Context, chainIDs []digest.Digest) (int, error) {
		for i, id := range chainIDs {
			if !exist[id] {
				return i, nil
			}
		}
		return len(chainIDs), nil
	}
}

func TestUnpackSize(t *testing.T) {
	base, a, b := sizingLayer("base", 100), sizingLayer("a", 10), sizingLayer("b", 20)
	cfg := &Config{}
	size := func(withConfig bool, lookup func(context.Context, []digest.Digest) (int, error)) int64 {
		t.Helper()
		blobs := map[digest.Digest][]byte{}
		root := &TreeNode{Children: []*TreeNode{
			sizingImage(t, blobs, "one", true, base, a),
			sizingImage(t, blobs, "two", withConfig, base, b),
		}}
		got, err := unpackSize(context.Background(), cfg, []App{&sizingApp{root: root}}, NewMemoryBlobProvider(blobs), lookup)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	// No snapshot exists, as after a prune: every layer, the shared base once.
	if got := size(true, snapshotsOf()); got != 3*(100+10+20) {
		t.Errorf("no snapshots: got %d, want %d", got, 3*(100+10+20))
	}
	// The base snapshot exists: only the layers above it.
	if got := size(true, snapshotsOf([]ocispec.Descriptor{base})); got != 3*(10+20) {
		t.Errorf("base snapshot exists: got %d, want %d", got, 3*(10+20))
	}
	// Everything is unpacked already.
	if got := size(true, snapshotsOf([]ocispec.Descriptor{base, a}, []ocispec.Descriptor{base, b})); got != 0 {
		t.Errorf("all snapshots exist: got %d, want 0", got)
	}
	// The second image has no config in the store: all its layers count, even the shared base.
	if got := size(false, snapshotsOf([]ocispec.Descriptor{base})); got != 3*(10+100+20) {
		t.Errorf("unreadable config: got %d, want %d", got, 3*(10+100+20))
	}
	if got := size(false, snapshotsOf()); got != 3*(100+10+100+20) {
		t.Errorf("unreadable config, no snapshots: got %d, want %d", got, 3*(100+10+100+20))
	}
}

func TestUnpackSizeFailsOnSnapshotError(t *testing.T) {
	blobs := map[digest.Digest][]byte{}
	root := &TreeNode{Children: []*TreeNode{sizingImage(t, blobs, "one", true, sizingLayer("base", 100))}}
	failing := func(context.Context, []digest.Digest) (int, error) { return 0, errors.New("unavailable") }
	_, err := unpackSize(context.Background(), &Config{}, []App{&sizingApp{root: root}}, NewMemoryBlobProvider(blobs), failing)
	if err == nil {
		t.Fatal("expected the snapshot lookup error")
	}
}
