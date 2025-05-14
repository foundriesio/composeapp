package compose

import (
	"context"
	"github.com/containerd/containerd/platforms"
)

func ListApps(ctx context.Context, cfg *Config) ([]App, error) {
	store, err := cfg.AppStoreFactory()
	if err != nil {
		return nil, err
	}
	appRefs, err := store.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	var apps []App
	for _, ref := range appRefs {
		if app, err := cfg.AppLoader.LoadAppTree(ctx, store, platforms.OnlyStrict(cfg.Platform), ref.String()); err == nil {
			apps = append(apps, app)
		}
		// TODO: handle the `else` case: return list of found but incomplete apps - apps that are missing some blobs
	}
	return apps, nil
}
