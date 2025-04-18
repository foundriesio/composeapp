package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/reference"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	ImageRootMaxSize = 100 * 1024
)

type (
	ImageRoot struct {
		specs.Versioned
		MediaType string          `json:"mediaType,omitempty"`
		Config    json.RawMessage `json:"config,omitempty"`
		Layers    json.RawMessage `json:"layers,omitempty"`
		Manifests json.RawMessage `json:"manifests,omitempty"`
		FSLayers  json.RawMessage `json:"fsLayers,omitempty"` // schema 1

		Type     BlobType
		Children []TreeNode
	}

	ImageTree TreeNode
	ImageRef  struct {
		reference.Spec
		Digest digest.Digest
	}
)

func (t *ImageTree) GetServiceHash() string {
	if !(t.Type == BlobTypeImageIndex ||
		t.Type == BlobTypeSkopeoImageIndex ||
		t.Type == BlobTypeImageManifest) {
		return ""
	}
	if t.Descriptor != nil {
		if hash, ok := t.Descriptor.Annotations[AppServiceHashLabelKey]; ok {
			return hash
		}
	}
	return ""
}

func ParseImageRef(ref string) (*ImageRef, error) {
	refSpec, err := reference.Parse(ref)
	if err != nil {
		return nil, err
	}
	return &ImageRef{
		Spec:   refSpec,
		Digest: refSpec.Digest(),
	}, nil
}

func (r ImageRef) GetBlobRef(digest digest.Digest) string {
	return r.Locator + "@" + digest.String()
}

func (t *ImageTree) Print(initDepth ...int) {
	startDepth := 0
	if len(initDepth) > 0 {
		startDepth = initDepth[0]
	}
	rootType := "unknown"
	err := (*TreeNode)(t).Walk(func(node *TreeNode, depth int) error {
		printDepth := (startDepth + depth) * 10
		if depth == 0 {
			if images.IsIndexType(node.Descriptor.MediaType) {
				rootType = "index"
			} else if images.IsManifestType(node.Descriptor.MediaType) {
				rootType = "manifest"
			}
			var id string
			if node.HasRef() {
				id = node.Ref()
			} else {
				id = node.Descriptor.Digest.String()
			}
			fmt.Printf("%*s%s: %s, %d\n", printDepth, "|—>", rootType, id, node.Descriptor.Size)
		} else if depth == 1 && rootType == "index" {
			fmt.Printf("%*smanifest: %s, %s, %d\n", printDepth, " ", node.Descriptor.Digest.String(), node.Descriptor.Platform.Architecture, node.Descriptor.Size)
		} else {
			if images.IsConfigType(node.Descriptor.MediaType) {
				fmt.Printf("%*sconfig: %s, %d\n", printDepth, " ", node.Descriptor.Digest.String(), node.Descriptor.Size)
			} else if images.IsLayerType(node.Descriptor.MediaType) {
				fmt.Printf("%*slayer:  %s, %d\n", printDepth, " ", node.Descriptor.Digest.String(), node.Descriptor.Size)
			}
		}
		return nil
	})
	if err != nil {
		fmt.Printf("Failed to print image tree: %s\n", err.Error())
	}
}

func LoadImageTree(ctx context.Context, provider BlobProvider, platform platforms.MatchComparer, ref string) (*TreeNode, error) {
	// root node
	rootRef, imageRoot, rootDesc, err := ReadImageRoot(WithBlobType(ctx, BlobTypeImageIndex), provider, ref)
	if err != nil {
		return nil, err
	}
	imageTree := TreeNode{
		Descriptor: rootDesc,
		Type:       imageRoot.Type,
	}

	// depth 1, arch specific manifests or config + layers
	var childrenDescs []*ocispec.Descriptor
	switch imageRoot.Type {
	case BlobTypeSkopeoImageIndex, BlobTypeImageIndex:
		{
			if err := json.Unmarshal(imageRoot.Manifests, &childrenDescs); err != nil {
				return nil, err
			}
		}
	case BlobTypeImageManifest:
		{
			layersMap := make(map[digest.Digest]struct{})
			// parse config and layers and add them as image root children
			configDesc, layerDescs, err := ParseImageRootAsManifest(imageRoot)
			if err != nil {
				return nil, err
			}
			configDesc.URLs = []string{rootRef.GetBlobRef(configDesc.Digest)}
			imageTree.Children = append(imageTree.Children, &TreeNode{
				Descriptor: configDesc, Type: BlobTypeImageConfig, Children: nil,
			})
			for _, l := range layerDescs {
				if _, ok := layersMap[l.Digest]; ok {
					continue
				}
				l.URLs = []string{rootRef.GetBlobRef(l.Digest)}
				imageTree.Children = append(imageTree.Children, &TreeNode{
					Descriptor: l, Type: BlobTypeImageLayer, Children: nil,
				})
				layersMap[l.Digest] = struct{}{}
			}
			return &imageTree, nil
		}
	default:
		return nil, fmt.Errorf("unsupported image root type: %s", rootDesc.MediaType)
	}

	// depth 2 if image root is index
	if imageRoot.Type != BlobTypeImageIndex && imageRoot.Type != BlobTypeSkopeoImageIndex {
		panic("expected image index got " + imageRoot.MediaType)
	}

	for _, c := range childrenDescs {
		var grandchildren []*TreeNode
		if imageRoot.Type != BlobTypeSkopeoImageIndex && !platform.Match(*c.Platform) {
			continue
		}
		manifestRef := rootRef.GetBlobRef(c.Digest)
		manifest, err := ReadImageManifest(ctx, provider, manifestRef, c.Size)
		if err != nil {
			return nil, err
		}
		c.URLs = []string{manifestRef}
		// Add references (URLs) of the index/parent to the list of the given image manifest URLs,
		// hence the `URLs` field contains both:
		// 1) URL of this platform specific image manifest
		// 2) URL of the index that enlists all platform specific manifests including the given one
		c.URLs = append(c.URLs, rootDesc.URLs...)
		manifest.Config.URLs = []string{rootRef.GetBlobRef(manifest.Config.Digest)}
		grandchildren = append(grandchildren, &TreeNode{Descriptor: &manifest.Config, Type: BlobTypeImageConfig, Children: nil})
		layersMap := make(map[digest.Digest]struct{})
		for ii := 0; ii < len(manifest.Layers); ii++ {
			l := &manifest.Layers[ii]
			if _, ok := layersMap[l.Digest]; ok {
				continue
			}
			l.URLs = []string{rootRef.GetBlobRef(l.Digest)}
			grandchildren = append(grandchildren, &TreeNode{Descriptor: l, Type: BlobTypeImageLayer, Children: nil})
			layersMap[l.Digest] = struct{}{}
		}
		imageTree.Children = append(imageTree.Children, &TreeNode{
			Descriptor: c, Type: BlobTypeImageManifest, Children: grandchildren,
		})
	}
	return &imageTree, nil
}

func ReadImageRoot(ctx context.Context, provider BlobProvider, ref string) (*ImageRef, *ImageRoot, *ocispec.Descriptor, error) {
	imageRef, err := ParseImageRef(ref)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(imageRef.Digest) == 0 {
		return imageRef, nil, nil, fmt.Errorf("unsupported image reference format; digest is required")
	}

	b, err := ReadBlobWithReadLimit(ctx, provider, ref, ImageRootMaxSize)
	if err != nil {
		return imageRef, nil, nil, err
	}
	var imageRoot ImageRoot
	if err := json.Unmarshal(b, &imageRoot); err != nil {
		return imageRef, nil, nil, err
	}

	imageRoot.Type, err = validateRoot(&imageRoot)
	if err != nil {
		return imageRef, nil, nil, fmt.Errorf("invalid image root: %s", err.Error())
	}
	return imageRef, &imageRoot, &ocispec.Descriptor{
		MediaType: imageRoot.MediaType,
		Digest:    imageRef.Digest,
		Size:      int64(len(b)),
		URLs:      []string{ref},
	}, nil
}

func ReadImageManifest(ctx context.Context, provider BlobProvider, ref string, size int64) (*ocispec.Manifest, error) {
	refSpec, err := reference.Parse(ref)
	if err != nil {
		return nil, err
	}
	if len(refSpec.Digest()) == 0 {
		return nil, fmt.Errorf("unsupported image reference format; digest is required")
	}

	b, err := ReadBlobWithResolving(ctx, provider, ref, size)
	if err != nil {
		return nil, err
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func ParseImageRootAsManifest(root *ImageRoot) (*ocispec.Descriptor, []*ocispec.Descriptor, error) {
	if !images.IsManifestType(root.MediaType) {
		return nil, nil, fmt.Errorf("invalid image root type; expected manifest, got: %s", root.MediaType)
	}
	var configDesc ocispec.Descriptor
	var layerDescs []*ocispec.Descriptor
	var err error
	if err = json.Unmarshal(root.Config, &configDesc); err == nil {
		err = json.Unmarshal(root.Layers, &layerDescs)
	}
	if err != nil {
		return nil, nil, err
	}
	return &configDesc, layerDescs, nil
}

func (t *TreeNode) GetImageConfigAndLayers() (*ocispec.Descriptor, []*ocispec.Descriptor, error) {
	var children []*TreeNode
	if images.IsIndexType(t.Descriptor.MediaType) {
		for _, c := range t.Children {
			if c.Children != nil {
				children = c.Children
			}
		}
	} else if images.IsManifestType(t.Descriptor.MediaType) {
		children = t.Children
	} else {
		return nil, nil, fmt.Errorf("invalid image root blob format: %s", t.Descriptor.MediaType)
	}
	var config *ocispec.Descriptor
	var layers []*ocispec.Descriptor

	for _, c := range children {
		if images.IsConfigType(c.Descriptor.MediaType) {
			config = c.Descriptor
		} else if images.IsLayerType(c.Descriptor.MediaType) {
			layers = append(layers, c.Descriptor)
		} else {
			return nil, nil, fmt.Errorf("invalid image config or layer format: %s", t.Descriptor.MediaType)
		}
	}
	return config, layers, nil
}

func validateRoot(root *ImageRoot) (BlobType, error) {
	blobType := BlobTypeUnknown
	if len(root.FSLayers) != 0 {
		return blobType, fmt.Errorf("media-type: schema 1 not supported")
	}
	if images.IsManifestType(root.MediaType) {
		if len(root.Manifests) != 0 || images.IsIndexType(root.MediaType) {
			return blobType, fmt.Errorf("media-type: expected manifest but found index (%s)", root.MediaType)
		}
		blobType = BlobTypeImageManifest
	} else if images.IsIndexType(root.MediaType) {
		if len(root.Config) != 0 || len(root.Layers) != 0 || images.IsManifestType(root.MediaType) {
			return blobType, fmt.Errorf("media-type: expected index but found manifest (%s)", root.MediaType)
		}
		blobType = BlobTypeImageIndex
	} else if root.SchemaVersion == 2 && len(root.Manifests) > 0 {
		blobType = BlobTypeSkopeoImageIndex
	}
	return blobType, nil
}
