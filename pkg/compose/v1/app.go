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

	appCtx struct {
		compose.AppRef
		manifest   ocispec.Manifest
		layersMeta map[string]layersMeta
		tree       *compose.AppTree
	}

	appLoader struct{}

	ctxKeyType string
)

const (
	AppManifestMediaType = "application/vnd.oci.image.manifest.v1+json"
	AppManifestMaxSize   = 50 * 1024
	AppLayerMediaType    = "application/octet-stream"
	AppLayersMetaVersion = "v1"

	ctxKeyAppRef ctxKeyType = "app:ref"
)

func WithAppRef(ctx context.Context, ref *compose.AppRef) context.Context {
	return context.WithValue(ctx, ctxKeyAppRef, ref)
}

func GetAppRef(ctx context.Context) *compose.AppRef {
	if appRef, ok := ctx.Value(ctxKeyAppRef).(*compose.AppRef); ok {
		return appRef
	}
	return nil
}

func (a *appCtx) Name() string {
	return a.AppRef.Name
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

func (l *appLoader) LoadAppTree(ctx context.Context, provider compose.BlobProvider, platform platforms.MatchComparer, ref string) (compose.App, *compose.AppTree, error) {
	// root node
	app, rootDesc, err := ReadAppManifest(ctx, provider, ref)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read app manifest: %s", err)
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
			if !errors.Is(readErr, errdefs.ErrNotFound) {
				fmt.Printf("Failed to read app layers meta: %s\n", readErr.Error())
			}
			// TODO: log else (if not found)
		}
	}

	// depth 1, compose
	composeProject, composeDesc, err := readAndLoadComposeProject(ctx, provider, app)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read app compose project: %s", err)
	}
	composeTree := compose.TreeNode{
		Descriptor: composeDesc,
		Type:       compose.BlobTypeAppBundle,
	}

	// depth 2, compose service images, each is a sub-tree
	for _, service := range composeProject.Services {
		imageTree, err := compose.LoadImageTree(WithAppRef(ctx, &app.AppRef), provider, platform, service.Image)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load app service image (%s): %s", service.Name, err)
		}
		composeTree.Children = append(composeTree.Children, imageTree)
	}
	appTree.Children = append(appTree.Children, &composeTree)
	app.tree = &appTree
	return app, &appTree, nil
}

func (a *appCtx) GetComposeRoot() *compose.TreeNode {
	for _, c := range a.tree.Children {
		if c.Type == compose.BlobTypeAppBundle {
			return c
		}
	}
	return nil
}

func ReadAppManifest(ctx context.Context, provider compose.BlobProvider, ref string) (*appCtx, *ocispec.Descriptor, error) {
	appRef, err := parseAndCheckAppRef(ref)
	if err != nil {
		return nil, nil, err
	}
	app := appCtx{AppRef: *appRef}
	b, err := compose.ReadBlobWithReadLimit(compose.WithBlobType(WithAppRef(ctx, appRef), compose.BlobTypeAppManifest),
		provider, ref, AppManifestMaxSize)
	if err != nil {
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

func readCompose(ctx context.Context, provider compose.BlobProvider, app *appCtx) ([]byte, *ocispec.Descriptor, error) {
	composeDesc, err := app.GetComposeDescriptor()
	if err != nil {
		return nil, nil, err
	}

	// Read and parse App compose project
	srcReader, err := provider.GetReadCloser(compose.WithBlobType(WithAppRef(ctx, &app.AppRef), compose.BlobTypeAppBundle),
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
	if err != nil {
		return nil, nil, err
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
