package compose

import (
	"context"
	"errors"
	"fmt"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/reference/docker"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/opencontainers/go-digest"
	"path"
)

const (
	AppServiceHashLabelKey = "io.compose-spec.config-hash"
	ServiceLabel           = "com.docker.compose.service"
)

type (
	AppsStatus struct {
		Apps []App
		*FetchStatus
		*InstallStatus
		*RunningStatus
	}
	FetchReport struct {
		BlobsStatus map[digest.Digest]*BlobInfo
	}
	FetchStatus struct {
		BlobsStatus  map[digest.Digest]FetchReport
		MissingBlobs map[digest.Digest]*BlobInfo
	}

	InstallReport struct {
		Images       map[digest.Digest]bool
		BundleErrors AppBundleErrs
	}
	InstallStatus struct {
		AppsInstallStatus   map[digest.Digest]*InstallReport
		NotInstalledImages  map[string]interface{}
		NotInstalledCompose map[digest.Digest]interface{}
	}

	RunningReport struct {
		Services []*Service
		Health   string
	}
	RunningStatus struct {
		AppsRunningStatus map[digest.Digest]RunningReport
		NotRunningApps    map[digest.Digest]interface{}
	}

	AppInstallCheckResult struct {
		AppName       string        `json:"app_name"`
		MissingImages []string      `json:"missing_images"`
		BundleErrors  AppBundleErrs `json:"bundle_errors"`
	}

	InstallCheckResult map[string]*AppInstallCheckResult

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
	Services []*Service

	ErrComposeInstall struct {
		Errs AppBundleErrs
	}
	ErrImageInstall struct {
		MissingImages []string
	}

	CheckAppsStatusOptions struct {
		CheckInstallation   bool
		CheckRunning        bool
		QuickCheckFetch     bool
		AppTreeBlobProvider BlobProvider
	}
	CheckAppsStatusOption func(*CheckAppsStatusOptions)
)

func WithCheckInstallation(check bool) CheckAppsStatusOption {
	return func(opts *CheckAppsStatusOptions) {
		opts.CheckInstallation = check
	}
}
func WithCheckRunning(check bool) CheckAppsStatusOption {
	return func(opts *CheckAppsStatusOptions) {
		opts.CheckRunning = check
	}
}
func WithQuickCheckFetch(quickCheck bool) CheckAppsStatusOption {
	return func(opts *CheckAppsStatusOptions) {
		opts.QuickCheckFetch = quickCheck
	}
}
func WithAppTreeBlobProvider(bp BlobProvider) CheckAppsStatusOption {
	return func(opts *CheckAppsStatusOptions) {
		opts.AppTreeBlobProvider = bp
	}
}

func (e *ErrComposeInstall) Error() string {
	return fmt.Sprintf("app compose installation errors: %d", len(e.Errs))
}
func (e *ErrImageInstall) Error() string {
	return fmt.Sprintf("app image installation errors: %d", len(e.MissingImages))
}

func (s *AppsStatus) AreRunning() bool {
	return len(s.NotRunningApps) == 0
}

func (s *AppsStatus) AreInstalled() bool {
	return len(s.NotInstalledImages) == 0 && len(s.NotInstalledCompose) == 0
}

func (s *AppsStatus) AreFetched() bool {
	return len(s.MissingBlobs) == 0
}

func CheckAppsStatus(
	ctx context.Context,
	cfg *Config,
	appRefs []string,
	options ...CheckAppsStatusOption) (*AppsStatus, error) {
	opts := &CheckAppsStatusOptions{
		CheckInstallation: true,
		CheckRunning:      true,
		QuickCheckFetch:   false,
	}
	for _, opt := range options {
		opt(opts)
	}

	var err error
	var appStore AppStore

	if appStore, err = cfg.AppStoreFactory(); err != nil {
		return nil, err
	}

	refs := appRefs
	if len(refs) == 0 {
		if refs, err = getStoreAppRefs(ctx, appStore); err != nil {
			return nil, err
		}
	}

	var apps []App
	var appTreeProvider BlobProvider
	var fallbackToRemoteProvider bool
	// If the app tree blob provider is specified then load app tree from it and do not fallback
	// to loading it from the remote provider.
	// Otherwise, use the local app store/storage as the app tree provider and fallback to the remote provider
	// if the app is not found in the local store or not all blobs of app trees are found in it.
	if opts.AppTreeBlobProvider != nil {
		appTreeProvider = opts.AppTreeBlobProvider
		fallbackToRemoteProvider = false
	} else {
		appTreeProvider = appStore
		fallbackToRemoteProvider = true
	}
	if apps, err = loadAppTrees(ctx, cfg, appTreeProvider, refs, fallbackToRemoteProvider); err != nil {
		return nil, fmt.Errorf("failed to load app trees: %w", err)
	}

	var fetchStatus *FetchStatus
	if fetchStatus, err = CheckAppsFetchStatus(ctx, cfg, appStore, apps, opts.QuickCheckFetch); err != nil {
		return nil, fmt.Errorf("failed to check apps fetch status: %w", err)
	}

	var installStatus *InstallStatus
	if opts.CheckInstallation {
		if installStatus, err = CheckAppsInstallStatus(ctx, cfg, appStore, apps); err != nil {
			return nil, fmt.Errorf("failed to check apps install status: %w", err)
		}
	}

	var runningStatus *RunningStatus
	if opts.CheckRunning {
		if runningStatus, err = CheckAppsRunningStatus(ctx, cfg, apps); err != nil {
			return nil, fmt.Errorf("failed to check apps running status: %w", err)
		}
	}

	return &AppsStatus{
		Apps:          apps,
		FetchStatus:   fetchStatus,
		InstallStatus: installStatus,
		RunningStatus: runningStatus,
	}, nil
}

func CheckAppsFetchStatus(
	ctx context.Context,
	cfg *Config,
	blobProvider BlobProvider,
	apps []App,
	quick bool) (*FetchStatus, error) {
	fetchStatus := &FetchStatus{
		MissingBlobs: map[digest.Digest]*BlobInfo{},
		BlobsStatus:  map[digest.Digest]FetchReport{},
	}
	ls, err := local.NewStore(cfg.StoreRoot)
	if err != nil {
		return nil, err
	}
	for _, app := range apps {
		fetchReport := FetchReport{BlobsStatus: map[digest.Digest]*BlobInfo{}}
		err := app.Tree().Walk(func(node *TreeNode, depth int) error {
			bi, checkBlobErr := checkNodeBlob(ctx, cfg, app, node, blobProvider, quick)
			if checkBlobErr != nil {
				return checkBlobErr
			}
			if bi.State == BlobMissing {
				if fetchStatus, err := ls.Status(ctx, node.Ref()); err == nil {
					bi.State = BlobFetching
					bi.BytesFetched = fetchStatus.Offset
				}
			}
			fetchReport.BlobsStatus[node.Descriptor.Digest] = bi
			if bi.State != BlobOk {
				fetchStatus.MissingBlobs[node.Descriptor.Digest] = bi
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		fetchStatus.BlobsStatus[app.Ref().Digest] = fetchReport
	}
	return fetchStatus, nil
}

func CheckAppsInstallStatus(
	ctx context.Context,
	cfg *Config,
	blobProvider BlobProvider,
	apps []App) (*InstallStatus, error) {

	installStatus := &InstallStatus{
		AppsInstallStatus:   map[digest.Digest]*InstallReport{},
		NotInstalledImages:  map[string]interface{}{},
		NotInstalledCompose: map[digest.Digest]interface{}{},
	}

	installedImages, err := GetInstalledImages(ctx, cfg)
	if err != nil {
		return nil, err
	}

	for _, app := range apps {
		// Check App installation
		installReport := &InstallReport{
			Images:       map[digest.Digest]bool{},
			BundleErrors: AppBundleErrs{},
		}
		// Check app compose installation and app images installation in the docker store
		appBundleErrs, checkComposeErr := app.CheckComposeInstallation(ctx, blobProvider, path.Join(cfg.ComposeRoot, app.Name()))
		if checkComposeErr != nil {
			if errors.Is(checkComposeErr, ErrAppIndexNotFound) {
				if appBundleErrs == nil {
					appBundleErrs = make(AppBundleErrs)
				}
				appBundleErrs[app.Ref().String()] = checkComposeErr.Error()
			} else {
				return nil, checkComposeErr
			}
		}
		installReport.BundleErrors = appBundleErrs

		appComposeRoot := app.GetComposeRoot()
		for _, imageNode := range appComposeRoot.Children {
			imageUri := imageNode.Ref()
			installed, err := checkImageInstallation(installedImages, imageUri)
			if err != nil {
				return nil, err
			}
			installReport.Images[imageNode.Descriptor.Digest] = installed
			if !installed {
				installStatus.NotInstalledImages[imageUri] = struct{}{}
			}
		}

		installStatus.AppsInstallStatus[app.Ref().Digest] = installReport
		if len(installReport.BundleErrors) > 0 {
			installStatus.NotInstalledCompose[app.Ref().Digest] = struct{}{}
		}
	}

	return installStatus, nil
}

func CheckAppsRunningStatus(
	ctx context.Context,
	cfg *Config,
	apps []App) (*RunningStatus, error) {
	runningStatus := &RunningStatus{
		AppsRunningStatus: map[digest.Digest]RunningReport{},
		NotRunningApps:    map[digest.Digest]interface{}{},
	}

	foundAppServices, err := GetAppServicesStatus(ctx, cfg)
	if err != nil {
		return nil, err
	}

	for _, app := range apps {
		var running = true
		var appServices []*Service
		appComposeRoot := app.GetComposeRoot()
		for _, imageNode := range appComposeRoot.Children {
			if srv := foundAppServices.find(imageNode); srv != nil {
				appServices = append(appServices, srv)
				if srv.State != "running" {
					running = false
				}
			} else {
				appServices = append(appServices, &Service{
					State: "not created",
				})
				running = false
			}
		}
		runningStatus.AppsRunningStatus[app.Ref().Digest] = RunningReport{
			Services: appServices,
			Health:   "todo",
		}
		if !running {
			runningStatus.NotRunningApps[app.Ref().Digest] = struct{}{}
		}
	}
	return runningStatus, nil
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

func GetAppServicesStatus(ctx context.Context, cfg *Config) (Services, error) {
	var services Services
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
		if _, ok := ctr.Labels[AppServiceHashLabelKey]; !ok {
			// Skip containers that are not related to Compose Apps
			continue
		}
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

func (s Services) find(imageNode *TreeNode) (foundService *Service) {
	imageRef := imageNode.Ref()
	serviceHash := imageNode.GetServiceHash()

	for _, srv := range s {
		if srv.Image == imageRef && srv.Hash == serviceHash {
			foundService = srv
			break
		}
	}
	return
}

func checkImageInstallation(installedImages *InstalledImagesInfo, uri string) (bool, error) {
	if _, ok := installedImages.InstalledImageRefs[uri]; ok {
		return true, nil
	}
	ref, err := reference.Parse(uri)
	if err != nil {
		return false, err
	}

	// Check if image with "short hash" tag is installed (if dockerd is not patched)
	taggedUri := ref.Locator + ":" + (ref.Digest().Encoded())[:7]
	if _, ok := installedImages.InstalledImageRefs[taggedUri]; ok {
		return true, nil
	}

	// Check familiar name for both, the digest and tag references/URIs
	for _, u := range []string{uri, taggedUri} {
		anyRef, err := docker.ParseAnyReference(u)
		if err != nil {
			return false, err
		}
		familiarRef := docker.FamiliarString(anyRef)
		if _, ok := installedImages.InstalledImageRefs[familiarRef]; ok {
			return true, nil
		}
	}

	return false, nil
}

func checkNodeBlob(ctx context.Context, cfg *Config, app App, node *TreeNode, bp BlobProvider, quick bool) (*BlobInfo, error) {
	checkOpts := []SecureReadOptions{WithExpectedSize(node.Descriptor.Size)}
	if !quick {
		checkOpts = append(checkOpts, WithExpectedDigest(node.Descriptor.Digest))
	}
	if node.HasRef() {
		checkOpts = append(checkOpts, WithRef(node.Ref()))
	}
	bs, stateCheckErr := CheckBlob(WithAppRef(WithBlobType(ctx, node.Type), app.Ref()), bp, node.Descriptor.Digest, checkOpts...)
	if stateCheckErr != nil {
		return nil, stateCheckErr
	}

	return &BlobInfo{
		Descriptor:  node.Descriptor,
		State:       bs,
		Type:        node.Type,
		StoreSize:   AlignToBlockSize(node.Descriptor.Size, cfg.BlockSize),
		RuntimeSize: app.GetBlobRuntimeSize(node.Descriptor, cfg.Platform.Architecture, cfg.BlockSize),
	}, nil
}

func getStoreAppRefs(ctx context.Context, store AppStore) ([]string, error) {
	appRefs, err := store.ListApps(ctx)
	if err != nil {
		return nil, err
	}

	var stringRefs []string
	for _, appRef := range appRefs {
		stringRefs = append(stringRefs, appRef.String())
	}
	return stringRefs, nil
}

func loadAppTrees(ctx context.Context,
	cfg *Config,
	blobProvider BlobProvider,
	appRefs []string,
	fallbackLoadingFromRemote bool) ([]App, error) {
	var apps []App
	for _, appRef := range appRefs {
		app, err := cfg.AppLoader.LoadAppTree(ctx, blobProvider, platforms.OnlyStrict(cfg.Platform), appRef)
		if fallbackLoadingFromRemote && errors.Is(err, ErrAppNotFound) {
			app, err = cfg.AppLoader.LoadAppTree(ctx, NewRemoteBlobProviderFromConfig(cfg),
				platforms.OnlyStrict(cfg.Platform), appRef)
		}
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, nil
}
