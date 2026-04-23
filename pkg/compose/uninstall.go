package compose

import (
	"context"
	"errors"
	"github.com/docker/docker/api/types/filters"
	"os"
)

type (
	UninstallOpts struct {
		Prune     bool
		PruneType PruneType
	}
	UninstallOpt func(*UninstallOpts)

	PruneType string
)

var (
	ErrUninstallRunningApps            = errors.New("failed to uninstall apps: some apps are still running, please stop them first")
	PruneTypeAllUnusedImages PruneType = "all-unused-images"
	PruneTypeOnlyAppImages   PruneType = "only-app-images"
)

func WithImagePruning(pruneType ...PruneType) UninstallOpt {
	return func(opts *UninstallOpts) {
		opts.Prune = true
		opts.PruneType = PruneTypeOnlyAppImages
		if len(pruneType) > 0 {
			opts.PruneType = pruneType[0]
		}
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
			// Multiple versions of the same app exist in the store.
			// If the version being removed is not installed, another version may still be
			// installed and using the same compose directory. In that case, keep the app
			// compose directory; otherwise we could remove compose files needed by the
			// other installed version.
			if _, isNotInstalled := status.NotInstalledCompose[app.Ref().Digest]; isNotInstalled {
				continue
			}
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
		var dangling string
		switch opts.PruneType {
		case PruneTypeAllUnusedImages:
			dangling = "false"
		case PruneTypeOnlyAppImages:
			dangling = "true"
		}
		_, err = cli.ImagesPrune(ctx, filters.NewArgs(filters.Arg("dangling", dangling)))
	}
	return err
}
