package compose

import (
	"context"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/docker/docker/client"
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
	if opts.ProgressReporter != nil {
		// Every state change has been handled when Install returns; after a cancellation the
		// queue is not waited for. Evaluated at return time, hence the closure.
		defer func() { opts.ProgressReporter.Stop(ctx.Err() == nil) }()
	}

	unlock, err := LockInstall(cfg.StoreRoot)
	if err != nil {
		return err
	}
	defer unlock()

	cs, err := cfg.AppStoreFactory()
	if err != nil {
		return fmt.Errorf("failed to create app store instance: %w", err)
	}
	app, err := cfg.AppLoader.LoadAppTree(ctx, cs, platforms.OnlyStrict(cfg.Platform), appURI)
	if err != nil {
		return fmt.Errorf("failed to load app %s: %w", appURI, err)
	}

	// TODO: Implement progress reporting for app compose installation
	reportInstallState(ctx, &opts, app, AppInstallStateComposeInstalling)

	if err := installImages(ctx, cfg, app, &opts); err != nil {
		return fmt.Errorf("failed to load images for app %s: %w", app.Ref().String(), err)
	}

	if err := InstallCompose(ctx, app, cs, cfg.ComposeRoot); err != nil {
		return err
	}
	// TODO: Implement progress reporting for app compose installation checking
	reportInstallState(ctx, &opts, app, AppInstallStateComposeChecking)
	return checkComposeInstallation(ctx, app, cs, cfg)
}

func reportInstallState(ctx context.Context, opts *InstallOptions, app App, state AppInstallState) {
	if opts.ProgressReporter == nil {
		return
	}
	opts.ProgressReporter.Send(ctx, InstallProgress{
		AppInstallState: state,
		AppID:           app.Ref().String(),
	})
}

// imageProgressCallback forwards the image load progress to the install progress reporter and
// records the loaded images; it is nil when neither is wanted. A state change of a layer or an
// image is sent so that it is never lost; the samples that follow it (bytes of a layer being
// loaded, time of a layer being extracted) may be dropped when the reporter is behind.
func imageProgressCallback(ctx context.Context, opts *InstallOptions, app App) ProgressCallbackFn {
	if opts.ProgressReporter == nil && opts.LoadedImages == nil {
		return nil
	}
	var last LoadImageProgress
	return func(ilp *LoadImageProgress) {
		if opts.LoadedImages != nil && ilp.State == ImageLoadStateImageLoaded {
			opts.LoadedImages[ilp.ImageID] = struct{}{}
		}
		if opts.ProgressReporter == nil {
			return
		}
		p := InstallProgress{
			AppInstallState: AppInstallStateImagesLoading,
			AppID:           app.Ref().String(),
			ImageLoadState:  ilp.State,
			ImageID:         ilp.ImageID,
			ID:              ilp.ID,
			Current:         ilp.Current,
			Total:           ilp.Total,
		}
		if isProgressSample(ilp, &last) {
			opts.ProgressReporter.Update(p)
		} else {
			opts.ProgressReporter.Send(ctx, p)
		}
		last = *ilp
	}
}

// isProgressSample reports whether an update only refines the state the previous update already
// announced for the same layer. The last sample of a layer being loaded, the one that completes
// it, counts as a state change.
func isProgressSample(p, last *LoadImageProgress) bool {
	if p.State != ImageLoadStateLayerLoading && p.State != ImageLoadStateLayerSyncing {
		return false
	}
	if p.State != last.State || p.ID != last.ID || p.ImageID != last.ImageID {
		return false
	}
	return p.State != ImageLoadStateLayerLoading || p.Current < p.Total
}

// installImages puts the app images into dockerd: straight into its containerd on a daemon that
// uses the containerd image store, with `docker load` otherwise.
func installImages(ctx context.Context, cfg *Config, app App, opts *InstallOptions) error {
	cli, err := GetDockerClient(cfg.DockerHost)
	if err != nil {
		return err
	}
	defer cli.Close()
	report := imageProgressCallback(ctx, opts, app)
	ctrd, err := connectCtrdIfInstallTarget(ctx, cli)
	if err != nil {
		return err
	}
	if ctrd != nil {
		defer ctrd.Close()
		return installImagesCtrd(ctx, ctrd, app, cfg, report)
	}
	return loadImagesWithDocker(ctx, cli, app, cfg.GetBlobsRoot(), report)
}

// loadImagesWithDocker tries first to load the images with reading blobs directly from the store
// and with digest references, which needs the Foundries patches to dockerd, then the plain way.
func loadImagesWithDocker(ctx context.Context, cli *client.Client, app App, blobsRoot string, report ProgressCallbackFn) error {
	var loadOpts []LoadImageOption
	if report != nil {
		loadOpts = append(loadOpts, WithProgressReporting(report))
	}
	patchedOpts := append([]LoadImageOption{WithRefWithDigest(), WithBlobReadingFromStore()}, loadOpts...)
	err := LoadImages(ctx, cli, app, blobsRoot, patchedOpts...)
	if err != nil {
		err = LoadImages(ctx, cli, app, blobsRoot, loadOpts...)
	}
	return err
}

func checkComposeInstallation(ctx context.Context, app App, cs AppStore, cfg *Config) error {
	checkErrMap, err := app.CheckComposeInstallation(ctx, cs, cfg.GetAppComposeDir(app.Name()))
	if err != nil {
		return err
	}
	if len(checkErrMap) == 0 {
		return nil
	}
	// TODO: remove prints and return error map
	fmt.Println("the following app bundle files are not correctly installed")
	for filePath, fileErr := range checkErrMap {
		fmt.Printf("\t%s\t%s\n", filePath, fileErr)
	}
	return fmt.Errorf("app bundle is not installed completely")
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
