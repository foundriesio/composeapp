package compose

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
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
// hardlinked into containerd's content store under a short lease and dropped by containerd's GC
// once the lease is deleted.

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
	// ingestAbortAttempts and ingestAbortDelay bound the retries of a failed ingest abort.
	ingestAbortAttempts = 20
	ingestAbortDelay    = 50 * time.Millisecond
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

// installImagesCtrd installs the images of one app into dockerd's containerd. Under a lease it links
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

// installImage announces every layer of the image, as existing when its snapshot is there and as
// pending otherwise, then puts what the image needs into the content store and unpacks it. If a
// base snapshot went away between the scan and the unpack (for example `docker image prune`
// removed the image that held it), the unpack fails on the missing layer blob; then every layer
// is linked and the unpack is run once more, without announcing the layers again.
func (c *ctrdClient) installImage(ctx context.Context, img *ctrdImage, blobsRoot string,
	platform platforms.MatchComparer, report ProgressCallbackFn) error {
	first, err := c.firstMissingSnapshot(ctx, img.chainIDs)
	if err != nil {
		return fmt.Errorf("failed to scan the snapshots: %w", err)
	}
	reportImageState(report, ImageLoadStateImageLoading, img.ref)
	for i, layer := range img.layers {
		state := ImageLoadStateLayerPending
		if i < first {
			state = ImageLoadStateLayerExist
		}
		reportLayerState(report, img, layer, state, 0, 0)
	}
	if err := c.putImage(ctx, img, blobsRoot, first); err != nil {
		return fmt.Errorf("failed to put the image into containerd: %w", err)
	}
	loaded := map[int]bool{} // layers reported loaded, across both unpack attempts
	err = c.unpackWatched(ctx, img, blobsRoot, platform, first, loaded, report)
	if err == nil {
		return nil
	}
	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("failed to unpack: %w", err)
	}
	if err := c.linkLayers(ctx, img.layers, blobsRoot); err != nil {
		return fmt.Errorf("failed to link every layer into containerd after a snapshot went away: %w", err)
	}
	if err := c.unpackWatched(ctx, img, blobsRoot, platform, 0, loaded, report); err != nil {
		return fmt.Errorf("failed to unpack after linking every layer: %w", err)
	}
	return nil
}

// unpackWatched runs the unpack and, beside it, reports the layers as their snapshots appear:
// the first layer without a snapshot as extracting, and every layer whose snapshot exists as
// loaded, once, with the time it was watched extracting. The report describes what the
// snapshotter shows, not what the differ does: a snapshot that exists is extracted, not durable,
// and a layer done within one poll is reported loaded without having been reported extracting.
func (c *ctrdClient) unpackWatched(ctx context.Context, img *ctrdImage, blobsRoot string, platform platforms.MatchComparer,
	first int, loaded map[int]bool, report ProgressCallbackFn) error {
	if report == nil {
		return c.unpack(ctx, img, platform)
	}
	w := &unpackWatch{c: c, img: img, blobsRoot: blobsRoot, first: first, loaded: loaded, report: report,
		stop: make(chan struct{}), done: make(chan struct{})}
	go w.run(ctx)
	err := c.unpack(ctx, img, platform)
	close(w.stop)
	<-w.done
	return err
}

// unpackWatch measures the extraction of a layer by the free space its snapshot takes from the
// snapshotter's filesystem, against the size of the layer's tar when that is known; the differ's
// reads cannot be observed from outside containerd. The measure is an estimate: small files and
// directories take whole blocks, and anything else written to that filesystem counts too.
type unpackWatch struct {
	c          *ctrdClient
	img        *ctrdImage
	blobsRoot  string
	first      int
	loaded     map[int]bool
	report     ProgressCallbackFn
	stop, done chan struct{}
	extracting int       // index of the layer reported extracting, or -1
	since      time.Time // when it became the first layer without a snapshot
	bar        bool      // it is reported with bytes; off when its tar size or a measure is unknown
	total      int64     // the size of its tar
	freeAt     int64     // free bytes of the snapshotter's filesystem when it became that layer
	written    int64     // the most bytes its snapshot has been seen to take
}

func (w *unpackWatch) run(ctx context.Context) {
	defer close(w.done)
	w.extracting = -1
	// A poll is a stat of the layer being extracted and the free space of the snapshotter root,
	// cheap enough to sample a layer that is extracted within a second a few times.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		w.scan(ctx, false)
		select {
		case <-w.stop:
			w.scan(ctx, true)
			return
		case <-ticker.C:
		}
	}
}

// scan reports the layers whose snapshots appeared since the last scan and, unless this is the
// final scan after the unpack returned, the first layer still without one: with the bytes its
// snapshot has taken so far against its tar size, or with the milliseconds it has been extracting
// when that size is unknown. A loaded layer carries the milliseconds it was watched extracting.
func (w *unpackWatch) scan(ctx context.Context, final bool) {
	sn := w.c.SnapshotService(w.c.snapshotter)
	for i := w.first; i < len(w.img.chainIDs); i++ {
		if w.loaded[i] {
			continue
		}
		_, err := sn.Stat(ctx, w.img.chainIDs[i].String())
		if err == nil {
			w.loaded[i] = true
			var ms int64
			if w.extracting == i {
				ms = time.Since(w.since).Milliseconds()
			}
			reportLayerState(w.report, w.img, w.img.layers[i], ImageLoadStateLayerLoaded, ms, 0)
			continue
		}
		if !errdefs.IsNotFound(err) {
			slog.Debug("unpack progress: failed to stat a snapshot", "chainID", w.img.chainIDs[i], "err", err)
			return
		}
		if final {
			return
		}
		if w.extracting != i {
			w.startLayer(i)
		}
		current, total := w.sample()
		reportLayerState(w.report, w.img, w.img.layers[i], ImageLoadStateLayerSyncing, current, total)
		return
	}
}

// startLayer begins the measure of layer i: its tar size and the free space to count from. The
// bar is off when either is unknown.
func (w *unpackWatch) startLayer(i int) {
	w.extracting, w.since = i, time.Now()
	w.total = uncompressedSize(w.blobsRoot, w.img.layers[i])
	w.written = 0
	free, err := freeBytes(w.c.snapshotterRoot)
	w.bar = w.total > 0 && err == nil
	w.freeAt = free
}

// sample returns what to report for the layer being extracted: the most bytes its snapshot has
// been seen to take, never more than its tar size, with that size; or the milliseconds since it
// became the layer being extracted, with 0, when the size is unknown or a measure failed. A
// failed measure turns the bar off for the rest of the layer.
func (w *unpackWatch) sample() (current, total int64) {
	if w.bar {
		free, err := freeBytes(w.c.snapshotterRoot)
		if err == nil {
			w.written = max(w.written, min(w.freeAt-free, w.total))
			return w.written, w.total
		}
		w.bar = false
	}
	return time.Since(w.since).Milliseconds(), 0
}

// firstMissingSnapshot returns the index of the first layer whose snapshot does not exist; every
// layer after it has to be linked and unpacked too, because a snapshot needs its parent.
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

// putImage puts the layers from first on, the config, the manifest and, when the image is pinned
// by one, the index into the content store. The manifest gets no layer labels: only the lease holds
// the layers. WriteBlob leaves the labels of a blob that already exists as they are.
func (c *ctrdClient) putImage(ctx context.Context, img *ctrdImage, blobsRoot string, first int) error {
	if err := c.linkLayers(ctx, img.layers[first:], blobsRoot); err != nil {
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

// linkLayers puts the layers into the content store without copying them: each store blob is
// hardlinked to the path the content store keeps it at, and then registered in place.
func (c *ctrdClient) linkLayers(ctx context.Context, layers []ocispec.Descriptor, blobsRoot string) error {
	for _, layer := range layers {
		if err := linkLayer(c.contentRoot, blobsRoot, layer); err != nil {
			return err
		}
		if err := c.registerLayer(ctx, blobsRoot, layer); err != nil {
			return err
		}
	}
	return nil
}

// registerLayer records a layer whose file is at the content store's path for it, without reading
// it: the writer the content store opens for such a file owns no ingest and reports the layer's
// size as its offset, and is committed as it is. A writer at any other offset means the file is
// not there (containerd's blob cleanup removes a file it has no record of): its ingest is
// discarded, the layer is linked again and the registration is tried once more; a second miss
// fails the install rather than copy the layer.
func (c *ctrdClient) registerLayer(ctx context.Context, blobsRoot string, desc ocispec.Descriptor) error {
	cs := c.ContentStore()
	ref := "composectl-" + desc.Digest.String()
	for attempt := 1; ; attempt++ {
		w, err := content.OpenWriter(ctx, cs, content.WithRef(ref), content.WithDescriptor(desc))
		if errdefs.IsAlreadyExists(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to open a content writer for layer %s: %w", desc.Digest, err)
		}
		st, err := w.Status()
		if err == nil && st.Offset == desc.Size {
			err = w.Commit(ctx, desc.Size, desc.Digest)
			_ = w.Close()
			if err != nil && !errdefs.IsAlreadyExists(err) {
				return fmt.Errorf("failed to register layer %s: %w", desc.Digest, err)
			}
			return nil
		}
		_ = w.Close()
		if err != nil {
			return fmt.Errorf("failed to read the content writer status of layer %s: %w", desc.Digest, err)
		}
		if err := abortIngest(ctx, cs, ref); err != nil {
			return fmt.Errorf("failed to discard the ingest of layer %s: %w", desc.Digest, err)
		}
		if attempt == 2 {
			return fmt.Errorf("layer %s cannot be registered in containerd's content store without a copy:"+
				" its link is removed before it is recorded", desc.Digest)
		}
		if err := linkLayer(c.contentRoot, blobsRoot, desc); err != nil {
			return err
		}
	}
}

// abortIngest discards the ingest of ref, retrying up to ingestAbortAttempts times, ingestAbortDelay
// apart, while the abort fails: the writer's Close only tells containerd to stop, so its side may
// still be writing the ingest's status when the abort removes the ingest directory, which then
// fails as not empty. A cancelled context ends the retries with its error.
func abortIngest(ctx context.Context, cs content.IngestManager, ref string) error {
	for attempt := 1; ; attempt++ {
		err := cs.Abort(ctx, ref)
		if err == nil || errdefs.IsNotFound(err) {
			return nil
		}
		if attempt == ingestAbortAttempts {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(ingestAbortDelay):
		}
	}
}

// linkFile makes the hardlink of a store blob in the content store; a variable so that tests can
// make it fail.
var linkFile = os.Link

// linkLayer checks that the store blob of a layer is there with the layer's size and hardlinks it
// to the path the content store keeps that blob at. containerd registers a file that is already at
// that path without copying or hashing it (the "shared" content sharing policy), so the write that
// follows moves no bytes; the blob was verified when it was pulled, and the unpack checks every
// extracted layer against the image's diff IDs. A file already at that path is containerd's own
// blob or a link an interrupted install left, and is used as it is; a link that cannot be made
// for any other reason fails the install.
func linkLayer(contentRoot, blobsRoot string, desc ocispec.Descriptor) error {
	src := filepath.Join(blobsRoot, desc.Digest.Encoded())
	if err := checkBlob(src, desc); err != nil {
		return err
	}
	dir := filepath.Join(contentRoot, "blobs", desc.Digest.Algorithm().String())
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create the content store directory of layer %s: %w", desc.Digest, err)
	}
	err := linkFile(src, filepath.Join(dir, desc.Digest.Encoded()))
	switch {
	case err == nil || errors.Is(err, fs.ErrExist):
		return nil
	case errors.Is(err, syscall.EXDEV):
		return fmt.Errorf("cannot link layer %s from %s into containerd's content store %s: both must be on one filesystem",
			desc.Digest, blobsRoot, contentRoot)
	default:
		return fmt.Errorf("failed to link layer %s into containerd's content store: %w", desc.Digest, err)
	}
}

// checkBlob checks that the store blob is there and has the size its descriptor promises.
func checkBlob(path string, desc ocispec.Descriptor) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("failed to find blob %s in the store: %w", desc.Digest, err)
	}
	if fi.Size() != desc.Size {
		return fmt.Errorf("blob %s in the store has %d bytes, expected %d", desc.Digest, fi.Size(), desc.Size)
	}
	return nil
}

// writeBlob writes the store blob of desc to the content store; a blob the content store has
// already is left as it is.
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

// reportLayerState reports a layer of an image; the ID is the compressed layer digest, so that
// one layer keeps one identity in the output, and the size is that of the compressed blob.
func reportLayerState(report ProgressCallbackFn, img *ctrdImage, layer ocispec.Descriptor, state ImageLoadState, current, total int64) {
	if report == nil {
		return
	}
	report(&LoadImageProgress{State: state, ImageID: img.ref, ID: layer.Digest.Encoded()[:12],
		Current: current, Total: total, Size: layer.Size})
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

// uncompressedSize returns the size of a layer's tar, which is what the snapshotter writes when it
// extracts the layer: the blob's size for an uncompressed layer, the size recorded in the last four
// bytes of a gzip layer (modulo 4 GiB), and 0 for any other compression.
func uncompressedSize(blobsRoot string, desc ocispec.Descriptor) int64 {
	const gzipMinSize = 18 // header and trailer of an empty stream
	switch {
	case strings.HasSuffix(desc.MediaType, ".tar"):
		return desc.Size
	case !strings.HasSuffix(desc.MediaType, "gzip") || desc.Size < gzipMinSize:
		return 0
	}
	f, err := os.Open(filepath.Join(blobsRoot, desc.Digest.Encoded()))
	if err != nil {
		return 0
	}
	defer f.Close()
	var trailer [4]byte
	if _, err := f.ReadAt(trailer[:], desc.Size-int64(len(trailer))); err != nil {
		return 0
	}
	return int64(binary.LittleEndian.Uint32(trailer[:]))
}

// freeBytes returns the free space of the filesystem that holds path; a variable so that tests
// can drive the measure.
var freeBytes = func(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
