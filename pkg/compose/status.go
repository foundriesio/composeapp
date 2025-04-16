package compose

import (
	"context"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/reference/docker"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"path"
)

const (
	AppServiceHashLabelKey = "io.compose-spec.config-hash"
	ServiceLabel           = "com.docker.compose.service"

	ContainerStateRunning = "running"
)

type (
	InstalledImagesInfo struct {
		// Image ID to image summary map (Image ID is the docker's internal ID for the image,
		// not the digest URI of the image).
		InstalledImages map[string]image.Summary
		// Image refs (both digest and tag) to image ID map.
		// The same image can have multiple references.
		InstalledImageRefs map[string]string
	}
	Service struct {
		Name   string `json:"name"`
		Image  string `json:"image"`
		Hash   string `json:"hash"`
		CtrID  string `json:"ctr-id"`
		State  string `json:"state"`
		Status string `json:"status"`
		Health string `json:"health,omitempty"`
	}

	ErrComposeInstall struct {
		Errs AppBundleErrs
	}
	ErrImageInstall struct {
		MissingImages []string
	}
)

func (e *ErrComposeInstall) Error() string {
	return fmt.Sprintf("app compose installation errors: %d", len(e.Errs))
}
func (e *ErrImageInstall) Error() string {
	return fmt.Sprintf("app image installation errors: %d", len(e.MissingImages))
}

func CheckRunning(
	ctx context.Context,
	cfg *Config,
	appRefs []string) error {

	bp := NewStoreBlobProvider(path.Join(cfg.StoreRoot, "blobs", "sha256"))

	apps := map[string]App{}
	for _, appRef := range appRefs {
		// Load app tree, this requires the app manifest and key blobs to be fetched
		app, err := cfg.AppLoader.LoadAppTree(ctx, bp, platforms.OnlyStrict(cfg.Platform), appRef)
		if err != nil {
			return err
		}
		if err := checkComposeInstallation(ctx, cfg, app, bp); err != nil {
			return err
		}
		apps[appRef] = app
	}

	if err := checkAppImagesInstallation(ctx, cfg, apps); err != nil {
		return err
	}

	services, err := checkAppsRunningStatus(ctx, cfg)
	if err != nil {
		return err
	}

	for _, app := range apps {
		// Iterate over each app/compose image and check whether it is installed in the docker store
		for _, imageNode := range app.GetComposeRoot().Children {
			imageRef := imageNode.Ref()
			serviceHash := (*ImageTree)(imageNode).GetServiceHash()
			// check if there is a started container for a given compose service
			var foundContainer bool
			for _, srv := range services {
				if srv.Image == imageRef && srv.Hash == serviceHash && srv.State == ContainerStateRunning {
					foundContainer = true
					break
				}
			}
			//TODO: include more details into the returned error
			if !foundContainer {
				return fmt.Errorf("app service is not started: %s\n", imageRef)
			}
		}
	}
	return nil
}

func checkComposeInstallation(ctx context.Context, cfg *Config, app App, bp BlobProvider) error {
	// Check app compose project installation, this requires the app compose archive to be fetched
	// and extracted into the project/compose root directory
	installErrs, err := app.CheckComposeInstallation(ctx, bp, path.Join(cfg.ComposeRoot, app.Name()))
	if err != nil {
		return err
	}
	if len(installErrs) > 0 {
		return &ErrComposeInstall{Errs: installErrs}
	}
	return nil
}

func checkAppImagesInstallation(ctx context.Context, cfg *Config, apps map[string]App) error {
	// Get info about all images installed in the docker store
	installedImagesInfo, err := GetInstalledImages(ctx, cfg)
	if err != nil {
		return err
	}

	var missingImages []string
	for _, app := range apps {
		// Iterate over each app/compose image and check whether it is installed in the docker store
		for _, imageNode := range app.GetComposeRoot().Children {
			imageRef := imageNode.Ref()
			if imageRef == "" {
				return fmt.Errorf("cannot check whether app image is installed because its reference is missing")
			}
			// If the given app image is among the installed images then continue checking remaining app images
			if _, ok := installedImagesInfo.InstalledImageRefs[imageRef]; ok {
				continue
			}
			// Check if the tagged version of the given app image reference is listed among the installed images
			// It can happen if the dockerd doesn't have the patch making it support the digest refs during image loading
			s, err := reference.Parse(imageRef)
			if err != nil {
				missingImages = append(missingImages, imageRef)
				continue
			}
			// If the app image referenced with the tag is found among the installed images then continue
			// checking remaining app images
			taggedRef := s.Locator + ":" + (s.Digest().Encoded())[:7]
			if _, ok := installedImagesInfo.InstalledImageRefs[taggedRef]; ok {
				continue
			}
			// Check so-called familiar names
			anyRef, err := docker.ParseAnyReference(imageRef)
			if err != nil {
				missingImages = append(missingImages, imageRef)
				continue
			}
			familiarRef := docker.FamiliarString(anyRef)
			if _, ok := installedImagesInfo.InstalledImageRefs[familiarRef]; ok {
				continue
			}
			// TODO:
			//named, err := docker.ParseDockerRef(taggedRef)
			//if named.
			//
			//familiarName := docker.FamiliarName(named)
			//if _, ok := installedImagesInfo.InstalledImageRefs[familiarRef]; ok {
			//	continue
			//}
			missingImages = append(missingImages, imageRef)
		}
	}

	if len(missingImages) > 0 {
		return &ErrImageInstall{MissingImages: missingImages}
	}
	return nil
}

func GetInstalledImages(ctx context.Context, cfg *Config) (*InstalledImagesInfo, error) {
	// Image ID to image summary map (Image ID is the docker's internal ID for the image, not the digest URI of the image)
	installedImages := map[string]image.Summary{}
	// Image refs (both digest and tag) to image ID map
	installedImageRefs := map[string]string{}
	cli, err := GetDockerClient(cfg.DockerHost)
	if err != nil {
		return nil, err
	}
	// curl --unix-socket /var/run/docker.sock http://localhost/images/json?all=1
	images, err := cli.ImageList(ctx, dockertypes.ImageListOptions{All: true})
	if err != nil {
		return nil, err
	}
	for _, imageSummary := range images {
		installedImages[imageSummary.ID] = imageSummary
		for _, d := range imageSummary.RepoDigests {
			installedImageRefs[d] = imageSummary.ID
		}
		for _, t := range imageSummary.RepoTags {
			installedImageRefs[t] = imageSummary.ID
		}
	}
	return &InstalledImagesInfo{
		InstalledImages:    installedImages,
		InstalledImageRefs: installedImageRefs,
	}, nil
}

func checkAppsRunningStatus(ctx context.Context, cfg *Config) ([]*Service, error) {
	var services []*Service
	cli, err := GetDockerClient(cfg.DockerHost)
	if err != nil {
		return nil, err
	}
	// curl --unix-socket /var/run/docker.sock http://localhost/containers/json?all=1
	ctrs, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	for _, ctr := range ctrs {
		services = append(services, &Service{
			Name:   ctr.Labels[ServiceLabel],
			Image:  ctr.Image,
			Hash:   ctr.Labels[AppServiceHashLabelKey],
			CtrID:  ctr.ID,
			State:  ctr.State,
			Status: ctr.Status,
			// TODO: check health if needed
			//Health: health,
		})
	}
	return services, nil
}
