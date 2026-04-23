package compose

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/containerd/containerd/reference/docker"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
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
	ErrUninstallRunningApps = errors.New("failed to uninstall apps: some apps are still running, please stop them first")

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
		if err = os.RemoveAll(cfg.GetAppComposeDir(app.Name())); err != nil {
			return err
		}
	}

	if opts.Prune {
		cli, errClient := GetDockerClient(cfg.DockerHost)
		if errClient != nil {
			return fmt.Errorf("failed to create docker client: %w", errClient)
		}

		var err error
		var allImages []image.Summary
		if allImages, err = cli.ImageList(ctx, types.ImageListOptions{All: true}); err != nil {
			return fmt.Errorf("failed to list images: %w", err)
		}

		imagesNotInUse := make(map[string]image.Summary)
		for _, img := range allImages {
			imagesNotInUse[img.ID] = img
		}

		var allContainers []types.Container
		if allContainers, err = cli.ContainerList(ctx, container.ListOptions{All: true}); err != nil {
			return fmt.Errorf("failed to list containers: %w", err)
		}
		for _, ctr := range allContainers {
			delete(imagesNotInUse, ctr.ImageID)
		}

		switch opts.PruneType {
		case PruneTypeAllUnusedImages:
			// Remove all images that are not in use by any container.
			for imgID := range imagesNotInUse {
				// TODO: print debug message about which image is being removed and any error that occurs during removal.
				_, _ = cli.ImageRemove(ctx, imgID, types.ImageRemoveOptions{Force: true, PruneChildren: true})
			}
		case PruneTypeOnlyAppImages:
			// Build a map of image refs to image summary for images that are not in use by any container.
			// We will use this map to check if an image ref related to the uninstalled apps is used by any container before removing it.
			imageRefsNotInUse := make(map[string]image.Summary)
			setAllImageRefVariants := func(ref string) {
				imageRefsNotInUse[ref] = imagesNotInUse[ref]
				// Make sure to consider all variants of the same image ref, "normalized" and "familiar"
				if anyRef, err := docker.ParseAnyReference(ref); err == nil {
					imageRefsNotInUse[anyRef.String()] = imagesNotInUse[ref]
					if familiarRef := docker.FamiliarString(anyRef); len(familiarRef) > 0 {
						imageRefsNotInUse[familiarRef] = imagesNotInUse[ref]
					}
				}
			}
			for _, img := range imagesNotInUse {
				for _, ref := range img.RepoDigests {
					setAllImageRefVariants(ref)
				}
				for _, ref := range img.RepoTags {
					setAllImageRefVariants(ref)
				}
			}
			// Remove images that are referenced by the apps being uninstalled and are not in use by any container.
			removeAppImageRefs(ctx, status.Apps, cli, imageRefsNotInUse)
		}
	}
	return err
}

func removeAppImageRefs(ctx context.Context, apps []App, cli *client.Client, imageRefsNotInUse map[string]image.Summary) {
	// Collect image refs related to the uninstalled apps.
	var imageRefsToPrune []string
	for _, app := range apps {
		for _, imageRoot := range app.GetComposeRoot().Children {
			curImageRoot := imageRoot
			for {
				imageRef := curImageRoot.Ref()
				// Add a digest ref
				imageRefsToPrune = append(imageRefsToPrune, imageRef)
				if ref, err := ParseImageRef(imageRef); err == nil {
					// Add a tag ref
					imageRefsToPrune = append(imageRefsToPrune, ref.GetTagRef())
				}
				if curImageRoot.Type == BlobTypeImageManifest || len(curImageRoot.Children) == 0 {
					break
				}
				// the image root points to an image index, let's add refs that point to the image manifest
				curImageRoot = curImageRoot.Children[0]
			}
		}
	}
	// Remove image refs related to the uninstalled apps and images the refs point to are not in use by any container.
	// If the removed ref is the only ref for the image, the image will also be removed;
	// if there are other refs for the image, only the removed ref will be removed.
	// This is the best effort to remove images related to the uninstalled apps without
	// affecting other apps that may share the same images.
	// In some case it can remove an image for which there is no container but some other utility reference it
	// by the same reference as the uninstalled app, but that is an acceptable edge case and best effort
	// to clean up images related to the uninstalled apps.
	for _, ref := range imageRefsToPrune {
		if _, notInUse := imageRefsNotInUse[ref]; notInUse {
			// TODO: print debug message about which image is being removed and any error that occurs during removal.
			_, _ = cli.ImageRemove(ctx, ref, types.ImageRemoveOptions{Force: false, PruneChildren: true})
		}
	}
}
