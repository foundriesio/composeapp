package compose

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/reference"
	ref1 "github.com/containerd/containerd/reference/docker"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/pkg/archive"
	"github.com/foundriesio/composeapp/internal/progress"
	"github.com/foundriesio/composeapp/pkg/docker"
	"os"
	"path"
)

type (
	AppInstallState string

	InstallProgress struct {
		AppInstallState AppInstallState
		AppID           string
		ImageLoadState  docker.ImageLoadState
		ImageID         string
		ID              string
		Current         int64
		Total           int64
	}

	InstallProgressFunc func(*InstallProgress)

	InstallOptions struct {
		ProgressReporter progress.Reporter[InstallProgress]
	}

	InstallOption func(*InstallOptions)

	AppInstallCheckResult struct {
		AppName       string        `json:"app_name"`
		MissingImages []string      `json:"missing_images"`
		BundleErrors  AppBundleErrs `json:"bundle_errors"`
	}

	InstallCheckResult map[string]*AppInstallCheckResult
)

const (
	AppInstallStateComposeInstalling AppInstallState = "app:install:compose:install"
	AppInstallStateComposeChecking   AppInstallState = "app:install:compose:check"
	AppInstallStateImagesLoading     AppInstallState = "app:install:images:loading"
)

func WithInstallProgress(pf InstallProgressFunc) InstallOption {
	return func(o *InstallOptions) {
		o.ProgressReporter = progress.NewReporter[InstallProgress](2)
		o.ProgressReporter.Start(pf)
	}
}

func Install(ctx context.Context,
	app App,
	provider BlobProvider,
	blobsRoot string,
	composeRoot string,
	dockerHost string,
	options ...InstallOption) error {
	opts := InstallOptions{}
	for _, o := range options {
		o(&opts)
	}

	if opts.ProgressReporter != nil {
		// TODO: Implement progress reporting for app compose installation
		opts.ProgressReporter.Update(InstallProgress{
			AppInstallState: AppInstallStateComposeInstalling,
			AppID:           app.Ref().String(),
		})
	}

	if err := InstallCompose(ctx, app, provider, composeRoot); err != nil {
		return err
	}
	if opts.ProgressReporter != nil {
		// TODO: Implement progress reporting for app compose installation checking
		opts.ProgressReporter.Update(InstallProgress{
			AppInstallState: AppInstallStateComposeChecking,
			AppID:           app.Ref().String(),
		})
	}
	if checkErrMap, err := app.CheckComposeInstallation(ctx, provider, path.Join(composeRoot, app.Name())); err != nil {
		return err
	} else if len(checkErrMap) > 0 {
		// TODO: remove prints and return error map
		fmt.Println("the following app bundle files are not correctly installed")
		for filePath, fileErr := range checkErrMap {
			fmt.Printf("\t%s\t%s\n", filePath, fileErr)
		}
		return fmt.Errorf("app bundle is not installed completely")
	}

	cli, err := GetDockerClient(dockerHost)
	if err != nil {
		return err
	}

	appImageURIs := make(docker.ImageDescriptions)
	err = app.GetComposeRoot().Walk(func(node *TreeNode, depth int) error {
		if node.Type == BlobTypeImageManifest {
			nodeURI := node.Descriptor.URLs[0]
			appImageURIs[nodeURI] = node.Descriptor.URLs
		}
		return nil
	})
	if err != nil {
		return err
	}

	err = docker.LoadImages(ctx, cli, appImageURIs, blobsRoot, docker.WithRefWithDigest(),
		docker.WithBlobReadingFromStore(),
		docker.WithProgressReporting(func(ilp *docker.LoadImageProgress) {
			if opts.ProgressReporter != nil {
				opts.ProgressReporter.Update(InstallProgress{
					AppInstallState: AppInstallStateImagesLoading,
					AppID:           app.Ref().String(),
					ImageLoadState:  ilp.State,
					ImageID:         ilp.ImageID,
					ID:              ilp.ID,
					Current:         ilp.Current,
					Total:           ilp.Total,
				})
			}
		}))
	if err != nil {
		// Try to load images without pinning to digest and reading directly from the store
		err = docker.LoadImages(ctx, cli, appImageURIs, blobsRoot, docker.WithProgressReporting(func(ilp *docker.LoadImageProgress) {
			if opts.ProgressReporter != nil {
				opts.ProgressReporter.Update(InstallProgress{
					AppInstallState: AppInstallStateImagesLoading,
					AppID:           app.Ref().String(),
					ImageLoadState:  ilp.State,
					ImageID:         ilp.ImageID,
					ID:              ilp.ID,
					Current:         ilp.Current,
					Total:           ilp.Total,
				})
			}
		}))
	}
	return err
}

func InstallCompose(ctx context.Context, app App, provider BlobProvider, composeRoot string) error {
	appInstallDir := path.Join(composeRoot, app.Name())
	if err := os.MkdirAll(appInstallDir, 0755); err != nil {
		return err
	}
	tarOptions := archive.TarOptions{
		NoLchown: true,
	}
	composeDesc := app.GetComposeRoot().Descriptor

	rc, err := provider.GetReadCloser(WithBlobType(WithAppRef(ctx, app.Ref()), BlobTypeAppBundle),
		WithExpectedDigest(composeDesc.Digest), WithExpectedSize(composeDesc.Size))
	if err != nil {
		return err
	}
	defer rc.Close()

	if err := archive.Untar(rc, path.Join(composeRoot, app.Name()), &tarOptions); err != nil {
		return err
	}
	return nil
}

func CheckInstallation(
	ctx context.Context,
	cfg *Config,
	appRefs []string,
	blobProvider BlobProvider) (InstallCheckResult, error) {
	cli, err := GetDockerClient(cfg.DockerHost)
	if err != nil {
		return nil, err
	}
	images, err := cli.ImageList(ctx, dockertypes.ImageListOptions{All: true})
	if err != nil {
		return nil, err
	}
	installedImages := map[string]bool{}
	for _, i := range images {
		if len(i.RepoDigests) > 0 {
			installedImages[i.RepoDigests[0]] = true
		}
		if len(i.RepoTags) > 0 {
			// unpatch docker won't store the digest URI of loaded image
			installedImages[i.RepoTags[0]] = true
		}
	}

	checkResult := InstallCheckResult{}
	for _, appRef := range appRefs {
		app, err := cfg.AppLoader.LoadAppTree(ctx, blobProvider, platforms.OnlyStrict(cfg.Platform), appRef)
		if err != nil {
			return nil, err
		}
		var missingImages []string
		appComposeRoot := app.GetComposeRoot()
		for _, imageNode := range appComposeRoot.Children {
			imageUri := imageNode.Ref()
			if !installedImages[imageUri] {
				if s, err := reference.Parse(imageUri); err == nil {
					taggedUri := s.Locator + ":" + (s.Digest().Encoded())[:7]
					if !installedImages[taggedUri] {
						// Check familiar name
						if anyRef, err := ref1.ParseAnyReference(imageUri); err == nil {
							familiarRef := ref1.FamiliarString(anyRef)
							if !installedImages[familiarRef] {
								missingImages = append(missingImages, imageUri)
							}
						}
					}
				}
			}
		}
		errMap, err := app.CheckComposeInstallation(ctx, blobProvider, path.Join(cfg.ComposeRoot, app.Name()))
		if err != nil {
			return nil, err
		}
		checkResult[appRef] = &AppInstallCheckResult{
			AppName:       app.Name(),
			MissingImages: missingImages,
			BundleErrors:  errMap,
		}
	}
	return checkResult, nil
}
