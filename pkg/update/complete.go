package update

import (
	"context"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"io/fs"
	"os"
	"path/filepath"
)

func (u *runnerImpl) complete(ctx context.Context) error {

	cs, err := v1.NewAppStore(u.config.StoreRoot, u.config.Platform, false)
	if err != nil {
		return err
	}

	apps := map[string]compose.App{}
	appNames := map[string]struct{}{}
	for _, appURI := range u.URIs {
		app, err := u.config.AppLoader.LoadAppTree(ctx, cs, platforms.OnlyStrict(u.config.Platform), appURI)
		if err != nil {
			return err
		}
		apps[appURI] = app
		appNames[app.Name()] = struct{}{}
	}

	missingBlobs := map[string]string{}
	appBlobs := make(map[string]struct{})
	for appURI, app := range apps {
		err = app.Tree().Walk(func(node *compose.TreeNode, depth int) error {
			// Check if all app blobs are present
			blobURI := node.Descriptor.URLs[0]
			checkOpts := []compose.SecureReadOptions{
				compose.WithExpectedSize(node.Descriptor.Size),
				compose.WithExpectedDigest(node.Descriptor.Digest),
				compose.WithRef(blobURI),
			}
			bs, stateCheckErr := compose.CheckBlob(compose.WithAppRef(compose.WithBlobType(ctx, node.Type),
				apps[appURI].Ref()), cs, node.Descriptor.Digest, checkOpts...)
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

	// remove blobs that are not in the update apps, but are in the store
	// walk the store and remove any blobs that are not in the app blobs
	err = filepath.Walk(u.config.GetBlobsRoot(), func(path string, f fs.FileInfo, err error) error {
		if f.IsDir() {
			return nil
		}
		if _, ok := appBlobs[f.Name()]; !ok {
			fmt.Printf("removing blob %s\n", path)
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("failed to remove blob %s: %w", path, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to remove unused blobs from the app store: %w", err)
	}

	err = filepath.Walk(u.config.ComposeRoot, func(path string, f fs.FileInfo, err error) error {
		if !f.IsDir() {
			return nil
		}
		if path == u.config.ComposeRoot {
			return nil
		}
		if _, ok := appNames[f.Name()]; !ok {
			fmt.Printf("removing app compose dir: %s\n", path)
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("failed to remove app compose project; path: %s, err: %s", path, err.Error())
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to remove unused app compose projects: %w", err)
	}

	// TODO: prune/remove unused images from the docker store

	return err
}
