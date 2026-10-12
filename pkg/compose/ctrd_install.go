package compose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/platforms"
	dockerclient "github.com/docker/docker/client"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sys/unix"
)

// Install of app images straight into the containerd behind dockerd: the compressed layers are
// streamed under a short lease and dropped by containerd's GC once the lease is deleted.

const (
	// MinDockerVersionForCtrdInstall is the oldest dockerd on which `docker image ls` lists an image
	// whose compressed layers are not in containerd; `ps` and `check --install` read that list.
	MinDockerVersionForCtrdInstall = "29.5.2"

	installLeasePrefix  = "composectl/install/"
	installLockFileName = ".install.lock"
	contentPluginType   = "io.containerd.content.v1"
	gcRefConfigLabel    = "containerd.io/gc.ref.content.config"
	gcRefManifestLabel  = "containerd.io/gc.ref.content.m.0"
	// recordCleanupTimeout bounds the removal of partial image records after a failed write.
	recordCleanupTimeout = 30 * time.Second
)

type (
	// ctrdClient is a connection to the containerd behind dockerd.
	ctrdClient struct {
		*containerd.Client
		snapshotter     string
		contentRoot     string
		snapshotterRoot string
	}

	// ctrdImage is one app image as the containerd install needs it.
	ctrdImage struct {
		ref      string             // the pinned digest reference the app uses
		tagRef   string             // the short tag reference that `ps` and status read
		root     ocispec.Descriptor // what ref points at: the platform manifest or an index
		manifest ocispec.Descriptor // the platform manifest selected when the app tree was loaded
		config   ocispec.Descriptor
		layers   []ocispec.Descriptor
		chainIDs []digest.Digest // chain ID of the snapshot of each layer
	}
)

// LockInstall takes the lock that serializes every operation that writes to or deletes from
// dockerd's containerd: install, uninstall, rm and prune. It blocks until a concurrent composectl
// releases it.
func LockInstall(storeRoot string) (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(storeRoot, installLockFileName), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open the install lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("failed to take the install lock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// PruneInstallLeases deletes the leases that interrupted installs left in dockerd's containerd,
// and with them the blobs and snapshots that only they hold. Call it under the install lock.
// It does nothing on a daemon whose images are loaded with `docker load`.
func PruneInstallLeases(ctx context.Context, cfg *Config) error {
	c, err := ctrdIfInstallTarget(ctx, cfg)
	if err != nil || c == nil {
		return err
	}
	defer c.Close()
	if err := deleteInstallLeases(ctx, c.LeasesService()); err != nil {
		return fmt.Errorf("failed to prune install leases: %w", err)
	}
	return nil
}

// connectCtrdIfInstallTarget returns a client to dockerd's containerd when app images are installed
// into it directly, or nil when they are loaded with `docker load`: a graphdriver daemon, a daemon
// older than MinDockerVersionForCtrdInstall or one that is not on a local unix socket.
func connectCtrdIfInstallTarget(ctx context.Context, cli *dockerclient.Client) (*ctrdClient, error) {
	info, err := GetDockerInfo(ctx, cli)
	if err != nil {
		return nil, fmt.Errorf("failed to discover the docker image store: %w", err)
	}
	if !isCtrdInstallTarget(info, cli.DaemonHost()) {
		return nil, nil
	}
	return newCtrdClient(ctx, info)
}

// ctrdIfInstallTarget is connectCtrdIfInstallTarget for the operations that worked without dockerd
// before (the storage check, prune): a dockerd that cannot be reached is treated like a `docker load` one.
func ctrdIfInstallTarget(ctx context.Context, cfg *Config) (*ctrdClient, error) {
	cli, err := GetDockerClient(cfg.DockerHost)
	if err != nil {
		return nil, nil
	}
	defer cli.Close()
	info, err := GetDockerInfo(ctx, cli)
	if err != nil || !isCtrdInstallTarget(info, cli.DaemonHost()) {
		return nil, nil
	}
	c, err := newCtrdClient(ctx, info)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to dockerd's containerd: %w", err)
	}
	return c, nil
}

func isCtrdInstallTarget(info *DockerInfo, dockerHost string) bool {
	return info.Snapshotter() != "" &&
		info.Containerd != nil && len(info.Containerd.Address) > 0 &&
		isLocalDockerHost(dockerHost) &&
		versionAtLeast(info.ServerVersion, MinDockerVersionForCtrdInstall)
}

func newCtrdClient(ctx context.Context, info *DockerInfo) (*ctrdClient, error) {
	c, err := containerd.New(info.Containerd.Address,
		containerd.WithDefaultNamespace(info.Containerd.Namespaces.Containers))
	if err != nil {
		return nil, fmt.Errorf("dockerd %s uses the containerd image store, but composectl cannot connect"+
			" to its containerd at %s (the socket is usually writable by root only): %w",
			info.ServerVersion, info.Containerd.Address, err)
	}
	cc := &ctrdClient{Client: c, snapshotter: info.Snapshotter()}
	if cc.contentRoot, err = pluginRoot(ctx, c, contentPluginType, "content"); err == nil {
		cc.snapshotterRoot, err = pluginRoot(ctx, c, snapshotterPluginType, cc.snapshotter)
	}
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	return cc, nil
}

func pluginRoot(ctx context.Context, c *containerd.Client, pluginType, id string) (string, error) {
	resp, err := c.IntrospectionService().Plugins(ctx, []string{"type==" + pluginType + ",id==" + id})
	if err != nil {
		return "", fmt.Errorf("failed to introspect containerd plugin %s.%s: %w", pluginType, id, err)
	}
	if len(resp.Plugins) != 1 || resp.Plugins[0].Exports["root"] == "" {
		return "", fmt.Errorf("containerd plugin %s.%s does not export its root", pluginType, id)
	}
	return resp.Plugins[0].Exports["root"], nil
}

// installImagesCtrd installs the images of one app into dockerd's containerd. Under a lease it streams
// the layers that have no snapshot yet, unpacks every image and syncs the snapshotter's filesystem,
// and only then creates the image records: a record never points at a snapshot that a power cut
// can still lose. Deleting the lease lets containerd's GC drop the compressed layers.
func installImagesCtrd(ctx context.Context, c *ctrdClient, app App, cfg *Config, report ProgressCallbackFn) error {
	blobsRoot := cfg.GetBlobsRoot()
	imgs, err := ctrdImagesOf(ctx, app, NewStoreBlobProvider(blobsRoot))
	if err != nil {
		return fmt.Errorf("failed to read the app images from the store: %w", err)
	}
	ls := c.LeasesService()
	if err := deleteInstallLeases(ctx, ls); err != nil {
		return fmt.Errorf("failed to remove leases of interrupted installs: %w", err)
	}
	lease, err := ls.Create(ctx, leases.WithID(installLeaseID(app.Name(), app.Ref().Digest)))
	if err != nil {
		return fmt.Errorf("failed to create the install lease: %w", err)
	}
	lctx := leases.WithLease(ctx, lease.ID)
	platform := platforms.OnlyStrict(cfg.Platform)
	for _, img := range imgs {
		if err := c.installImage(lctx, img, blobsRoot, platform, report); err != nil {
			return fmt.Errorf("failed to install image %s: %w", img.ref, err)
		}
	}
	if err := syncFs(c.snapshotterRoot); err != nil {
		return fmt.Errorf("failed to sync the snapshots of app %s: %w", app.Name(), err)
	}
	for _, img := range imgs {
		if err := putImageRecords(ctx, c.ImageService(), img); err != nil {
			return fmt.Errorf("failed to record image %s: %w", img.ref, err)
		}
		reportImageState(report, ImageLoadStateImageLoaded, img.ref)
	}
	if err := ls.Delete(ctx, lease, leases.SynchronousDelete); err != nil {
		return fmt.Errorf("failed to delete the install lease: %w", err)
	}
	return nil
}

func installLeaseID(appName string, appDigest digest.Digest) string {
	return installLeasePrefix + appName + "@" + appDigest.String()
}

func installLeases(all []leases.Lease) []leases.Lease {
	var found []leases.Lease
	for _, l := range all {
		if strings.HasPrefix(l.ID, installLeasePrefix) {
			found = append(found, l)
		}
	}
	return found
}

// deleteInstallLeases removes the leases that interrupted installs left behind. Under the install
// lock no other install is running, so every install lease is such a leftover.
func deleteInstallLeases(ctx context.Context, ls leases.Manager) error {
	all, err := ls.List(ctx)
	if err != nil {
		return fmt.Errorf("failed to list containerd leases: %w", err)
	}
	for _, l := range installLeases(all) {
		if err := ls.Delete(ctx, l, leases.SynchronousDelete); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("failed to delete the install lease %s: %w", l.ID, err)
		}
	}
	return nil
}

// installImage streams what image needs and unpacks it. If a base snapshot went away between the
// scan and the unpack (for example `docker image prune` removed the image that held it), the unpack
// fails on the missing layer blob; then every layer is streamed and the unpack is run once more.
func (c *ctrdClient) installImage(ctx context.Context, img *ctrdImage, blobsRoot string,
	platform platforms.MatchComparer, report ProgressCallbackFn) error {
	first, err := c.firstMissingSnapshot(ctx, img.chainIDs)
	if err != nil {
		return fmt.Errorf("failed to scan the snapshots: %w", err)
	}
	reportImageState(report, ImageLoadStateImageLoading, img.ref)
	if err := c.streamImage(ctx, img, blobsRoot, first); err != nil {
		return fmt.Errorf("failed to stream the image to containerd: %w", err)
	}
	err = c.unpack(ctx, img, platform)
	if err == nil {
		return nil
	}
	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("failed to unpack: %w", err)
	}
	if err := c.streamLayers(ctx, img.layers, blobsRoot); err != nil {
		return fmt.Errorf("failed to stream every layer to containerd after a snapshot went away: %w", err)
	}
	if err := c.unpack(ctx, img, platform); err != nil {
		return fmt.Errorf("failed to unpack after streaming every layer: %w", err)
	}
	return nil
}

// firstMissingSnapshot returns the index of the first layer whose snapshot does not exist; every
// layer after it has to be streamed and unpacked too, because a snapshot needs its parent.
func (c *ctrdClient) firstMissingSnapshot(ctx context.Context, chainIDs []digest.Digest) (int, error) {
	sn := c.SnapshotService(c.snapshotter)
	for i, chainID := range chainIDs {
		_, err := sn.Stat(ctx, chainID.String())
		if errdefs.IsNotFound(err) {
			return i, nil
		}
		if err != nil {
			return 0, fmt.Errorf("failed to stat snapshot %s: %w", chainID, err)
		}
	}
	return len(chainIDs), nil
}

// streamImage writes the layers from first on, the config, the manifest and, when the image is
// pinned by one, the index. The manifest gets no layer labels: only the lease holds the layers.
// WriteBlob leaves the labels of a blob that already exists as they are.
func (c *ctrdClient) streamImage(ctx context.Context, img *ctrdImage, blobsRoot string, first int) error {
	if err := c.streamLayers(ctx, img.layers[first:], blobsRoot); err != nil {
		return err
	}
	if err := c.writeBlob(ctx, blobsRoot, img.config, nil); err != nil {
		return err
	}
	manifestLabels := map[string]string{gcRefConfigLabel: img.config.Digest.String()}
	if err := c.writeBlob(ctx, blobsRoot, img.manifest, manifestLabels); err != nil {
		return err
	}
	if img.root.Digest == img.manifest.Digest {
		return nil
	}
	err := c.writeBlob(ctx, blobsRoot, img.root, map[string]string{gcRefManifestLabel: img.manifest.Digest.String()})
	if errors.Is(err, os.ErrNotExist) {
		// The app was pulled by skopeo: the store has the image's platform manifest but not the index
		// the app pins it by. The image records point at the manifest then, as `docker load` does.
		img.root = img.manifest
		return nil
	}
	return err
}

func (c *ctrdClient) streamLayers(ctx context.Context, layers []ocispec.Descriptor, blobsRoot string) error {
	for _, layer := range layers {
		if err := c.writeBlob(ctx, blobsRoot, layer, nil); err != nil {
			return err
		}
	}
	return nil
}

func (c *ctrdClient) writeBlob(ctx context.Context, blobsRoot string, desc ocispec.Descriptor, labels map[string]string) error {
	f, err := os.Open(filepath.Join(blobsRoot, desc.Digest.Encoded()))
	if err != nil {
		return fmt.Errorf("failed to open blob: %w", err)
	}
	defer f.Close()
	var opts []content.Opt
	if labels != nil {
		opts = append(opts, content.WithLabels(labels))
	}
	if err := content.WriteBlob(ctx, c.ContentStore(), "composectl-"+desc.Digest.String(), f, desc, opts...); err != nil {
		return fmt.Errorf("failed to write blob %s to containerd: %w", desc.Digest, err)
	}
	return nil
}

// unpack creates the snapshots of the image's layers. The target is the platform manifest the app
// tree selected, not the index: on arm64 the default platform matcher would also accept arm/v7.
func (c *ctrdClient) unpack(ctx context.Context, img *ctrdImage, platform platforms.MatchComparer) error {
	ci := containerd.NewImageWithPlatform(c.Client, images.Image{Name: img.ref, Target: img.manifest}, platform)
	return ci.Unpack(ctx, c.snapshotter)
}

// putImageRecords creates the pinned digest reference and the short tag reference, or points the
// existing records at the image, so that a retried install is idempotent. If the second record
// fails, the records this call created are deleted again (best effort), so that an image is
// either fully recorded and reported as loaded, or absent and still held only by the lease.
func putImageRecords(ctx context.Context, is images.Store, img *ctrdImage) error {
	var created []string
	for _, name := range []string{img.ref, img.tagRef} {
		rec := images.Image{Name: name, Target: img.root}
		_, err := is.Create(ctx, rec)
		if err == nil {
			created = append(created, name)
			continue
		}
		if errdefs.IsAlreadyExists(err) {
			_, err = is.Update(ctx, rec, "target")
		}
		if err != nil {
			deleteImageRecords(ctx, is, created)
			return fmt.Errorf("failed to create image record %s: %w", name, err)
		}
	}
	return nil
}

// deleteImageRecords removes the records a failed putImageRecords created. The failure may be the
// cancellation of ctx itself, so the deletes run on a context that is not cancelled, with a bound.
func deleteImageRecords(ctx context.Context, is images.Store, names []string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordCleanupTimeout)
	defer cancel()
	for _, name := range names {
		_ = is.Delete(cleanupCtx, name)
	}
}

// syncFs flushes the filesystem that holds path. A power cut after the image records are created
// must not lose the snapshots they point at.
func syncFs(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", path, err)
	}
	defer unix.Close(fd)
	if err := unix.Syncfs(fd); err != nil {
		return fmt.Errorf("failed to sync the filesystem of %s: %w", path, err)
	}
	return nil
}

func reportImageState(report ProgressCallbackFn, state ImageLoadState, imageID string) {
	if report != nil {
		report(&LoadImageProgress{State: state, ImageID: imageID})
	}
}

// ctrdImagesOf describes each image of the app as the containerd install and the storage precheck
// need it. The app tree already holds the platform manifest of each image.
func ctrdImagesOf(ctx context.Context, app App, provider BlobProvider) ([]*ctrdImage, error) {
	var imgs []*ctrdImage
	for _, root := range app.GetComposeRoot().Children {
		img, err := ctrdImageOf(ctx, root, provider)
		if err != nil {
			return nil, fmt.Errorf("failed to read image %s: %w", root.Ref(), err)
		}
		imgs = append(imgs, img)
	}
	return imgs, nil
}

func ctrdImageOf(ctx context.Context, root *TreeNode, provider BlobProvider) (*ctrdImage, error) {
	ref, err := ParseImageRef(root.Ref())
	if err != nil {
		return nil, fmt.Errorf("failed to parse the image reference: %w", err)
	}
	mNode, err := platformManifestNode(root)
	if err != nil {
		return nil, fmt.Errorf("failed to find the platform manifest: %w", err)
	}
	manifest, err := ReadImageManifest(ctx, provider, mNode.Ref(), mNode.Descriptor.Size)
	if err != nil {
		return nil, fmt.Errorf("failed to read image manifest: %w", err)
	}
	cfgNode := childOfType(mNode, BlobTypeImageConfig)
	if cfgNode == nil {
		return nil, fmt.Errorf("image config is not found")
	}
	b, err := ReadBlob(ctx, provider, cfgNode.Ref(), cfgNode.Descriptor.Digest, cfgNode.Descriptor.Size)
	if err != nil {
		return nil, fmt.Errorf("failed to read image config: %w", err)
	}
	var imageConfig ocispec.Image
	if err := json.Unmarshal(b, &imageConfig); err != nil {
		return nil, fmt.Errorf("failed to unmarshal image config: %w", err)
	}
	if len(imageConfig.RootFS.DiffIDs) != len(manifest.Layers) {
		return nil, fmt.Errorf("image config has %d diff IDs for %d layers",
			len(imageConfig.RootFS.DiffIDs), len(manifest.Layers))
	}
	return &ctrdImage{
		ref:      root.Ref(),
		tagRef:   ref.GetTagRef(),
		root:     plainDescriptor(root.Descriptor),
		manifest: plainDescriptor(mNode.Descriptor),
		config:   plainDescriptor(&manifest.Config),
		layers:   manifest.Layers,
		chainIDs: identity.ChainIDs(imageConfig.RootFS.DiffIDs),
	}, nil
}

// platformManifestNode walks from the image root down to its platform manifest.
func platformManifestNode(root *TreeNode) (*TreeNode, error) {
	node := root
	for node.Type != BlobTypeImageManifest {
		if node.Type != BlobTypeImageIndex && node.Type != BlobTypeSkopeoImageIndex {
			return nil, fmt.Errorf("invalid image type: %s", node.Type)
		}
		if len(node.Children) != 1 {
			return nil, fmt.Errorf("the image index has %d manifests for the platform, expected one", len(node.Children))
		}
		node = node.Children[0]
	}
	return node, nil
}

func childOfType(node *TreeNode, blobType BlobType) *TreeNode {
	for _, child := range node.Children {
		if child.Type == blobType {
			return child
		}
	}
	return nil
}

// plainDescriptor drops what the app tree adds to a descriptor (composectl's references in URLs,
// the index's platform), keeping what containerd records and content need.
func plainDescriptor(desc *ocispec.Descriptor) ocispec.Descriptor {
	return ocispec.Descriptor{MediaType: desc.MediaType, Digest: desc.Digest, Size: desc.Size}
}
