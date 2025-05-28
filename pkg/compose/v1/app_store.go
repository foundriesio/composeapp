package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/reference"
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"syscall"
)

type (
	appStore struct {
		bp               compose.BlobProvider
		root             string
		appsRoot         string
		blobsRoot        string
		platform         ocispec.Platform
		skopeoStoreAware bool
	}
)

const (
	BlobProviderTypeAppStore compose.BlobProviderType = "blob-provider:app-store"
)

func NewAppStore(root string, platform ocispec.Platform, skopeoStoreAware ...bool) (compose.AppStore, error) {
	cs, err := local.NewStore(root)
	if err != nil {
		return nil, err
	}

	var isSkopeoStoreAware = true
	if len(skopeoStoreAware) > 0 {
		isSkopeoStoreAware = skopeoStoreAware[0]
	}

	return &appStore{
		bp:               compose.NewLocalBlobProvider(cs),
		root:             root,
		appsRoot:         path.Join(root, "apps"),
		blobsRoot:        compose.GetBlobsRootFor(root),
		platform:         platform,
		skopeoStoreAware: isSkopeoStoreAware,
	}, nil
}

func (s *appStore) Type() compose.BlobProviderType {
	return BlobProviderTypeAppStore
}

func (s *appStore) ListApps(ctx context.Context) ([]*compose.AppRef, error) {
	var apps []*compose.AppRef
	err := filepath.Walk(s.appsRoot, func(path string, fi os.FileInfo, err error) error {
		if pathErr, ok := err.(*os.PathError); ok {
			if s.appsRoot == pathErr.Path {
				// Just exit with empty error since the store is simply empty
				return nil
			}
		}
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

func (s *appStore) RemoveApps(ctx context.Context, apps []*compose.AppRef, prune bool) error {
	for _, a := range apps {
		appVerDir := filepath.Join(s.appsRoot, a.Name, a.Digest.Encoded())
		if _, err := os.Stat(appVerDir); os.IsNotExist(err) {
			// TODO: add debug level logging instead of just printing message unconditionally
			fmt.Printf("app dir does not exist in the store's app dir,"+
				" will check and remove app blobs only from the store's blob dir: %s", appVerDir)
			continue
		}
		err := os.RemoveAll(appVerDir)
		if err != nil {
			return err
		}
		appVerNumb := 0
		appDir := filepath.Join(s.appsRoot, a.Name)
		if err := filepath.Walk(appDir, func(path string, info fs.FileInfo, err error) error {
			if path == appDir {
				return nil
			}
			appVerNumb += 1
			return nil
		}); err != nil {
			return err
		}
		if appVerNumb == 0 {
			if err := os.RemoveAll(appDir); err != nil {
				return err
			}
		}
	}
	if prune {
		_, pruneErr := s.Prune(ctx)
		return pruneErr
	}
	return nil
}

func (s *appStore) Prune(ctx context.Context) ([]string, error) {
	apps, err := s.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	referencedBlobs := map[string]bool{}
	for _, a := range apps {
		app, err := NewAppLoader().LoadAppTree(ctx, s, platforms.OnlyStrict(s.platform), a.String())
		if err != nil {
			return nil, err
		}
		if err := app.Tree().Walk(func(node *compose.TreeNode, depth int) error {
			referencedBlobs[node.Descriptor.Digest.Encoded()] = true
			return nil
		}); err != nil {
			return nil, err
		}
	}
	var blobsToPrune []string
	err = filepath.Walk(s.blobsRoot, func(path string, info fs.FileInfo, err error) error {
		if pathErr, ok := err.(*os.PathError); ok {
			if s.blobsRoot == pathErr.Path {
				// Just exit with empty error since the store is simply empty
				return nil
			}
		}
		if info.IsDir() {
			return nil
		}
		if _, ok := referencedBlobs[info.Name()]; !ok {
			blobsToPrune = append(blobsToPrune, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, p := range blobsToPrune {
		if err := os.RemoveAll(p); err != nil {
			return nil, err
		}
	}
	return blobsToPrune, nil
}

func (s *appStore) GetReadCloser(ctx context.Context, opts ...compose.SecureReadOptions) (io.ReadCloser, error) {
	rc, err := s.bp.GetReadCloser(ctx, opts...)
	if !s.skopeoStoreAware {
		return rc, err
	}
	if !errors.Is(err, errdefs.ErrNotFound) {
		return rc, err
	}
	// if a blob is not found in the blob dir then it may be the skopeo generated store and some of the app blobs
	// are stored in the `apps` directory.
	// The following:
	// 1) checks if the missing blob is one the blobs that skopeo and aklite stores under the apps dir;
	// 2) if the #1 is true then return read closer for the corresponding item under the `apps` dir.
	appRef := compose.GetAppRef(ctx)
	if appRef == nil {
		return rc, err
	}
	blobType := compose.GetBlobType(ctx)
	if len(blobType) == 0 {
		return rc, err
	}
	var blobPath string
	var checkHash bool
	switch blobType {
	case compose.BlobTypeAppManifest:
		{
			blobPath = path.Join(s.appsRoot, appRef.Name, appRef.Digest.Encoded(), "manifest.json")
			checkHash = true
		}
	case compose.BlobTypeAppBundle:
		{
			p := compose.GetSecureReadParams(opts...)
			blobPath = path.Join(s.appsRoot, appRef.Name, appRef.Digest.Encoded(), p.ExpectedDigest.Encoded()+".tgz")
			checkHash = true
		}
	case compose.BlobTypeImageIndex:
		{
			p := compose.GetSecureReadParams(opts...)
			imageRef, parseErr := compose.ParseAppRef(p.Ref)
			if parseErr != nil {
				return nil, parseErr
			}
			blobPath = path.Join(s.appsRoot, appRef.Name, appRef.Digest.Encoded(), "images", imageRef.Spec.Hostname(),
				imageRef.Repo, imageRef.Name, imageRef.Digest.Encoded(), "index.json")
			checkHash = false
		}
	case compose.BlobTypeSkopeoImageIndex:
		{
			p := compose.GetSecureReadParams(opts...)
			imageRef, parseErr := compose.ParseAppRef(p.Ref)
			if parseErr != nil {
				return nil, parseErr
			}
			blobPath = path.Join(s.appsRoot, appRef.Name, appRef.Digest.Encoded(), "images", imageRef.Spec.Hostname(),
				imageRef.Repo, imageRef.Name, imageRef.Digest.Encoded(), "index.json")
			checkHash = false
		}
	default:
		return nil, err
	}

	f, fileOpenErr := os.Open(blobPath)
	if checkHash && fileOpenErr == nil {
		newOpts := opts
		p := compose.GetSecureReadParams(opts...)
		if len(p.ExpectedDigest) == 0 {
			if len(p.Ref) > 0 {
				s, err := reference.Parse(p.Ref)
				if err != nil {
					return nil, err
				}
				p.ExpectedDigest = s.Digest()
				newOpts = append(newOpts, compose.WithExpectedDigest(p.ExpectedDigest))
			} else {
				return nil, fmt.Errorf("missing parameters: either `SecureReadOpts.Ref` or `SecureReadOpts.ExpectedDigest` should be specified")
			}
		}
		return compose.NewSecureReadCloser(f, newOpts...)
	} else {
		return f, fileOpenErr
	}
}

func (s *appStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	return s.bp.Info(ctx, dgst)
}

func MakeAkliteHappy(ctx context.Context, store compose.AppStore, app compose.App, platformMatcher platforms.MatchComparer) error {
	storeV1 := store.(*appStore)
	appV1 := app.(*appCtx)
	appDir := path.Join(storeV1.root, "apps", app.Name(), appV1.Digest.Encoded())
	if err := os.MkdirAll(appDir, 0777); err != nil {
		return err
	}
	blobPath := path.Join(compose.GetBlobsRootFor(storeV1.root), appV1.Digest.Encoded())
	manifestLink := path.Join(appDir, "manifest.json")
	if _, err := os.Stat(manifestLink); err == nil {
		if err := syscall.Unlink(manifestLink); err != nil {
			// TODO: warning
			fmt.Printf("Failed to delete the current app manifest file: %s\n", err.Error())
		}
	}
	if err := syscall.Link(blobPath, manifestLink); err != nil {
		return err
	}

	if err := writeAndSync(path.Join(appDir, "uri"), []byte(appV1.Spec.String())); err != nil {
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
		if err := syscall.Link(path.Join(compose.GetBlobsRootFor(storeV1.root), appBundleDesc.Digest.Encoded()), appBundleLink); err != nil {
			return err
		}
	}
	if b, _, err := readCompose(ctx, storeV1.bp, appV1); err == nil {
		if writeErr := writeAndSync(path.Join(appDir, "docker-compose.yml"), b); writeErr != nil {
			fmt.Printf("Failed to write compose file: %s\n", writeErr.Error())
		}
	} else {
		return err
	}

	appComposeRoot := app.GetComposeRoot()
	for _, imageNode := range appComposeRoot.Children {
		uri, err := reference.Parse(imageNode.Ref())
		if err != nil {
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
		index := ocispec.Index{
			Versioned: specs.Versioned{
				SchemaVersion: 2,
			},
		}
		if imageNode.Type == compose.BlobTypeImageIndex {
			var manifests []ocispec.Descriptor
			for _, im := range imageNode.Children {
				if platformMatcher.Match(*im.Descriptor.Platform) {
					manifests = append(manifests, *im.Descriptor)
				}
			}
			index.Manifests = manifests
		} else {
			index.Manifests = []ocispec.Descriptor{
				*imageNode.Descriptor,
			}
		}
		b, err := json.Marshal(&index)
		if err != nil {
			return err
		}
		if err := writeAndSync(indexFile, b); err != nil {
			return err
		}
		// Set write permission for an owner for each image manifest because
		// `skopeo` rewrites image manifest even if it exists. So, if the permission is set to read-only
		// then `skopeo copy` command will fail.
		for _, m := range index.Manifests {
			if err := os.Chmod(path.Join(storeV1.blobsRoot, m.Digest.Encoded()), 0644); err != nil {
				return err
			}
		}
	}
	return err
}

func writeAndSync(path string, data []byte) error {
	tmpfile := path + ".tmp"
	f, err := os.OpenFile(tmpfile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("failed to create a tmp file: %s; err: %s", tmpfile, err.Error())
	}
	defer os.Remove(tmpfile)
	_, err = f.Write(data)
	if err != nil {
		return fmt.Errorf("failed to write a tmp file: %s; err: %s", tmpfile, err.Error())
	}
	err = f.Sync()
	if err != nil {
		return fmt.Errorf("failed to sync a tmp file: %s; err: %s", tmpfile, err.Error())
	}
	err = f.Close()
	if err != nil {
		return fmt.Errorf("failed to close a tmp file: %s; err: %s", tmpfile, err.Error())
	}
	return os.Rename(tmpfile, path)
}
