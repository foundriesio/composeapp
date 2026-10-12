package compose

import (
	"context"
	"fmt"
	"syscall"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type (
	// StorageNeed is the number of bytes an operation needs on the filesystem that holds Path.
	StorageNeed struct {
		Path     string `json:"path"`
		Required int64  `json:"required"`
	}

	// appImages is an app with its images as the containerd install sees them.
	appImages struct {
		app  App
		imgs []*ctrdImage
	}

	// installNeeds accumulates, over all images, what the containerd install still has to write.
	installNeeds struct {
		streamed  map[digest.Digest]struct{} // the layer blobs that install streams
		snapshots map[digest.Digest]struct{} // the chain IDs of the snapshots that install creates
		peak      int64                      // compressed bytes streamed into the content store
		unpacked  int64                      // unpacked bytes of the snapshots
	}
)

// StorageNeeds sizes what fetching and installing the apps still needs, per path. storeSize and
// runtimeSize are the store and runtime sizes of the missing blobs, as `check` reports them.
//
// On a daemon whose images are installed into containerd there are four terms: the missing blobs in
// the store, the extracted app bundles, the compressed layers that install streams into the content
// store (the install peak) and the unpacked size of the snapshots that do not exist yet. On a daemon
// that loads images with `docker load` there is one term at StoreRoot, as before.
func StorageNeeds(ctx context.Context, cfg *Config, provider BlobProvider, apps []App,
	storeSize int64, runtimeSize int64) ([]StorageNeed, error) {
	singleTerm := []StorageNeed{{Path: cfg.StoreRoot, Required: storeSize + runtimeSize}}
	c, err := ctrdIfInstallTarget(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to find the install target: %w", err)
	}
	if c == nil {
		return singleTerm, nil
	}
	defer c.Close()
	imgs, ok := appImagesOf(ctx, apps, provider)
	if !ok {
		// The layers of an image are not known at all: its manifest is not in the app tree. The
		// fetch check reports the missing blob; size as a `docker load` host does rather than fail.
		return singleTerm, nil
	}
	needs, err := c.installNeeds(ctx, cfg, imgs)
	if err != nil {
		return nil, fmt.Errorf("failed to size the install: %w", err)
	}
	return []StorageNeed{
		{Path: cfg.StoreRoot, Required: storeSize},
		{Path: cfg.ComposeRoot, Required: bundlesSize(apps, cfg)},
		{Path: c.contentRoot, Required: needs.peak},
		{Path: c.snapshotterRoot, Required: needs.unpacked},
	}, nil
}

// appImagesOf describes the images of the apps for the sizing. ok is false when the layers of an
// image are not known at all.
func appImagesOf(ctx context.Context, apps []App, provider BlobProvider) ([]appImages, bool) {
	var all []appImages
	for _, app := range apps {
		imgs, ok := sizingImagesOf(ctx, app, provider)
		if !ok {
			return nil, false
		}
		all = append(all, appImages{app: app, imgs: imgs})
	}
	return all, true
}

// sizingImagesOf is ctrdImagesOf for the sizing: an image whose manifest or config cannot be read
// (an interrupted fetch, a broken blob in a local check) is described from the app tree instead,
// without chain IDs, and is then sized as if every one of its layers had to be streamed and
// unpacked. ok is false when not even the tree knows the layers of an image.
func sizingImagesOf(ctx context.Context, app App, provider BlobProvider) ([]*ctrdImage, bool) {
	var imgs []*ctrdImage
	for _, root := range app.GetComposeRoot().Children {
		img, err := ctrdImageOf(ctx, root, provider)
		if err != nil {
			img = treeImageOf(root)
		}
		if img == nil {
			return nil, false
		}
		imgs = append(imgs, img)
	}
	return imgs, true
}

// treeImageOf describes an image from the app tree alone: the layer descriptors the fetch check
// loaded with the manifest. It is nil when the tree has no layers for the image.
func treeImageOf(root *TreeNode) *ctrdImage {
	mNode, err := platformManifestNode(root)
	if err != nil {
		return nil
	}
	var layers []ocispec.Descriptor
	for _, child := range mNode.Children {
		if child.Type == BlobTypeImageLayer {
			layers = append(layers, *child.Descriptor)
		}
	}
	if len(layers) == 0 {
		return nil
	}
	return &ctrdImage{ref: root.Ref(), layers: layers}
}

func (c *ctrdClient) installNeeds(ctx context.Context, cfg *Config, apps []appImages) (*installNeeds, error) {
	needs := newInstallNeeds()
	for _, a := range apps {
		for _, img := range a.imgs {
			first := 0 // an image without chain IDs is sized as if every layer were missing
			if img.chainIDs != nil {
				var err error
				if first, err = c.firstMissingSnapshot(ctx, img.chainIDs); err != nil {
					return nil, fmt.Errorf("failed to scan the snapshots of %s: %w", img.ref, err)
				}
			}
			needs.addImage(img, first, func(layer *ocispec.Descriptor) int64 {
				return a.app.GetBlobRuntimeSize(layer, cfg.Platform.Architecture, cfg.BlockSize)
			})
		}
	}
	return needs, nil
}

func newInstallNeeds() *installNeeds {
	return &installNeeds{
		streamed:  map[digest.Digest]struct{}{},
		snapshots: map[digest.Digest]struct{}{},
	}
}

// addImage counts the layers from first on. A layer blob is streamed once however many images share
// it; a snapshot is counted once by chain ID, even when its blob is in the store already, because
// the same blob above another parent is another snapshot. An image without chain IDs (its config
// could not be read) has every layer counted as a snapshot of its own: without the chain it is
// not known which parents it shares with other images, so nothing is deduplicated.
func (n *installNeeds) addImage(img *ctrdImage, first int, unpackedSize func(*ocispec.Descriptor) int64) {
	for i := first; i < len(img.layers); i++ {
		layer := img.layers[i]
		if _, ok := n.streamed[layer.Digest]; !ok {
			n.streamed[layer.Digest] = struct{}{}
			n.peak += layer.Size
		}
		if img.chainIDs == nil {
			n.unpacked += unpackedSize(&layer)
			continue
		}
		if _, ok := n.snapshots[img.chainIDs[i]]; !ok {
			n.snapshots[img.chainIDs[i]] = struct{}{}
			n.unpacked += unpackedSize(&layer)
		}
	}
}

func bundlesSize(apps []App, cfg *Config) (size int64) {
	for _, app := range apps {
		size += app.GetBlobRuntimeSize(app.GetComposeRoot().Descriptor, cfg.Platform.Architecture, cfg.BlockSize)
	}
	return size
}

// MergeStorageNeeds adds up the needs whose paths are on the same filesystem (st_dev). The result
// has one entry per filesystem, under the first of its paths.
func MergeStorageNeeds(needs []StorageNeed) ([]StorageNeed, error) {
	devices := make([]uint64, len(needs))
	for i, n := range needs {
		var st syscall.Stat_t
		if err := syscall.Stat(n.Path, &st); err != nil {
			return nil, fmt.Errorf("failed to stat %s: %w", n.Path, err)
		}
		devices[i] = uint64(st.Dev) // Dev is 32 bits wide on some architectures
	}
	return mergeByDevice(needs, devices), nil
}

func mergeByDevice(needs []StorageNeed, devices []uint64) []StorageNeed {
	var merged []StorageNeed
	index := map[uint64]int{}
	for i, n := range needs {
		if j, ok := index[devices[i]]; ok {
			merged[j].Required += n.Required
			continue
		}
		index[devices[i]] = len(merged)
		merged = append(merged, n)
	}
	return merged
}

// CheckStorage compares what is needed on each filesystem with the space available on it, after
// the watermark or reserved storage is kept back.
func CheckStorage(needs []StorageNeed, watermark uint64, watermarkInBytes bool) ([]*UsageInfo, error) {
	merged, err := MergeStorageNeeds(needs)
	if err != nil {
		return nil, err
	}
	var usage []*UsageInfo
	for _, n := range merged {
		ui, err := GetUsageInfo(n.Path, n.Required, watermark, watermarkInBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to get usage of %s: %w", n.Path, err)
		}
		usage = append(usage, ui)
	}
	return usage, nil
}
