//go:build publish

package internal

import (
	"context"
	"encoding/json"
	"github.com/docker/distribution"
	"github.com/docker/distribution/manifest"
	"github.com/docker/distribution/manifest/ocischema"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

type (
	AppManifest struct {
		ocischema.Manifest
		// ArtifactType is the IANA media type of the artifact this schema refers to.
		ArtifactType string `json:"artifactType,omitempty"`
		// This field breaks the OCI image specification. It should be removed once all devices switch to version >= v93
		Manifests  []distribution.Descriptor `json:"manifests,omitempty"`
	}
	ManifestBuilder struct {
		bs distribution.BlobService
		manifest AppManifest
	}
)

var (
	AppManifestTemplate = AppManifest{
		Manifest:     ocischema.Manifest{
			Versioned: manifest.Versioned{
				SchemaVersion: 2,
				MediaType:     v1.MediaTypeImageManifest,
			},
			// Set the empty descriptor for the config as the specification guides
			// https://github.com/opencontainers/image-spec/blob/main/manifest.md#guidance-for-an-empty-descriptor
			Config: distribution.Descriptor{
				MediaType: v1.DescriptorEmptyJSON.MediaType,
				Digest: v1.DescriptorEmptyJSON.Digest,
				Size: v1.DescriptorEmptyJSON.Size,
			},
			Annotations: map[string]string{"compose-app": "v1"},
		},
		ArtifactType: "application/vnd.fio+compose-app",
	}
)

func NewManifestBuilder(bs distribution.BlobService)  distribution.ManifestBuilder {
	return &ManifestBuilder{
		bs: bs,
		manifest: AppManifestTemplate,
	}
}

func (mb *ManifestBuilder) Build(ctx context.Context) (distribution.Manifest, error) {
	_, err := mb.bs.Stat(ctx, mb.manifest.Config.Digest)
	switch err {
	case nil:
		// Config blob is present in the blob store
		return fromStruct(mb.manifest)
	case distribution.ErrBlobUnknown:
		// nop
	default:
		return nil, err
	}
	// Add config to the blob store
	_, err = mb.bs.Put(ctx, mb.manifest.Config.MediaType, v1.DescriptorEmptyJSON.Data)
	if err != nil {
		return nil, err
	}
	return fromStruct(mb.manifest)
}

// AppendReference adds a reference to the current ManifestBuilder.
func (mb *ManifestBuilder) AppendReference(d distribution.Describable) error {
	mb.manifest.Layers = append(mb.manifest.Layers, d.Descriptor())
	return nil
}

// References returns the current references added to this builder.
func (mb *ManifestBuilder) References() []distribution.Descriptor {
	return mb.manifest.Layers
}

func (mb *ManifestBuilder) SetLayerMetaManifests(manifests []distribution.Descriptor)  {
	mb.manifest.Manifests = manifests
}

func fromStruct(m AppManifest) (*ocischema.DeserializedManifest, error) {
	canonical, err := json.MarshalIndent(&m, "", "   ")

	dm := ocischema.DeserializedManifest{}
	err = dm.UnmarshalJSON(canonical)
	if err != nil {
		return nil, err
	}
	return &dm, err
}
