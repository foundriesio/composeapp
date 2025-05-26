package compose

import (
	"context"
	"errors"
	"fmt"
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
		FetchStatus
		InstallStatus
		RunningStatus
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
		AppsInstallStatus   map[digest.Digest]InstallReport
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
)

func NewAppsStatus() AppsStatus {
	return AppsStatus{
		Apps: []App{},
		FetchStatus: FetchStatus{
			BlobsStatus:  make(map[digest.Digest]FetchReport),
			MissingBlobs: make(map[digest.Digest]*BlobInfo),
		},
		InstallStatus: InstallStatus{
			AppsInstallStatus:   make(map[digest.Digest]InstallReport),
			NotInstalledImages:  make(map[string]interface{}),
			NotInstalledCompose: make(map[digest.Digest]interface{}),
		},
		RunningStatus: RunningStatus{
			AppsRunningStatus: make(map[digest.Digest]RunningReport),
			NotRunningApps:    make(map[digest.Digest]interface{}),
		},
	}
}

func (e *ErrComposeInstall) Error() string {
	return fmt.Sprintf("app compose installation errors: %d", len(e.Errs))
}
func (e *ErrImageInstall) Error() string {
	return fmt.Sprintf("app image installation errors: %d", len(e.MissingImages))
}

func (s *AppsStatus) AreRunning() bool {
	return len(s.RunningStatus.NotRunningApps) == 0
}

func (s *AppsStatus) AreInstalled() bool {
	return len(s.InstallStatus.NotInstalledImages) == 0 && len(s.InstallStatus.NotInstalledCompose) == 0
}

func (s *AppsStatus) AreFetched() bool {
	return len(s.FetchStatus.MissingBlobs) == 0
}

func CheckAppsStatus(
	ctx context.Context,
	cfg *Config,
	appRefs []string) (*AppsStatus, error) {
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
	if apps, err = loadAppTrees(ctx, cfg, appStore, refs, true); err != nil {
		return nil, fmt.Errorf("failed to load app trees: %w", err)
	}

	installedImages, err := GetInstalledImages(ctx, cfg)
	if err != nil {
		return nil, err
	}

	foundAppServices, err := GetAppServicesStatus(ctx, cfg)
	if err != nil {
		return nil, err
	}

	var fetchStatus *FetchStatus
	if fetchStatus, err = CheckAppsFetchStatus(ctx, cfg, appStore, apps); err != nil {
		return nil, fmt.Errorf("failed to check apps fetch status: %w", err)
	}

	appsStatus := NewAppsStatus()
	appsStatus.Apps = apps
	appsStatus.FetchStatus = *fetchStatus

	for _, app := range appsStatus.Apps {
		// Check App installation
		installReport := InstallReport{
			Images:       make(map[digest.Digest]bool),
			BundleErrors: make(AppBundleErrs),
		}
		// Check app compose installation and app images installation in the docker store
		appBundleErrs, checkComposeErr := app.CheckComposeInstallation(ctx, appStore, path.Join(cfg.ComposeRoot, app.Name()))
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

		var running = true
		var appServices []*Service
		appComposeRoot := app.GetComposeRoot()
		for _, imageNode := range appComposeRoot.Children {
			imageUri := imageNode.Ref()

			installed, err := checkImageInstallation(installedImages, imageUri)
			if err != nil {
				return nil, err
			}
			installReport.Images[imageNode.Descriptor.Digest] = installed
			if !installed {
				appsStatus.InstallStatus.NotInstalledImages[imageUri] = struct{}{}
			}

			// check running status
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

		appsStatus.InstallStatus.AppsInstallStatus[app.Ref().Digest] = installReport
		if len(installReport.BundleErrors) > 0 {
			appsStatus.InstallStatus.NotInstalledCompose[app.Ref().Digest] = struct{}{}
		}

		appsStatus.RunningStatus.AppsRunningStatus[app.Ref().Digest] = RunningReport{
			Services: appServices,
			Health:   "todo",
		}
		if !running {
			appsStatus.RunningStatus.NotRunningApps[app.Ref().Digest] = struct{}{}
		}
	}

	return &appsStatus, nil
}

func CheckAppsFetchStatus(
	ctx context.Context,
	cfg *Config,
	blobProvider BlobProvider,
	apps []App) (*FetchStatus, error) {
	fetchStatus := &FetchStatus{
		MissingBlobs: map[digest.Digest]*BlobInfo{},
		BlobsStatus:  map[digest.Digest]FetchReport{},
	}
	for _, app := range apps {
		fetchReport := FetchReport{BlobsStatus: map[digest.Digest]*BlobInfo{}}
		err := app.Tree().Walk(func(node *TreeNode, depth int) error {
			bi, checkBlobErr := checkNodeBlob(ctx, cfg, app, node, blobProvider)
			if checkBlobErr != nil {
				return checkBlobErr
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

func newRemoteBlobProvider(c *Config) BlobProvider {
	authorizer := NewRegistryAuthorizer(c.DockerCfg, c.ConnectTimeout)
	resolver := NewResolver(authorizer, c.ConnectTimeout)
	return NewRemoteBlobProvider(resolver)
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

func checkNodeBlob(ctx context.Context, cfg *Config, app App, node *TreeNode, bp BlobProvider) (*BlobInfo, error) {
	var quick bool
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
			app, err = cfg.AppLoader.LoadAppTree(ctx, newRemoteBlobProvider(cfg),
				platforms.OnlyStrict(cfg.Platform), appRef)
		}
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, nil
}
