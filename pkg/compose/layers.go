//go:build publish

package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/foundriesio/composeapp/internal"

	"github.com/compose-spec/compose-go/types"
	"github.com/docker/distribution"
	"github.com/docker/distribution/manifest"
	"github.com/docker/distribution/manifest/manifestlist"
	"github.com/docker/distribution/manifest/ocischema"
	"github.com/docker/distribution/manifest/schema2"
	"github.com/docker/distribution/reference"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

type (
	ArchManifestServices map[string]map[distribution.ManifestService]digest.Digest
	LayerMeta            struct {
		Size        int64 `json:"size"`
		Usage       int64 `json:"usage"`
		ArchiveSize int64 `json:"archive_size,omitempty"`
	}
	ArchLayersMeta struct {
		FsBlockSize uint32                      `json:"fs_block_size"`
		Layers      map[digest.Digest]LayerMeta `json:"layers"`
	}
	LayersMeta map[string]ArchLayersMeta
)

func GetManifestService(ctx context.Context, regClient internal.RegistryClient, repoRef reference.Named) (distribution.ManifestService, error) {
	repo, err := regClient.GetRepository(ctx, repoRef)
	if err != nil {
		return nil, err
	}
	return repo.Manifests(ctx, nil)
}

func GetBlobService(ctx context.Context, regClient internal.RegistryClient, repoRef reference.Named) (distribution.BlobStore, error) {
	repo, err := regClient.GetRepository(ctx, repoRef)
	if err != nil {
		return nil, err
	}
	return repo.Blobs(ctx), nil
}

func GetAppLayers(ctx context.Context, services map[string]types.ServiceConfig, archList []string) (map[string][]distribution.Descriptor, error) {
	svcImages := make(map[string]string)
	for svc, svcCfg := range services {
		svcImages[svc] = svcCfg.Image
	}
	return GetAppLayersFromMap(ctx, svcImages, archList)
}

func GetLayers(ctx context.Context, services map[string]interface{}, archList []string) (map[string][]distribution.Descriptor, error) {
	svcImages := make(map[string]string)
	for svc, cfg := range services {
		svcCfg := cfg.(map[string]interface{})
		svcImages[svc] = svcCfg["image"].(string)
	}
	return GetAppLayersFromMap(ctx, svcImages, archList)
}

func GetAppLayersFromMap(ctx context.Context, svcImages map[string]string, archList []string) (map[string][]distribution.Descriptor, error) {
	regClient := internal.NewRegistryClient()

	// Get manifests per architecture and per image, for one architecture there should be one manifest for each service image
	// One image might have two or more manifests per the same architecture, it's odd, but is true, see nginx's manifest list
	// Thus, we have to created a map of maps, arch -> image/manifest-service -> manifest-digest, to avoid inclusion
	// more then manifest per image and per architecture
	archToManifestList := make(ArchManifestServices)
	for _, image := range svcImages {
		imageRef, err := reference.ParseNamed(image)
		if err != nil {
			return nil, err
		}

		imageManifestSvc, err := GetManifestService(ctx, regClient, imageRef)
		if err != nil {
			return nil, err
		}

		indexManifest, err := imageManifestSvc.Get(ctx, imageRef.(reference.Canonical).Digest())
		if err != nil {
			return nil, err
		}

		populateFromList := func(manifestList *manifestlist.DeserializedManifestList) {
			for _, manifest := range manifestList.Manifests {
				if _, ok := archToManifestList[manifest.Platform.Architecture]; !ok {
					archToManifestList[manifest.Platform.Architecture] = make(map[distribution.ManifestService]digest.Digest)
				}
				archToManifestList[manifest.Platform.Architecture][imageManifestSvc] = manifest.Digest
			}
		}

		populateFromManifest := func(manifest *schema2.DeserializedManifest) error {
			imageBlobSvc, err := GetBlobService(ctx, regClient, imageRef)
			if err != nil {
				return err
			}
			b, err := imageBlobSvc.Get(ctx, manifest.Config.Digest)
			if err != nil {
				return err
			}
			config := make(map[string]interface{})
			err = json.Unmarshal(b, &config)
			if err != nil {
				return err
			}
			arch := config["architecture"].(string)
			if _, ok := archToManifestList[arch]; !ok {
				archToManifestList[arch] = make(map[distribution.ManifestService]digest.Digest)
			}
			archToManifestList[arch][imageManifestSvc] = imageRef.(reference.Canonical).Digest()
			return nil
		}

		switch im := indexManifest.(type) {
		case *manifestlist.DeserializedManifestList:
			populateFromList(im)
		case *schema2.DeserializedManifest:
			err = populateFromManifest(im)
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unexpected type of image manifest; image: %s, type: %T", imageRef.(reference.Canonical).String(), indexManifest)
		}
	}

	appLayers := make(map[string]map[string]distribution.Descriptor)
	expectedManNumber := len(svcImages)
	isInArchList := func(arch string) bool {
		if len(archList) == 0 {
			return true
		}

		for _, a := range archList {
			if arch == a {
				return true
			}
		}
		return false
	}
	for arch, manifests := range archToManifestList {
		// Shortlist architectures, we need to include only architectures for which there is one manifest per each service image
		if len(manifests) != expectedManNumber {
			fmt.Printf("  |-> exclude  %s architecture, some of the app images (%d images) don't have manifest for it\n", arch, expectedManNumber-len(manifests))
			delete(archToManifestList, arch)
			continue
		}

		if !isInArchList(arch) {
			fmt.Printf("  |-> exclude  %s architecture since it's not in a list of the factory supported architectures: %q\n", arch, archList)
			delete(archToManifestList, arch)
			continue
		}

		fmt.Printf("  |-> getting app layers for architecture: %s\n", arch)

		// we use map instead of slice/array of Descriptor in order to avoid layer duplication since
		// different images can consists of the same layers (layer intersection across images)
		appLayers[arch] = make(map[string]distribution.Descriptor)
		for manSvc, d := range manifests {
			manifest, err := manSvc.Get(ctx, d)
			if err != nil {
				return nil, err
			}
			var layers []distribution.Descriptor
			switch v := manifest.(type) {
			case *schema2.DeserializedManifest:
				layers = v.Layers
			case *ocischema.DeserializedManifest:
				layers = v.Layers
			default:
				return nil, fmt.Errorf("unsupport manifest type: %T", manifest)
			}
			for _, layer := range layers {
				appLayers[arch][layer.Digest.Encoded()] = layer
			}
		}
	}

	// now we need to sort layers in order to get consistent hash if app layers don't change
	sortedAppLayers := make(map[string][]distribution.Descriptor)

	for arch, layers := range appLayers {
		sortedLayerHashes := make([]string, len(layers))
		var indx int
		for layerHash := range layers {
			sortedLayerHashes[indx] = layerHash
			indx++
		}
		sort.Strings(sortedLayerHashes)

		sortedAppLayers[arch] = make([]distribution.Descriptor, len(layers))
		for ii, hash := range sortedLayerHashes {
			sortedAppLayers[arch][ii] = layers[hash]
		}
	}

	return sortedAppLayers, nil
}

func ComposeAppLayersManifest(arch string, layers []distribution.Descriptor) (distribution.Manifest, *distribution.Descriptor, error) {
	platform := v1.Platform{
		Architecture: arch,
		// OS:           "LMP", make manifest a bit smaller until the updated aklite is installed on every device
	}

	manifestDef := struct {
		manifest.Versioned
		Platform    *v1.Platform              `json:"platform"`
		Layers      []distribution.Descriptor `json:"layers"`
		Annotations map[string]string         `json:"annotations"`
	}{
		Versioned: manifest.Versioned{
			SchemaVersion: 2,
			MediaType:     v1.MediaTypeImageIndex,
		},
		Platform: &platform,
		Layers:   layers,
		Annotations: map[string]string{
			"compose-app-layers": "v1",
		},
	}
	manifestJson, err := json.Marshal(manifestDef)
	if err != nil {
		return nil, nil, err
	}

	// Convert the custom manifest that breaks the OCI image specification into the type that the distribution
	// functionality understands. Use the json unmarshalling functionality for it.
	dm := manifestlist.DeserializedManifestList{}
	err = dm.UnmarshalJSON(manifestJson)
	if err != nil {
		return nil, nil, err
	}
	return dm, &distribution.Descriptor{
		MediaType: dm.MediaType,
		Size:      int64(len(manifestJson)),
		Digest:    digest.FromBytes(manifestJson),
		Platform:  manifestDef.Platform,
	}, nil
}

func PostAppLayersManifests(ctx context.Context, appRef string, layers map[string][]distribution.Descriptor, dryRun bool) ([]distribution.Descriptor, error) {
	// sort layer lists by arch

	manifestDescArchs := make([]string, len(layers))
	var ii int
	for arch := range layers {
		manifestDescArchs[ii] = arch
		ii++
	}

	sort.Strings(manifestDescArchs)

	regClient := internal.NewRegistryClient()

	ref, err := reference.ParseNormalizedNamed(appRef)
	if err != nil {
		return nil, err
	}

	manSvc, err := GetManifestService(ctx, regClient, ref)
	if err != nil {
		return nil, err
	}

	ii = 0
	manDescrs := make([]distribution.Descriptor, len(layers))
	for _, arch := range manifestDescArchs {
		manifest, desc, err := ComposeAppLayersManifest(arch, layers[arch])
		if err != nil {
			return nil, err
		}
		if dryRun {
			fmt.Printf("  |-> skipping layer manifest publishing for dryrun\n")
		} else {
			fmt.Printf("  |-> posting a layer manifest for architecture: %s...", arch)
			digest, err := manSvc.Put(ctx, manifest, distribution.WithTag("layers-"+arch+(desc.Digest.Hex())[:7]))
			if err != nil {
				return nil, err
			}
			if digest.Encoded() != (*desc).Digest.Encoded() {
				return nil, fmt.Errorf("digest of the posted manifest doesn't match to the composed manifest digest")
			}
			fmt.Printf("OK |-> digest: %s\n", digest)
			manDescrs[ii] = *desc
		}
		ii++
	}
	return manDescrs, nil
}

func GetAppLayersMeta(layersMetaFile string, appLayers map[string][]distribution.Descriptor) ([]byte, error) {
	var layersMeta LayersMeta
	appLayersMeta := LayersMeta{}

	fmt.Println("== Parsing all apps layers metadata...")
	if b, err := os.ReadFile(layersMetaFile); err == nil {
		if err := json.Unmarshal(b, &layersMeta); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}

	fmt.Println("== Getting App layers metadata...")
	for arch, layers := range appLayers {
		if _, exists := layersMeta[arch]; !exists {
			return nil, fmt.Errorf("no metadata about app layers of the given arch: %s", arch)
		}
		appLayersMeta[arch] = ArchLayersMeta{FsBlockSize: layersMeta[arch].FsBlockSize, Layers: map[digest.Digest]LayerMeta{}}
		for _, l := range layers {
			if _, exists := layersMeta[arch].Layers[l.Digest]; !exists {
				return nil, fmt.Errorf("app layer hasn't been built;"+
					" one of the App images must have been changed since tagging and before pinning;"+
					" layer digest: %s", l.Digest)
			}
			appLayersMeta[arch].Layers[l.Digest] = LayerMeta{
				// Layer's diff size (diff = layer's part of rootfs )
				Size: layersMeta[arch].Layers[l.Digest].Size,
				// Disk usage by the layer's data (rootfs) and metadata
				Usage: layersMeta[arch].Layers[l.Digest].Usage,
				// Layer's archive/blob size
				ArchiveSize: l.Size,
			}
		}
	}
	return json.Marshal(appLayersMeta)
}
