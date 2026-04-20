package compose

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerClient "github.com/docker/docker/client"
)

type (
	UninstallOpts struct {
		Prune                 bool
		RemoveAllUnusedImages bool
	}
	UninstallOpt func(*UninstallOpts)
)

var (
	ErrUninstallRunningApps = errors.New("failed to uninstall apps: some apps are still running, please stop them first")
)

func WithImagePruning(allUnused ...bool) UninstallOpt {
	return func(opts *UninstallOpts) {
		opts.Prune = true
		if len(allUnused) > 0 {
			opts.RemoveAllUnusedImages = allUnused[0]
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
			// Cannot remove compose app dir if there is another version of this app in the store
			continue
		}
		err = os.RemoveAll(cfg.GetAppComposeDir(app.Name()))
		if err != nil {
			return err
		}
	}

	if opts.Prune {
		dockerClient, errClient := GetDockerClient(cfg.DockerHost)
		if errClient != nil {
			return errClient
		}

		if opts.RemoveAllUnusedImages {
			// Prune all unused images, including dangling and non-dangling ones.
			// The non-dangling images are the ones that are not referenced by any container, but they can still be tagged.
			_, err = dockerClient.ImagesPrune(ctx, filters.NewArgs(filters.Arg("dangling", "false")))
			return err
		}

		// Get all containers to find out which images are still referenced by the containers, so that we don't prune those images.
		allContainers, errCtrList := dockerClient.ContainerList(ctx, container.ListOptions{})
		if errCtrList != nil {
			return fmt.Errorf("failed to list containers: %w", errCtrList)
		}
		imagesWithContainer := make(map[string]types.Container)
		for _, container := range allContainers {
			imagesWithContainer[container.Image] = container
		}

		// Remove or untag images that thar are referenced by the compose apps to be uninstalled, but not referenced by any container.
		// We cannot even untag images that are still referenced by the containers, if we do then
		// composectl will think the app that uses that image is uninstalled.
		for _, app := range status.Apps {
			for _, imageRoot := range app.GetComposeRoot().Children {
				if _, hasContainer := imagesWithContainer[imageRoot.Ref()]; !hasContainer {
					removeImage(ctx, dockerClient, imageRoot)
				}
			}
		}
		// Prune dangling images, which are the ones that are not tagged and not referenced by any container.
		_, err = dockerClient.ImagesPrune(ctx, filters.NewArgs(filters.Arg("dangling", "true")))
	}
	return err
}

func removeImage(ctx context.Context, client *dockerClient.Client, imageRoot *TreeNode) {
	imageNode := imageRoot
	for {
		// remove image (untag if more than 2 references) referenced by the URI with a digest
		// Ignore error because the image may have already been removed as a child of another image,
		// or the image may be referenced by other compose apps that are running or not uninstalled.
		_, _ = client.ImageRemove(ctx, imageNode.Ref(), types.ImageRemoveOptions{Force: false, PruneChildren: true})
		if imageRef, refParseErr := ParseImageRef(imageNode.Ref()); refParseErr == nil {
			// remove image (untag if more than 2 references) referenced by the URI with a tag
			// Ignore error because the image may have already been removed as a child of another image,
			// or the image may be referenced by other compose apps that are running or not uninstalled.
			_, _ = client.ImageRemove(ctx, imageRef.GetTagRef(), types.ImageRemoveOptions{Force: false, PruneChildren: true})
		}
		if imageNode.Type == BlobTypeImageManifest || len(imageNode.Children) == 0 {
			break
		}
		// The image root points to the image index, which was removed, now remove the image manifest
		imageNode = imageNode.Children[0]
	}
}
