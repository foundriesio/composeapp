package update

import (
	"context"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
)

type (
	CompleteOpts struct {
		Prune bool
	}
	CompleteOpt func(*CompleteOpts)
)

func CompleteWithPruning() CompleteOpt {
	return func(opts *CompleteOpts) {
		opts.Prune = true
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
			if _, ok := updateApps[app]; !ok {
				appsToPrune = append(appsToPrune, app)
			}
		}
		if err := compose.UninstallApps(ctx, u.config, appsToPrune, compose.WithImagePruning()); err != nil {
			return err
		}
		if err := compose.RemoveApps(ctx, u.config, appsToPrune, compose.WithoutCheckStatus()); err != nil {
			return err
		}
	}

	// Prune blobs in the app store
	store, err := u.config.AppStoreFactory(u.config)
	if err != nil {
		return err
	}
	_, err = store.Prune(ctx)
	return err
}
