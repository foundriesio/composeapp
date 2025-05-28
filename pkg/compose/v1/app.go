package v1

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/compose-spec/compose-go/loader"
	composetypes "github.com/compose-spec/compose-go/types"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/platforms"
	"github.com/docker/docker/pkg/archive"
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"io"
	"os"
	"path"
	"strconv"
)

type (
	layersMeta struct {
		FsBlockSize int `json:"fs_block_size"`
		Layers      map[digest.Digest]struct {
			Size        int64 `json:"size"`
			Usage       int64 `json:"usage"`
			ArchiveSize int64 `json:"archive_size"`
		} `json:"layers"`
	}

	StoreType string

	appCtx struct {
		compose.AppRef
		manifest   ocispec.Manifest
		layersMeta map[string]layersMeta
		tree       *compose.AppTree
		storeType  StoreType
		nodeCount  int
	}

	appLoader struct{}

	fileInfo struct {
		name   string
		size   int64
		digest digest.Digest
	}
)

const (
	AppManifestMediaType   = "application/vnd.oci.image.manifest.v1+json"
	AppManifestMaxSize     = 50 * 1024
	AppComposeMaxSize      = 1024 * 1024
	AppBundleFileMaxSize   = 1024*1024*1024 - AppComposeMaxSize
	AppBundleMaxSize       = AppComposeMaxSize + AppBundleFileMaxSize
	AppLayerMediaType      = "application/octet-stream"
	AppLayersMetaVersion   = "v1"
	AppServiceHashLabelKey = "io.compose-spec.config-hash"

	AnnotationKeyAppBundleIndexDigest = "org.foundries.app.bundle.index.digest"
	AnnotationKeyAppBundleIndexSize   = "org.foundries.app.bundle.index.size"

	StoreTypeSkopeo     = "skopeo store"
	StoreTypeComposeCtl = "composectl store"
)

func (a *appCtx) Name() string {
	return a.AppRef.Name
}

func (a *appCtx) Tree() *compose.AppTree {
	return a.tree
}

func (a *appCtx) NodeCount() int {
	if a.nodeCount == 0 {
		a.nodeCount = (*compose.TreeNode)(a.tree).NodeCount()
	}
	return a.nodeCount
}

func (a *appCtx) Ref() *compose.AppRef {
	return &a.AppRef
}

func (a *appCtx) HasLayersMeta(arch string) bool {
	if a.layersMeta != nil {
		_, ok := a.layersMeta[arch]
		return ok
	}
	return false
}

func (a *appCtx) GetBlobRuntimeSize(desc *ocispec.Descriptor, arch string, blockSize int64) int64 {
	if images.IsLayerType(desc.MediaType) && a.HasLayersMeta(arch) {
		if i, ok := a.layersMeta[arch].Layers[desc.Digest]; ok {
			return i.Usage
		} else {
			// assume that average compression ratio is 5
			return desc.Size * 5
		}
	} else {
		return compose.AlignToBlockSize(desc.Size, blockSize)
	}
}

func NewAppLoader() compose.AppLoader {
	return &appLoader{}
}

func (l *appLoader) LoadAppTree(ctx context.Context, provider compose.BlobProvider, platform platforms.MatchComparer, ref string) (compose.App, error) {
	// root node
	app, rootDesc, err := ReadAppManifest(ctx, provider, ref)
	if err != nil {
		if errors.Is(err, compose.ErrAppNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to read app manifest: %s", err)
	}
	appTree := compose.AppTree{Descriptor: rootDesc, Type: compose.BlobTypeAppManifest}

	// depth 1, layers meta (optional)
	layersMetaDesc, _ := app.GetLayersMetadataDescriptor()
	if layersMetaDesc != nil {
		if b, readErr := compose.ReadBlob(ctx, provider, app.GetBlobRef(layersMetaDesc.Digest), layersMetaDesc.Digest, layersMetaDesc.Size); readErr == nil {
			if unmarshalErr := json.Unmarshal(b, &app.layersMeta); unmarshalErr == nil {
				appTree.Children = append(appTree.Children, &compose.TreeNode{
					Descriptor: layersMetaDesc,
					Type:       compose.BlobTypeAppLayersMeta,
				})
			} else {
				// TODO: log
				fmt.Printf("Failed to unmarshal app layers meta: %s\n", unmarshalErr.Error())
			}
		} else {
			_, isPathErr := readErr.(*os.PathError)
			if !errors.Is(readErr, errdefs.ErrNotFound) && !isPathErr {
				fmt.Printf("Failed to read app layers meta: %s\n", readErr.Error())
			}
			// TODO: log else (if not found)
		}
	}

	// depth 1, compose
	composeProject, composeDesc, err := readAndLoadComposeProject(ctx, provider, app)
	if err != nil {
		return nil, fmt.Errorf("failed to read app compose project: %s", err)
	}
	composeTree := compose.TreeNode{
		Descriptor: composeDesc,
		Type:       compose.BlobTypeAppBundle,
	}

	// depth 1, app bundle index/hashes if present and app is pulled by `composectl`
	if app.storeType == StoreTypeComposeCtl {
		if indexNode := getAppIndexNodeIfPresent(app.Ref(), composeDesc); indexNode != nil {
			// Check if app is being loaded from a local store, if so then make sure it is present, otherwise
			// do not add it to the app tree, since it is optional in this case.
			// The app index is mandatory only if app is loaded from a remote blob provider - container registry.
			if provider.Type() != compose.BlobProviderTypeRemote {
				if _, err := provider.Info(ctx, indexNode.Descriptor.Digest); err == nil {
					appTree.Children = append(appTree.Children, indexNode)
				}
			} else {
				appTree.Children = append(appTree.Children, indexNode)
			}
		}
	}

	// depth 2, compose service images, each is a sub-tree
	for _, service := range composeProject.Services {
		imageTree, err := compose.LoadImageTree(compose.WithAppRef(ctx, &app.AppRef), provider, platform, service.Image)
		if err != nil {
			return nil, fmt.Errorf("failed to load app service image (%s): %s", service.Name, err)
		}
		if service.Labels != nil {
			if srvHash, ok := service.Labels[AppServiceHashLabelKey]; ok {
				if imageTree.Descriptor.Annotations == nil {
					imageTree.Descriptor.Annotations = make(map[string]string)
				}
				imageTree.Descriptor.Annotations[AppServiceHashLabelKey] = srvHash
			}
		}
		composeTree.Children = append(composeTree.Children, imageTree)
	}
	appTree.Children = append(appTree.Children, &composeTree)
	app.tree = &appTree
	return app, nil
}

func (a *appCtx) GetComposeRoot() *compose.TreeNode {
	return getChildByType(a.tree.Children, compose.BlobTypeAppBundle)
}

func (a *appCtx) GetComposeIndex() *compose.TreeNode {
	return getChildByType(a.tree.Children, compose.BlobTypeAppIndex)
}

func (a *appCtx) GetCompose(ctx context.Context, provider compose.BlobProvider) (project *composetypes.Project, err error) {
	project, _, err = readAndLoadComposeProject(ctx, provider, a)
	return
}

func ReadAppManifest(ctx context.Context, provider compose.BlobProvider, ref string) (*appCtx, *ocispec.Descriptor, error) {
	appRef, err := parseAndCheckAppRef(ref)
	if err != nil {
		return nil, nil, err
	}
	app := appCtx{AppRef: *appRef, storeType: StoreTypeComposeCtl}
	b, err := compose.ReadBlobWithReadLimit(compose.WithBlobType(compose.WithAppRef(ctx, appRef), compose.BlobTypeAppManifest),
		provider, ref, AppManifestMaxSize)
	if err != nil {
		if compose.ErrToBlobState(err) == compose.BlobMissing {
			return &app, nil, compose.ErrAppNotFound
		}
		return &app, nil, err
	}
	if err := json.Unmarshal(b, &app.manifest); err != nil {
		return &app, nil, err
	}
	if app.manifest.MediaType != AppManifestMediaType {
		return nil, nil, fmt.Errorf("invald app manifest media type; expected: %s, got: %s", AppManifestMediaType, app.manifest.MediaType)
	}
	desc := ocispec.Descriptor{
		MediaType: app.manifest.MediaType,
		Digest:    appRef.Digest,
		Size:      int64(len(b)),
		URLs:      []string{ref},
	}
	if _, err := provider.Info(ctx, appRef.Digest); errors.Is(err, errdefs.ErrNotFound) {
		// If app manifest was successfully read through provided blob provider but is missing in the blob store, then
		// it indicates that this app was pulled by `aklite+skopeo` but not by `composectl`.
		app.storeType = StoreTypeSkopeo
	}
	return &app, &desc, nil
}

func (a *appCtx) GetComposeDescriptor() (*ocispec.Descriptor, error) {
	if len(a.manifest.Layers) == 0 {
		return nil, fmt.Errorf("reference to App compose project bundle is not found in the App manifest; " +
			"no layers are found in the App manifest")
	}
	desc := a.manifest.Layers[0]
	if desc.MediaType != AppLayerMediaType {
		return nil, fmt.Errorf("invalid type of App compose project bundle; "+
			"expected: %s, got %s", "application/octet-stream", desc.MediaType)
	}
	desc.URLs = append(desc.URLs, a.Spec.Locator+"@"+desc.Digest.String())
	return &desc, nil
}

func (a *appCtx) GetLayersMetadataDescriptor() (*ocispec.Descriptor, error) {
	if len(a.manifest.Layers) < 2 {
		return nil, fmt.Errorf("reference to App layers metadata is not found; " +
			"App manifest must have at least two layers")
	}
	desc := a.manifest.Layers[1]
	if desc.Annotations == nil {
		return nil, fmt.Errorf("no layers meta is found in the app manifest")
	}
	version, ok := desc.Annotations["layers-meta"]
	if !ok {
		return nil, fmt.Errorf("no layers meta is found in the app manifest")
	}
	if version != AppLayersMetaVersion {
		return nil, fmt.Errorf("unsupported layers meta version; supported: %s, got: %s", AppLayersMetaVersion, version)
	}
	desc.URLs = append(desc.URLs, a.Spec.Locator+"@"+desc.Digest.String())
	return &desc, nil
}

func (a *appCtx) CheckComposeInstallation(ctx context.Context, provider compose.BlobProvider, installationRootDir string) (bundleErrs compose.AppBundleErrs, err error) {
	appIndex, errBundleIndx := a.getAppBundleIndex(ctx, provider)
	if errBundleIndx != nil {
		if errBundleIndx == compose.ErrAppHasNoIndex {
			return a.checkAppBundleInstallation(ctx, provider, installationRootDir)
		} else {
			return nil, errBundleIndx
		}
	}

	bundleErrMap := compose.AppBundleErrs{}
	for filePath, fileDigest := range appIndex {
		f, err := os.Open(path.Join(installationRootDir, filePath))
		if os.IsNotExist(err) {
			bundleErrMap[filePath] = err.Error()
			continue
		}
		r, err := compose.NewSecureReadCloser(f, compose.WithExpectedDigest(fileDigest), compose.WithReadLimit(AppBundleFileMaxSize))
		if err != nil {
			return nil, err
		}
		if _, err := io.ReadAll(r); err != nil {
			bundleErrMap[filePath] = err.Error()
		}
	}
	if len(bundleErrMap) > 0 {
		bundleErrs = bundleErrMap
	}
	return
}

func (a *appCtx) checkAppBundleInstallation(ctx context.Context, provider compose.BlobProvider, installationRootDir string) (bundleErrs compose.AppBundleErrs, err error) {
	appBundleDescriptor, err := a.GetComposeDescriptor()
	if err != nil {
		return nil, err
	}
	appBundleFiles, err := a.indexAppBundle(ctx, provider, appBundleDescriptor)
	if err != nil {
		return nil, err
	}
	bundleErrMap := compose.AppBundleErrs{}
	for _, bundleFile := range appBundleFiles {
		func() {
			filePath := path.Join(installationRootDir, bundleFile.name)
			f, err := os.Open(filePath)
			if err != nil {
				bundleErrMap[filePath] = err.Error()
				return
			}
			defer f.Close()
			r, err := compose.NewSecureReadCloser(f, compose.WithExpectedDigest(bundleFile.digest),
				compose.WithExpectedSize(bundleFile.size))
			if err != nil {
				bundleErrMap[filePath] = err.Error()
				return
			}
			defer r.Close()
			// It produces ErrBlobSizeMismatch or ErrBlobDigestMismatch error if the file is bigger than expected
			// or a hash mismatch is detected after reading all file data
			_, err = io.ReadAll(r)
			if err != nil {
				bundleErrMap[filePath] = err.Error()
			}
		}()
	}
	if len(bundleErrMap) > 0 {
		bundleErrs = bundleErrMap
	}
	return
}

func (a *appCtx) getAppBundleIndex(ctx context.Context, blobProvider compose.BlobProvider) (map[string]digest.Digest, error) {
	indexNode := getChildByType(a.tree.Children, compose.BlobTypeAppIndex)
	if indexNode == nil {
		return nil, compose.ErrAppHasNoIndex
	}
	r, err := blobProvider.GetReadCloser(ctx, compose.WithExpectedDigest(indexNode.Descriptor.Digest),
		compose.WithExpectedSize(indexNode.Descriptor.Size))
	if err != nil {
		if errors.Is(err, errdefs.ErrNotFound) || os.IsNotExist(err) {
			if getChildByType(a.GetComposeRoot().Children, compose.BlobTypeSkopeoImageIndex) != nil {
				// App and its images are pulled by skopeo, hence we should not expect app bundle index in the store
				// even if the app manifest contains a reference to the bundle index.
				return nil, compose.ErrAppHasNoIndex
			}
			return nil, compose.ErrAppIndexNotFound
		}
		return nil, fmt.Errorf("failed to read app bundle index: %s", err.Error())
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read app bundle index: %s", err.Error())
	}

	var appIndex map[string]digest.Digest
	if err := json.Unmarshal(b, &appIndex); err != nil {
		return nil, fmt.Errorf("failed to unmarshal app bundle index: %s", err.Error())
	}
	return appIndex, nil
}

func (a *appCtx) indexAppBundle(ctx context.Context, provider compose.BlobProvider, appArchiveDesc *ocispec.Descriptor) ([]fileInfo, error) {
	srcReader, err := provider.GetReadCloser(compose.WithBlobType(compose.WithAppRef(ctx, &a.AppRef), compose.BlobTypeAppBundle),
		compose.WithRef(a.GetBlobRef(appArchiveDesc.Digest)),
		compose.WithExpectedDigest(appArchiveDesc.Digest), compose.WithExpectedSize(appArchiveDesc.Size))
	if err != nil {
		return nil, err
	}
	defer srcReader.Close()

	r, err := archive.DecompressStream(srcReader)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	tr := tar.NewReader(r)

	var bundleFiles []fileInfo
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break // End of archive
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // Skip non-regular files
		}
		digester := digest.Canonical.Digester()
		_, err = io.Copy(digester.Hash(), tr)
		if err != nil {
			return nil, err
		}
		bundleFiles = append(bundleFiles, fileInfo{
			name:   hdr.Name,
			size:   hdr.Size,
			digest: digester.Digest(),
		})
	}
	return bundleFiles, nil
}

func getChildByType(children []*compose.TreeNode, childType compose.BlobType) *compose.TreeNode {
	for _, c := range children {
		if c.Type == childType {
			return c
		}
	}
	return nil
}

func readCompose(ctx context.Context, provider compose.BlobProvider, app *appCtx) ([]byte, *ocispec.Descriptor, error) {
	composeDesc, err := app.GetComposeDescriptor()
	if err != nil {
		return nil, nil, err
	}

	// Read and parse App compose project
	srcReader, err := provider.GetReadCloser(compose.WithBlobType(compose.WithAppRef(ctx, &app.AppRef), compose.BlobTypeAppBundle),
		compose.WithRef(app.GetBlobRef(composeDesc.Digest)),
		compose.WithExpectedDigest(composeDesc.Digest), compose.WithExpectedSize(composeDesc.Size))
	if err != nil {
		return nil, nil, err
	}
	defer srcReader.Close()

	r, err := archive.DecompressStream(srcReader)
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()
	tr := tar.NewReader(r)

	var composeBytes []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break // End of archive
		}
		if err != nil {
			return nil, nil, err
		}
		// TODO: support different compose file names and multi-file compose projects
		if hdr.Name == "docker-compose.yml" {
			composeBytes, err = io.ReadAll(tr)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	return composeBytes, composeDesc, nil
}

func readAndLoadComposeProject(ctx context.Context, provider compose.BlobProvider, app *appCtx) (*composetypes.Project, *ocispec.Descriptor, error) {
	composeBytes, composeDesc, err := readCompose(ctx, provider, app)
	if err != nil {
		return nil, nil, err
	}
	cfgDetails := composetypes.ConfigDetails{
		ConfigFiles: []composetypes.ConfigFile{
			{
				Filename: "docker-compose.yml",
				Content:  composeBytes,
			},
		},
	}
	cp, err := loader.LoadWithContext(ctx, cfgDetails, func(options *loader.Options) {
		// TODO:  check params required to load project correctly
		//options.SkipNormalization = true
		//options.SkipConsistencyCheck = true
		options.SetProjectName(app.Name(), true)
	})
	if err != nil {
		return nil, nil, err
	}
	return cp, composeDesc, nil
}

func parseAndCheckAppRef(ref string) (*compose.AppRef, error) {
	appRef, err := compose.ParseAppRef(ref)
	if err != nil {
		return nil, err
	}
	if len(appRef.Digest) == 0 {
		return nil, fmt.Errorf("unsupported app reference format; digest is required")
	}
	return appRef, nil
}

func getAppIndexNodeIfPresent(appRef *compose.AppRef, appBundleDesc *ocispec.Descriptor) *compose.TreeNode {
	indexDigestStr, ok := appBundleDesc.Annotations[AnnotationKeyAppBundleIndexDigest]
	if !ok {
		return nil
	}
	indexSizeStr, ok := appBundleDesc.Annotations[AnnotationKeyAppBundleIndexSize]
	if !ok {
		return nil
	}
	indexSize, errConv := strconv.Atoi(indexSizeStr)
	if errConv != nil {
		return nil
	}
	indexDigest, err := digest.Parse(indexDigestStr)
	if err != nil {
		return nil
	}
	return &compose.TreeNode{
		Descriptor: &ocispec.Descriptor{
			Digest: indexDigest,
			Size:   int64(indexSize),
			URLs: []string{
				appRef.GetBlobRef(indexDigest),
			},
		},
		Type: compose.BlobTypeAppIndex,
	}
}
