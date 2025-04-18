package compose

import (
	"context"
	"fmt"
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
		err = docker.LoadImages(ctx, cli, appImageURIs, blobsRoot)
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
