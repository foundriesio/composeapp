package compose

import (
	"context"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/docker/docker/pkg/archive"
	"github.com/foundriesio/composeapp/internal/progress"
	"os"
	"path"
)

type (
	AppInstallState string

	InstallProgress struct {
		AppInstallState AppInstallState
		AppID           string
		ImageLoadState  ImageLoadState
		ImageID         string
		ID              string
		Current         int64
		Total           int64
	}

	InstallProgressFunc func(*InstallProgress)

	InstallOptions struct {
		ProgressReporter progress.Reporter[InstallProgress]
		LoadedImages     map[string]struct{}
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
func WithLoadedImages(li map[string]struct{}) InstallOption {
	return func(o *InstallOptions) {
		o.LoadedImages = li
	}
}

func Install(ctx context.Context,
	cfg *Config,
	appURI string,
	options ...InstallOption) error {
	opts := InstallOptions{}
	for _, o := range options {
		o(&opts)
	}

	cs, err := cfg.AppStoreFactory()
	if err != nil {
		return fmt.Errorf("failed to create app store instance: %w", err)
	}
	app, err := cfg.AppLoader.LoadAppTree(ctx, cs, platforms.OnlyStrict(cfg.Platform), appURI)
	if err != nil {
		return fmt.Errorf("failed to load app %s: %w", app, err)
	}

	if opts.ProgressReporter != nil {
		// TODO: Implement progress reporting for app compose installation
		opts.ProgressReporter.Update(InstallProgress{
			AppInstallState: AppInstallStateComposeInstalling,
			AppID:           app.Ref().String(),
		})
	}

	cli, err := GetDockerClient(cfg.DockerHost)
	if err != nil {
		return err
	}

	var loadImageOptions []LoadImageOption
	var withProgressOpt LoadImageOption
	if opts.ProgressReporter != nil || opts.LoadedImages != nil {
		withProgressOpt = WithProgressReporting(func(ilp *LoadImageProgress) {
			if opts.LoadedImages != nil {
				if ilp.State == ImageLoadStateImageLoaded {
					opts.LoadedImages[ilp.ImageID] = struct{}{}
				}
			}
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
		})
		loadImageOptions = append(loadImageOptions, withProgressOpt)
	}

	loadImageOptionsRequiringPatch := append(loadImageOptions, WithRefWithDigest(), WithBlobReadingFromStore())
	// Try to load app images with reading blobs directly from the store and specifying image digests (URI with hashes)
	err = LoadImages(ctx, cli, app, cfg.GetBlobsRoot(), loadImageOptionsRequiringPatch...)
	if err != nil {
		// Retry loading images without reading blobs directly from the store and specifying the digest,
		// in case if the docker daemon is not patch with the Foundries patches
		err = LoadImages(ctx, cli, app, cfg.GetBlobsRoot(), loadImageOptions...)
	}
	if err != nil {
		return fmt.Errorf("failed to load images for app %s: %w", app.Ref().String(), err)
	}

	if err := InstallCompose(ctx, app, cs, cfg.ComposeRoot); err != nil {
		return err
	}
	if opts.ProgressReporter != nil {
		// TODO: Implement progress reporting for app compose installation checking
		opts.ProgressReporter.Update(InstallProgress{
			AppInstallState: AppInstallStateComposeChecking,
			AppID:           app.Ref().String(),
		})
	}
	if checkErrMap, err := app.CheckComposeInstallation(ctx, cs, cfg.GetAppComposeDir(app.Name())); err != nil {
		return err
	} else if len(checkErrMap) > 0 {
		// TODO: remove prints and return error map
		fmt.Println("the following app bundle files are not correctly installed")
		for filePath, fileErr := range checkErrMap {
			fmt.Printf("\t%s\t%s\n", filePath, fileErr)
		}
		return fmt.Errorf("app bundle is not installed completely")
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
