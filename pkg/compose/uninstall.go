package compose

import (
	"context"
	"errors"
	"github.com/docker/docker/api/types/filters"
	"os"
)

type (
	UninstallOpts struct {
		Prune bool
	}
	UninstallOpt func(*UninstallOpts)
)

var (
	ErrUninstallRunningApps = errors.New("failed to uninstall apps: some apps are still running, please stop them first")
)

func WithImagePruning() UninstallOpt {
	return func(opts *UninstallOpts) {
		opts.Prune = true
	}
}

func UninstallApps(ctx context.Context, cfg *Config, appRefs []string, options ...UninstallOpt) error {
	if len(appRefs) == 0 {
		return nil
	}
	opts := &UninstallOpts{}
	for _, o := range options {
		o(opts)
	}
	status, err := CheckAppsStatus(ctx, cfg, appRefs)
	if err != nil {
		return err
	}
	if status.AreRunning() {
		return ErrUninstallRunningApps
	}

	store, err := cfg.AppStoreFactory()
	if err != nil {
		return err
	}
	appInStoreRefs, err := store.ListApps(ctx)
	if err != nil {
		return err
	}
	appsInStore := make(map[string]int)
	for _, ref := range appInStoreRefs {
		appsInStore[ref.Name] += 1
	}
	for _, app := range status.Apps {
		if appsInStore[app.Name()] > 1 {
			// Cannot remove compose app dir if there is another version of this app in the store
			continue
		}
		err = os.RemoveAll(cfg.GetAppComposeDir(app.Name()))
		if err != nil {
			return err
		}
	}

	if opts.Prune {
		cli, errClient := GetDockerClient(cfg.DockerHost)
		if errClient != nil {
			return errClient
		}
		// Prune only dangling images.
		// The dangling images are the ones that are not tagged and not referenced by any container.
		// TODO: consider pruning volumes and networks if needed.
		// TODO: consider pruning only those images that are related to the uninstalled apps,
		//       otherwise it prunes all dangling images including those that are not managed by composectl
		_, err = cli.ImagesPrune(ctx, filters.NewArgs(filters.Arg("dangling", "true")))
	}
	return err
}
