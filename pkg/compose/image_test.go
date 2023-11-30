package compose

import (
	"context"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"testing"
)

var (
	imageIndex = `
{
  "manifests": [
    {
      "digest": "sha256:48d9183eb12a05c99bcc0bf44a003607b8e941e1d4f41f9ad12bdcc4b5672f86",
      "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
      "platform": {
        "architecture": "amd64",
        "os": "linux"
      },
      "size": 528
    },
    {
      "digest": "sha256:9909a171ac287c316f771a4f1d1384df96957ed772bc39caf6deb6e3e360316f",
      "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
      "platform": {
        "architecture": "arm",
        "os": "linux",
        "variant": "v7"
      },
      "size": 528
    },
    {
      "digest": "sha256:6ce9a9a256a3495ae60ab0059ed1c7aee5ee89450477f2223f6ea7f6296df555",
      "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
      "platform": {
        "architecture": "arm64",
        "os": "linux",
        "variant": "v8"
      },
      "size": 528
    }
  ],
  "mediaType": "application/vnd.docker.distribution.manifest.list.v2+json",
  "schemaVersion": 2
}
`
	imageManifest = `{
   "schemaVersion": 2,
   "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
   "config": {
      "mediaType": "application/vnd.docker.container.image.v1+json",
      "size": 1487,
      "digest": "sha256:3cc20332140056b331ad58185ab589c085f4e7d79d8c9769533d6a9b95d4b1b0"
   },
   "layers": [
      {
         "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
         "size": 3331831,
         "digest": "sha256:579b34f0a95bb83b3acd6b3249ddc52c3d80f5c84b13c944e9e324feb86dd329"
      }
   ]
}`
)

func TestImageTreeWalk(t *testing.T) {
	blobs := map[digest.Digest][]byte{
		digest.FromBytes([]byte(imageIndex)):    []byte(imageIndex),
		digest.FromBytes([]byte(imageManifest)): []byte(imageManifest),
	}

	blobProvider := NewMemoryBlobProvider(blobs)
	for digest := range blobs {
		thisPlatform := specs.Platform{Architecture: "arm64", OS: "linux"}
		platformMatcher := platforms.OnlyStrict(thisPlatform)
		imageTree, err := LoadImageTree(context.Background(), blobProvider, platformMatcher, fmt.Sprintf("host/factory/app@%s", digest.String()))
		if err != nil {
			t.Fatal(err.Error())
		}
		err = imageTree.Walk(func(node *TreeNode, depth int) error {
			switch depth {
			case 0:
				{
					if digest != node.Descriptor.Digest {
						t.Errorf("got unexpected digest; expected %s, got: %s", digest, node.Descriptor.Digest)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err.Error())
		}
		config, layers, err := imageTree.GetImageConfigAndLayers()
		if err != nil {
			t.Fatal(err.Error())
		}
		expectedConfigDigest := "3cc20332140056b331ad58185ab589c085f4e7d79d8c9769533d6a9b95d4b1b0"
		if config.Digest.Encoded() != expectedConfigDigest {
			t.Errorf("got unexpected image config digest; expected %s, got: %s", expectedConfigDigest, config.Digest.Encoded())
		}
		if len(layers) != 1 {
			t.Errorf("unexpected image layer number; expected 1, got: %d", len(layers))
		}
		expectedLayerDigest := "579b34f0a95bb83b3acd6b3249ddc52c3d80f5c84b13c944e9e324feb86dd329"
		if layers[0].Digest.Encoded() != expectedLayerDigest {
			t.Errorf("got unexpected image layer digest; expected %s, got: %s", expectedLayerDigest, layers[0].Digest.Encoded())
		}
	}
}
