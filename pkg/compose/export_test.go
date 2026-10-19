package compose

import (
	"context"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Exported for the tests in package compose_test that drive the containerd install directly.

type CtrdClient = ctrdClient

var (
	ConnectCtrdIfInstallTarget = connectCtrdIfInstallTarget
	InstallImagesCtrd          = installImagesCtrd
)

func RegisterLayer(ctx context.Context, c *CtrdClient, blobsRoot string, desc ocispec.Descriptor) error {
	return c.registerLayer(ctx, blobsRoot, desc)
}

func CtrdSnapshotter(c *CtrdClient) string {
	return c.snapshotter
}

// ChainIDsOf returns the chain IDs of each app image, in the app's image order.
func ChainIDsOf(ctx context.Context, app App, provider BlobProvider) ([][]string, error) {
	imgs, err := ctrdImagesOf(ctx, app, provider)
	if err != nil {
		return nil, err
	}
	var out [][]string
	for _, img := range imgs {
		var ids []string
		for _, id := range img.chainIDs {
			ids = append(ids, id.String())
		}
		out = append(out, ids)
	}
	return out, nil
}

func CtrdContentRoot(c *CtrdClient) string {
	return c.contentRoot
}

// LayerDescriptorsOf returns the layer descriptors of each app image, in the app's image order.
func LayerDescriptorsOf(ctx context.Context, app App, provider BlobProvider) ([][]ocispec.Descriptor, error) {
	imgs, err := ctrdImagesOf(ctx, app, provider)
	if err != nil {
		return nil, err
	}
	var out [][]ocispec.Descriptor
	for _, img := range imgs {
		out = append(out, img.layers)
	}
	return out, nil
}
