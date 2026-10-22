package compose

import (
	"context"
	"fmt"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// CtrdUnpackSize returns the unpacked size of the image layers that installing the apps into
// dockerd's containerd has to unpack: in each image, the layer of the first missing snapshot and
// every layer above it. onCtrd is false when the images are loaded with `docker load` instead.
func CtrdUnpackSize(ctx context.Context, cfg *Config, apps []App, provider BlobProvider) (size int64, onCtrd bool, err error) {
	c, err := ctrdIfInstallTarget(ctx, cfg)
	if err != nil || c == nil {
		return 0, false, err
	}
	defer c.Close()
	size, err = unpackSize(ctx, cfg, apps, provider, c.firstMissingSnapshot)
	return size, true, err
}

// unpackSize counts each snapshot once by chain ID across all images; firstMissing returns the
// index of the first layer of a chain whose snapshot does not exist. An image whose manifest or
// config cannot be read has no known chain: all its layers are counted, so the size never drops
// below what the install unpacks.
func unpackSize(ctx context.Context, cfg *Config, apps []App, provider BlobProvider,
	firstMissing func(context.Context, []digest.Digest) (int, error)) (int64, error) {
	var size int64
	counted := map[digest.Digest]bool{}
	for _, app := range apps {
		runtimeSize := func(layer *ocispec.Descriptor) int64 {
			return app.GetBlobRuntimeSize(layer, cfg.Platform.Architecture, cfg.BlockSize)
		}
		for _, root := range app.GetComposeRoot().Children {
			img, err := ctrdImageOf(ctx, root, provider)
			if err != nil {
				size += layersSize(treeLayersOf(root), nil, counted, runtimeSize)
				continue
			}
			first, err := firstMissing(ctx, img.chainIDs)
			if err != nil {
				return 0, fmt.Errorf("failed to scan the snapshots of %s: %w", img.ref, err)
			}
			size += layersSize(img.layers[first:], img.chainIDs[first:], counted, runtimeSize)
		}
	}
	return size, nil
}

// layersSize sums the runtime size of the layers whose chain ID is not counted yet; without chain
// IDs every layer is summed.
func layersSize(layers []ocispec.Descriptor, chainIDs []digest.Digest, counted map[digest.Digest]bool,
	runtimeSize func(*ocispec.Descriptor) int64) (size int64) {
	for i := range layers {
		if chainIDs != nil && counted[chainIDs[i]] {
			continue
		}
		if chainIDs != nil {
			counted[chainIDs[i]] = true
		}
		size += runtimeSize(&layers[i])
	}
	return size
}

// treeLayersOf returns the layer descriptors the app tree holds for an image.
func treeLayersOf(root *TreeNode) []ocispec.Descriptor {
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
	return layers
}
