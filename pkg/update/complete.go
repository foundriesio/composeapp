package update

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/platforms"
	"github.com/docker/docker/api/types/filters"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
)

type (
	CompleteOpts struct {
		Prune                bool
		Force                bool
		PruneAllUnusedImages bool
	}
	CompleteOpt func(*CompleteOpts)
)

func CompleteWithPruning(pruneAllUnusedImages ...bool) CompleteOpt {
	return func(opts *CompleteOpts) {
		opts.Prune = true
		if len(pruneAllUnusedImages) > 0 {
			opts.PruneAllUnusedImages = pruneAllUnusedImages[0]
		}
	}
}

func CompleteWithForce() CompleteOpt {
	return func(opts *CompleteOpts) {
		opts.Force = true
	}
}

func (u *runnerImpl) complete(ctx context.Context, options ...CompleteOpt) error {
	opts := CompleteOpts{}
	for _, o := range options {
		o(&opts)
	}
	cs, err := v1.NewAppStore(u.config.StoreRoot, u.config.Platform, false)
	if err != nil {
		return err
	}

	updateApps := map[string]compose.App{}
	appNames := map[string]struct{}{}
	for _, appURI := range u.URIs {
		app, err := u.config.AppLoader.LoadAppTree(ctx, cs, platforms.OnlyStrict(u.config.Platform), appURI)
		if err != nil {
			return err
		}
		updateApps[appURI] = app
		appNames[app.Name()] = struct{}{}
	}

	missingBlobs := map[string]string{}
	appBlobs := make(map[string]struct{})
	for appURI, app := range updateApps {
		err = app.Tree().Walk(func(node *compose.TreeNode, depth int) error {
			// Check if all app blobs are present
			blobURI := node.Descriptor.URLs[0]
			checkOpts := []compose.SecureReadOptions{
				compose.WithExpectedSize(node.Descriptor.Size),
				compose.WithExpectedDigest(node.Descriptor.Digest),
				compose.WithRef(blobURI),
			}
			bs, stateCheckErr := compose.CheckBlob(compose.WithAppRef(compose.WithBlobType(ctx, node.Type),
				updateApps[appURI].Ref()), cs, node.Descriptor.Digest, checkOpts...)
			if stateCheckErr != nil {
				return stateCheckErr
			}

			appBlobs[node.Descriptor.Digest.Encoded()] = struct{}{}
			if bs != compose.BlobOk {
				missingBlobs[blobURI] = node.Descriptor.Digest.Encoded()
			}

			return nil
		})
		if err != nil {
			return fmt.Errorf("failed to check if all app blobs are present in the app store: %w", err)
		}
	}
	if len(missingBlobs) > 0 {
		return fmt.Errorf("update cannot be completed; missing blobs are found: %d", len(missingBlobs))
	}

	if opts.Prune {
		currentApps, err := compose.ListApps(ctx, u.config)
		if err != nil {
			return err
		}
		var appsToPrune []string
		for _, app := range currentApps {
			if _, ok := updateApps[app.Ref().String()]; !ok {
				appsToPrune = append(appsToPrune, app.Ref().String())
			}
		}
		if len(appsToPrune) > 0 {
			if err := compose.UninstallApps(ctx, u.config, appsToPrune, compose.WithImagePruning(opts.PruneAllUnusedImages)); err != nil {
				return err
			}
			if err := compose.RemoveApps(ctx, u.config, appsToPrune, compose.WithCheckStatus(false)); err != nil {
				return err
			}
		} else if opts.PruneAllUnusedImages {
			// If no apps are removed, we can still prune the images that are not used by any app.
			if dockerClient, errClient := compose.GetDockerClient(u.config.DockerHost); errClient == nil {
				_, err = dockerClient.ImagesPrune(ctx, filters.NewArgs(filters.Arg("dangling", "false")))
				return fmt.Errorf("failed to prune unused images: %w", err)
			} else {
				return fmt.Errorf("failed to create docker client for image pruning: %w", errClient)
			}
		}
	}

	// Prune blobs in the app store
	store, err := u.config.AppStoreFactory()
	if err != nil {
		return err
	}
	_, err = store.Prune(ctx)
	return err
}
