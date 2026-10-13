package compose

import "context"

// Exported for the tests in package compose_test that drive the containerd install directly.

type CtrdClient = ctrdClient

var (
	ConnectCtrdIfInstallTarget = connectCtrdIfInstallTarget
	InstallImagesCtrd          = installImagesCtrd
)

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
