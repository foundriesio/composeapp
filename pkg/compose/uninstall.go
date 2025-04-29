package compose

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types/filters"
	"os"
)

type (
	UninstallOpts struct {
		Prune bool
	}
	UninstallOpt func(*UninstallOpts)
)

func WithImagePruning() UninstallOpt {
	return func(opts *UninstallOpts) {
		opts.Prune = true
	}
}

func UninstallApps(ctx context.Context, cfg *Config, appRefs []string, options ...UninstallOpt) error {
	opts := &UninstallOpts{}
	for _, o := range options {
		o(opts)
	}
	status, err := CheckAppsStatus(ctx, cfg, appRefs)
	if err != nil {
		return err
	}
	if status.AreRunning() {
		return fmt.Errorf("cannot uninstall running app(s)")
	}
	for _, app := range status.Apps {
		err = os.RemoveAll(cfg.GetAppComposeDir(app.Name()))
		if err != nil {
			return err
		}
	}
	if opts.Prune {
		// Prune unused images, it should remove app images of stopped apps
		// from the docker store, unless they are used by some 3rd party containers/apps
		// TODO: don't remove unused images that does not belong to any of the specified apps
		// If the same image is used by one of the specified apps and some 3rd party app - how
		// to figure out it so we can skip this image removal
		cli, errClient := GetDockerClient(cfg.DockerHost)
		if errClient != nil {
			return errClient
		}
		_, err = cli.ImagesPrune(ctx, filters.NewArgs(filters.Arg("dangling", "false")))
	}
	return err
}
