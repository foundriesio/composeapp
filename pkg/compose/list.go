package compose

import (
	"context"
	"fmt"
	"github.com/containerd/containerd/platforms"
)

func ListApps(ctx context.Context, cfg *Config) ([]string, error) {
	store, err := cfg.AppStoreFactory(cfg)
	if err != nil {
		return nil, err
	}
	appRefs, err := store.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	var apps []string
	for _, ref := range appRefs {
		if app, err := cfg.AppLoader.LoadAppTree(ctx, store, platforms.OnlyStrict(cfg.Platform), ref.String()); err == nil {
			apps = append(apps, app.Ref().String())
		} else {
			// TODO: return list of found but incomplete apps - apps that are missing some blobs
			fmt.Printf("failed to load app tree; app: %s, err: %s\n", app.Name(), err)
		}
	}
	return apps, nil
}
