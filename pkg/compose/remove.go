package compose

import (
	"context"
	"fmt"
	"github.com/containerd/containerd/content/local"
)

// RemoveApps removes app blobs from the app blob store.
// This function does opposite action to the app fetching,
// it does NOT uninstall app, specifically it does NOT remove
// the app's compose project nor app's images from the docker store.
func RemoveApps(ctx context.Context, cfg *Config, appRefs []string) error {
	status, err := CheckAppsStatus(ctx, cfg, appRefs)
	if err != nil {
		return err
	}
	if !status.AreFetched() {
		return fmt.Errorf("cannot remove not fully fetched app(s)")
	}
	cs, err := local.NewStore(cfg.StoreRoot)
	if err != nil {
		return err
	}
	for _, blobs := range status.FetchStatus.BlobsStatus {
		for d := range blobs.BlobsStatus {
			if err := cs.Delete(ctx, d); err != nil {
				return err
			}
		}
	}
	return nil
}
