package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/reference"
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"io"
	"os"
	"path"
	"path/filepath"
	"syscall"
)

type (
	appStore struct {
		bp        compose.BlobProvider
		root      string
		appsRoot  string
		blobsRoot string
		platform  ocispec.Platform
	}
)

func NewAppStore(root string, platform ocispec.Platform) (compose.AppStore, error) {
	cs, err := local.NewStore(root)
	if err != nil {
		return nil, err
	}

	return &appStore{
		bp:        compose.NewLocalBlobProvider(cs),
		root:      root,
		appsRoot:  path.Join(root, "apps"),
		blobsRoot: path.Join(root, "blobs/sha256"),
		platform:  platform,
	}, nil
}

func (s *appStore) ListApps(ctx context.Context) ([]*compose.AppRef, error) {
	var apps []*compose.AppRef
	err := filepath.Walk(s.appsRoot, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.Name() != "uri" {
			return nil
		}
		var appRef *compose.AppRef
		if b, err := os.ReadFile(path); err == nil {
			appRef, err = compose.ParseAppRef(string(b))
			if err != nil {
				return err
			}
		} else {
			return err
		}
		apps = append(apps, appRef)
		return nil
	})
	return apps, err
}

func (s *appStore) GetReadCloser(ctx context.Context, opts ...compose.SecureReadOptions) (io.ReadCloser, error) {
	return s.bp.GetReadCloser(ctx, opts...)
}

func MakeAkliteHappy(ctx context.Context, store compose.AppStore, app compose.App) error {
	storeV1 := store.(*appStore)
	appV1 := app.(*appCtx)
	appDir := path.Join(storeV1.root, "apps", app.Name(), appV1.Digest.Encoded())
	if err := os.MkdirAll(appDir, 0777); err != nil {
		return err
	}
	blobPath := path.Join(storeV1.root, "blobs/sha256", appV1.Digest.Encoded())
	manifestLink := path.Join(appDir, "manifest.json")
	if _, err := os.Stat(manifestLink); err == nil {
		if err := syscall.Unlink(manifestLink); err != nil {
			// TODO: warning
			fmt.Printf("Failed to delete the current app manifest file: %s\n", err.Error())
		}
	}
	if err := syscall.Symlink(blobPath, manifestLink); err != nil {
		return err
	}

	if err := os.WriteFile(path.Join(appDir, "uri"), []byte(appV1.Spec.String()), 0644); err != nil {
		return err
	}

	appBundleDesc, err := appV1.GetComposeDescriptor()
	if err != nil {
		return err
	}
	appBundleLink := path.Join(appDir, appBundleDesc.Digest.Encoded()+".tgz")
	appBundleLinkExists := false
	if _, err := os.Stat(appBundleLink); err == nil {
		if unlinkErr := syscall.Unlink(appBundleLink); unlinkErr != nil {
			appBundleLinkExists = true
		}
	}
	if !appBundleLinkExists {
		if err := syscall.Symlink(path.Join(storeV1.root, "blobs/sha256", appBundleDesc.Digest.Encoded()), appBundleLink); err != nil {
			return err
		}
	}
	if b, _, err := readCompose(ctx, storeV1.bp, appV1); err == nil {
		if writeErr := os.WriteFile(path.Join(appDir, "docker-compose.yml"), b, 0644); writeErr != nil {
			fmt.Printf("Failed to write compose file: %s\n", writeErr.Error())
		}
	} else {
		return err
	}

	appComposeRoot := app.GetComposeRoot()
	for _, imageNode := range appComposeRoot.Children {
		uri, err := reference.Parse(imageNode.Descriptor.URLs[0])
		if err != nil {
			return err
		}
		indexBlobPath := path.Join(storeV1.root, "blobs/sha256", uri.Digest().Encoded())
		if err := os.Chmod(indexBlobPath, 0644); err != nil {
			return err
		}
		imagePath := uri.Locator[len(uri.Hostname()):]
		imageDir := path.Join(appDir, "images", uri.Hostname(), imagePath, uri.Digest().Encoded())
		if _, err := os.Stat(path.Join(imageDir, "index.json")); err == nil {
			continue
		}
		if err := os.MkdirAll(imageDir, 0777); err != nil {
			return err
		}
		indexFile := path.Join(imageDir, "index.json")
		if imageNode.Type == compose.BlobTypeImageIndex {
			if err := syscall.Symlink(indexBlobPath, indexFile); err != nil {
				return err
			}
			for _, im := range imageNode.Children {
				blobPath := path.Join(storeV1.root, "blobs/sha256", im.Descriptor.Digest.Encoded())
				if err := os.Chmod(blobPath, 0644); err != nil {
					return err
				}
			}
		} else {
			index := ocispec.Index{
				Versioned: specs.Versioned{
					SchemaVersion: 2,
				},
				Manifests: []ocispec.Descriptor{
					*imageNode.Descriptor,
				},
			}
			b, err := json.Marshal(&index)
			if err != nil {
				return err
			}
			if err := os.WriteFile(indexFile, b, 0644); err != nil {
				return err
			}
		}

	}
	return err
}
