package compose_test

// Drives the containerd install directly, with its synchronous progress callback, against the
// test dockerd on the containerd image store. Skips when that dockerd or the composectl binary
// (built by `make`) is not there, as under `make test-unit`.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/snapshots"
	"github.com/foundriesio/composeapp/pkg/compose"
	f "github.com/foundriesio/composeapp/test/fixtures"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const derivedAppDef = `
services:
  derived:
    image: %s
    command: sh -c "while true; do sleep 60; done"
`

func ctrdRig(t *testing.T) *compose.CtrdClient {
	t.Helper()
	if _, err := os.Stat(os.Getenv("COMPOSECTL_EXE")); err != nil {
		t.Skip("composectl binary is not built; run `make` first")
	}
	cli, err := compose.GetDockerClient("")
	f.Check(t, err)
	defer cli.Close()
	c, err := compose.ConnectCtrdIfInstallTarget(context.Background(), cli)
	f.Check(t, err)
	if c == nil {
		t.Skip("dockerd does not use the containerd image store")
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestCtrdInstallRetriesAfterBaseSnapshotRemoval: the base snapshot found by the scan is removed
// before the unpack, as `docker image prune` could do, and the install links every layer and
// unpacks again. The removal runs in the install's progress callback, which installImagesCtrd
// calls synchronously after the scan and before the layers are streamed.
func TestCtrdInstallRetriesAfterBaseSnapshotRemoval(t *testing.T) {
	c := ctrdRig(t)
	ctx := context.Background()
	cfg := f.NewTestConfig(t)
	store := compose.NewStoreBlobProvider(cfg.GetBlobsRoot())
	const base = "ghcr.io/foundriesio/busybox:1.36"

	baseApp := f.NewApp(t, fmt.Sprintf(derivedAppDef, base))
	baseApp.Publish(t)
	baseApp.Pull(t)
	defer baseApp.Remove(t)
	baseApp.Install(t)
	defer baseApp.Uninstall(t)
	baseTree, err := cfg.AppLoader.LoadAppTree(ctx, store, platforms.OnlyStrict(cfg.Platform), baseApp.PublishedUri)
	f.Check(t, err)
	baseChains, err := compose.ChainIDsOf(ctx, baseTree, store)
	f.Check(t, err)
	baseChain := baseChains[0]
	baseRef, err := compose.ParseImageRef(baseTree.GetComposeRoot().Children[0].Ref())
	f.Check(t, err)
	baseRefs := []string{baseTree.GetComposeRoot().Children[0].Ref(), baseRef.GetTagRef()}

	derivedApp := f.NewApp(t, fmt.Sprintf(derivedAppDef, f.PublishDerivedImage(t, base, 4)))
	derivedApp.Publish(t)
	derivedApp.Pull(t)
	defer derivedApp.Remove(t)
	derivedTree, err := cfg.AppLoader.LoadAppTree(ctx, store, platforms.OnlyStrict(cfg.Platform), derivedApp.PublishedUri)
	f.Check(t, err)

	sn := c.SnapshotService(compose.CtrdSnapshotter(c))
	var removedAt time.Time
	removeBase := func(p *compose.LoadImageProgress) {
		if p.State != compose.ImageLoadStateImageLoading || !removedAt.IsZero() {
			return
		}
		for _, ref := range baseRefs {
			f.Check(t, c.ImageService().Delete(ctx, ref, images.SynchronousDelete()))
		}
		removedAt = time.Now()
		if _, err := sn.Stat(ctx, baseChain[len(baseChain)-1]); !errdefs.IsNotFound(err) {
			t.Fatalf("the base snapshot is still there after its images were deleted: %v", err)
		}
	}
	f.Check(t, compose.InstallImagesCtrd(ctx, c, derivedTree, cfg, removeBase))
	defer derivedApp.Uninstall(t)
	if removedAt.IsZero() {
		t.Fatal("the progress callback did not run")
	}
	for _, chainID := range baseChain {
		info, err := sn.Stat(ctx, chainID)
		f.Check(t, err)
		if !info.Created.After(removedAt) {
			t.Errorf("base snapshot %s was not recreated by the retry: created %v, removed at %v", chainID, info.Created, removedAt)
		}
	}
	derivedRef := derivedTree.GetComposeRoot().Children[0].Ref()
	out, err := exec.Command("docker", "run", "--rm", "--pull", "never", "--network", "none", derivedRef, "echo", "ok").CombinedOutput()
	f.Checkf(t, err, "docker run %s: %s", derivedRef, out)
}

// linkRig publishes and pulls a one-image app with a fresh layer and returns the app, its tree and
// its layer descriptors.
func linkRig(t *testing.T, cfg *compose.Config) (*f.App, compose.App, []ocispec.Descriptor) {
	t.Helper()
	ctx := context.Background()
	store := compose.NewStoreBlobProvider(cfg.GetBlobsRoot())
	app := f.NewApp(t, fmt.Sprintf(derivedAppDef, f.PublishDerivedImage(t, "ghcr.io/foundriesio/busybox:1.36", 3)))
	app.Publish(t)
	app.Pull(t)
	tree, err := cfg.AppLoader.LoadAppTree(ctx, store, platforms.OnlyStrict(cfg.Platform), app.PublishedUri)
	f.Check(t, err)
	layers, err := compose.LayerDescriptorsOf(ctx, tree, store)
	f.Check(t, err)
	return app, tree, layers[0]
}

// storeCopyIn copies the store's blobs into a new store root under dir and returns a config that
// uses it.
func storeCopyIn(t *testing.T, cfg *compose.Config, dir string) *compose.Config {
	t.Helper()
	root, err := os.MkdirTemp(dir, "composectl-store-")
	f.Check(t, err)
	t.Cleanup(func() { os.RemoveAll(root) })
	copyCfg := *cfg
	copyCfg.StoreRoot = root
	f.Check(t, os.MkdirAll(copyCfg.GetBlobsRoot(), 0700))
	blobs, err := os.ReadDir(cfg.GetBlobsRoot())
	f.Check(t, err)
	for _, b := range blobs {
		data, err := os.ReadFile(filepath.Join(cfg.GetBlobsRoot(), b.Name()))
		f.Check(t, err)
		f.Check(t, os.WriteFile(filepath.Join(copyCfg.GetBlobsRoot(), b.Name()), data, 0600))
	}
	return &copyCfg
}

// layersToRegister returns the layers whose snapshot does not exist yet, from the first one on: the
// install leaves the ones below it alone, so only these reach the content store.
func layersToRegister(t *testing.T, c *compose.CtrdClient, cfg *compose.Config, tree compose.App, layers []ocispec.Descriptor) []ocispec.Descriptor {
	t.Helper()
	ctx := context.Background()
	chains, err := compose.ChainIDsOf(ctx, tree, compose.NewStoreBlobProvider(cfg.GetBlobsRoot()))
	f.Check(t, err)
	sn := c.SnapshotService(compose.CtrdSnapshotter(c))
	for i, chainID := range chains[0] {
		if _, err := sn.Stat(ctx, chainID); errdefs.IsNotFound(err) {
			return layers[i:]
		}
	}
	return nil
}

func deviceOf(t *testing.T, path string) uint64 {
	t.Helper()
	var st syscall.Stat_t
	f.Check(t, syscall.Stat(path, &st))
	return uint64(st.Dev)
}

// deleteImage removes both records the install creates for the app's image, the pinned digest
// reference and the short tag reference, so that containerd's GC drops its snapshots.
func deleteImage(t *testing.T, c *compose.CtrdClient, tree compose.App) {
	t.Helper()
	ref := tree.GetComposeRoot().Children[0].Ref()
	parsed, err := compose.ParseImageRef(ref)
	f.Check(t, err)
	for _, name := range []string{ref, parsed.GetTagRef()} {
		f.Check(t, c.ImageService().Delete(context.Background(), name, images.SynchronousDelete()))
	}
}

func contentBlobPath(c *compose.CtrdClient, d digest.Digest) string {
	return filepath.Join(compose.CtrdContentRoot(c), "blobs", d.Algorithm().String(), d.Encoded())
}

func statNlink(t *testing.T, path string) (os.FileInfo, uint64) {
	t.Helper()
	fi, err := os.Stat(path)
	f.Check(t, err)
	return fi, uint64(fi.Sys().(*syscall.Stat_t).Nlink)
}

// TestCtrdInstallLinksLayers: every layer blob is a second name of the store blob while the install
// lease exists, and the content root holds no copy of it afterwards. One layer is linked before the
// install, as an interrupted install leaves it, and is registered as is.
func TestCtrdInstallLinksLayers(t *testing.T) {
	c := ctrdRig(t)
	cfg := f.NewTestConfig(t)
	app, tree, layers := linkRig(t, cfg)
	defer app.Remove(t)
	layers = layersToRegister(t, c, cfg, tree, layers)
	if len(layers) == 0 {
		t.Fatal("every layer of the fresh image already has a snapshot")
	}
	storePath := func(d digest.Digest) string { return filepath.Join(cfg.GetBlobsRoot(), d.Encoded()) }
	f.Check(t, os.MkdirAll(filepath.Dir(contentBlobPath(c, layers[0].Digest)), 0700))
	f.Check(t, os.Link(storePath(layers[0].Digest), contentBlobPath(c, layers[0].Digest)))

	checked := false
	atLoaded := func(p *compose.LoadImageProgress) {
		if p.State != compose.ImageLoadStateImageLoaded {
			return
		}
		checked = true
		for _, l := range layers {
			src, nlink := statNlink(t, storePath(l.Digest))
			dst, _ := statNlink(t, contentBlobPath(c, l.Digest))
			if nlink != 2 || !os.SameFile(src, dst) {
				t.Errorf("layer %s: nlink %d, same file %v; expected a link into the content root", l.Digest, nlink, os.SameFile(src, dst))
			}
		}
	}
	f.Check(t, compose.InstallImagesCtrd(context.Background(), c, tree, cfg, atLoaded))
	defer deleteImage(t, c, tree)
	if !checked {
		t.Fatal("the image was never reported loaded")
	}
	for _, l := range layers {
		if _, nlink := statNlink(t, storePath(l.Digest)); nlink != 1 {
			t.Errorf("layer %s still has %d names after the lease was deleted", l.Digest, nlink)
		}
		if _, err := os.Stat(contentBlobPath(c, l.Digest)); !errdefs.IsNotFound(err) && !os.IsNotExist(err) {
			t.Errorf("layer %s is still in the content root: %v", l.Digest, err)
		}
	}
}

// installLeasesOf returns the install leases dockerd's containerd holds.
func installLeasesOf(t *testing.T, c *compose.CtrdClient) []leases.Lease {
	t.Helper()
	all, err := c.LeasesService().List(context.Background())
	f.Check(t, err)
	var found []leases.Lease
	for _, l := range all {
		if strings.HasPrefix(l.ID, "composectl/install/") {
			found = append(found, l)
		}
	}
	return found
}

// TestCtrdInstallFailsAcrossFilesystems: with the store on another filesystem the link fails and
// so does the install, naming both directories, without a copy of any layer in the content root;
// the next install from a store on the content root's filesystem goes on under the lease it left
// and removes it at the end.
func TestCtrdInstallFailsAcrossFilesystems(t *testing.T) {
	c := ctrdRig(t)
	ctx := context.Background()
	cfg := f.NewTestConfig(t)
	app, tree, layers := linkRig(t, cfg)
	defer app.Remove(t)
	other := "/dev/shm"
	if _, err := os.Stat(other); err != nil || deviceOf(t, other) == deviceOf(t, compose.CtrdContentRoot(c)) {
		t.Skip("no filesystem other than the content root's is available")
	}
	otherCfg := storeCopyIn(t, cfg, other)
	layers = layersToRegister(t, c, otherCfg, tree, layers)
	if len(layers) == 0 {
		t.Fatal("every layer of the fresh image already has a snapshot")
	}

	err := compose.InstallImagesCtrd(ctx, c, tree, otherCfg, nil)
	if err == nil || !strings.Contains(err.Error(), otherCfg.GetBlobsRoot()) || !strings.Contains(err.Error(), compose.CtrdContentRoot(c)) {
		t.Fatalf("expected the install to fail naming the store and the content root, got %v", err)
	}
	for _, l := range layers {
		if _, err := os.Stat(contentBlobPath(c, l.Digest)); !os.IsNotExist(err) {
			t.Errorf("layer %s is in the content root after the failed install: %v", l.Digest, err)
		}
	}
	if left := installLeasesOf(t, c); len(left) != 1 {
		t.Fatalf("expected the failed install to leave its lease, found %+v", left)
	}
	f.Check(t, compose.InstallImagesCtrd(ctx, c, tree, cfg, nil))
	defer deleteImage(t, c, tree)
	if left := installLeasesOf(t, c); len(left) != 0 {
		t.Fatalf("expected the next install to remove the lease, found %+v", left)
	}
}

// TestCtrdInstallResumesAfterInterruption: an install cancelled in the middle of a layer's
// extraction leaves its lease, marked as synced, holding the committed snapshots, the unfinished
// one and no image record. The next install of the same app goes on under that lease: it drops
// the unfinished snapshot before anything else, loads only the layers without a snapshot, removes
// the lease at the end, and leaves the image as a clean install does. A lease left by another
// app's install is removed first.
func TestCtrdInstallResumesAfterInterruption(t *testing.T) {
	c := ctrdRig(t)
	ctx := context.Background()
	cfg := f.NewTestConfig(t)
	store := compose.NewStoreBlobProvider(cfg.GetBlobsRoot())
	// A small layer under one of many files, whose extraction outlasts several polls of the
	// install's watcher: a poll finds the small layer's snapshot while the last layer is still
	// being extracted. A layer of a few MB is written within one poll on a fast disk.
	pollInterval := *compose.UnpackPollInterval
	*compose.UnpackPollInterval = 50 * time.Millisecond
	t.Cleanup(func() { *compose.UnpackPollInterval = pollInterval })
	image := f.PublishDerivedImageOfFiles(t, f.PublishDerivedImage(t, "ghcr.io/foundriesio/busybox:1.36", 1), 5000)
	app := f.NewApp(t, fmt.Sprintf(derivedAppDef, image))
	app.Publish(t)
	app.Pull(t)
	defer app.Remove(t)
	tree, err := cfg.AppLoader.LoadAppTree(ctx, store, platforms.OnlyStrict(cfg.Platform), app.PublishedUri)
	f.Check(t, err)
	layers, err := compose.LayerDescriptorsOf(ctx, tree, store)
	f.Check(t, err)
	toRegister := layersToRegister(t, c, cfg, tree, layers[0])
	if len(toRegister) == 0 {
		t.Fatal("every layer of the fresh image already has a snapshot")
	}
	chains, err := compose.ChainIDsOf(ctx, tree, store)
	f.Check(t, err)
	chain := chains[0]
	missing := chain[len(chain)-len(toRegister):] // the chain IDs of toRegister
	lastLayer, lastChainID := layers[0][len(layers[0])-1], chain[len(chain)-1]
	ref := tree.GetComposeRoot().Children[0].Ref()
	sn := c.SnapshotService(compose.CtrdSnapshotter(c))

	cutCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// A layer is reported syncing only while the unpack runs and its snapshot is the first
	// one missing; with bytes taken from the filesystem, its extraction is in progress.
	midExtraction := func(p *compose.LoadImageProgress) {
		if p.State == compose.ImageLoadStateLayerSyncing && p.ID == lastLayer.Digest.Encoded()[:12] && p.Current > 0 {
			cancel()
		}
	}
	if err := compose.InstallImagesCtrd(cutCtx, c, tree, cfg, midExtraction); err == nil || cutCtx.Err() == nil {
		t.Fatalf("expected the install to fail once cancelled in the extraction of the last layer, got %v", err)
	}
	left := installLeasesOf(t, c)
	if len(left) != 1 || !strings.HasPrefix(left[0].ID, "composectl/install/"+tree.Name()+"@") ||
		left[0].Labels[compose.InstallLeaseSyncedLabel] != "true" {
		t.Fatalf("expected the lease of the interrupted install, marked as synced, found %+v", left)
	}
	if _, err := c.ImageService().Get(ctx, ref); !errdefs.IsNotFound(err) {
		t.Fatalf("expected no record of the image after the interrupted install, got %v", err)
	}
	committed := 0
	for _, chainID := range missing {
		if info, err := sn.Stat(ctx, chainID); err == nil && info.Kind == snapshots.KindCommitted {
			committed++
		}
	}
	if committed == 0 || committed == len(missing) {
		t.Fatalf("the interrupted install committed %d of %d snapshots; expected some but not all", committed, len(missing))
	}
	t.Logf("the interrupted install committed %d of %d snapshots", committed, len(missing))
	unfinished := activeExtractSnapshot(t, sn, lastChainID)
	if unfinished == "" {
		t.Fatalf("expected the unfinished extraction of %s to leave an active snapshot", lastChainID)
	}
	other, err := c.LeasesService().Create(ctx, leases.WithID("composectl/install/other@"+digest.FromString("other").String()),
		leases.WithLabels(map[string]string{compose.InstallLeaseSyncedLabel: "true"}))
	f.Check(t, err)
	defer func() { _ = c.LeasesService().Delete(ctx, other) }()

	var events []compose.LoadImageProgress
	dropped := false
	atEvent := func(p *compose.LoadImageProgress) {
		events = append(events, *p)
		if len(events) > 1 || p.State != compose.ImageLoadStateImageLoading {
			return
		}
		// The first event comes before any layer is linked or unpacked.
		if _, err := sn.Stat(ctx, unfinished); !errdefs.IsNotFound(err) {
			t.Errorf("the unfinished snapshot %s is still there when the resumed install starts: %v", unfinished, err)
		}
		dropped = true
	}
	f.Check(t, compose.InstallImagesCtrd(ctx, c, tree, cfg, atEvent))
	defer deleteImage(t, c, tree)
	if !dropped {
		t.Error("the resumed install did not report the image loading first")
	}
	loaded := 0
	for _, e := range events {
		if e.State == compose.ImageLoadStateLayerLoaded {
			loaded++
		}
	}
	if loaded != len(missing)-committed {
		t.Errorf("expected %d layers loaded by the resumed install, %d skipped, got %d loaded", len(missing)-committed, committed, loaded)
	}
	if left := installLeasesOf(t, c); len(left) != 0 {
		t.Errorf("expected the resumed install to remove every install lease, found %+v", left)
	}
	for _, chainID := range chain {
		if info, err := sn.Stat(ctx, chainID); err != nil || info.Kind != snapshots.KindCommitted {
			t.Errorf("snapshot %s after the resumed install: %v, %v", chainID, info.Kind, err)
		}
	}
	if leftover := activeExtractSnapshot(t, sn, lastChainID); leftover != "" {
		t.Errorf("leftover snapshot %s after the resumed install", leftover)
	}
	out, err := exec.Command("docker", "run", "--rm", "--pull", "never", "--network", "none", ref, "echo", "ok").CombinedOutput()
	f.Checkf(t, err, "docker run %s: %s", ref, out)
}

// activeExtractSnapshot returns the key of the active "extract-<id> <chain ID>" snapshot of an
// unpack that did not commit the layer with chainID, or "" when there is none.
func activeExtractSnapshot(t *testing.T, sn snapshots.Snapshotter, chainID string) string {
	t.Helper()
	var key string
	f.Check(t, sn.Walk(context.Background(), func(_ context.Context, info snapshots.Info) error {
		if info.Kind == snapshots.KindActive && strings.HasSuffix(info.Name, " "+chainID) {
			key = info.Name
		}
		return nil
	}))
	return key
}

// TestCtrdLinkedBlobAndGC: containerd's blob-directory cleanup runs after a GC that removed content.
// A linked file it finds before the writer is opened is removed, and the registration links it again
// rather than copy it; once the writer is open its expected digest keeps the file; after the commit
// the record keeps it.
func TestCtrdLinkedBlobAndGC(t *testing.T) {
	c := ctrdRig(t)
	ctx := context.Background()
	cfg := f.NewTestConfig(t)
	app, _, layers := linkRig(t, cfg)
	defer app.Remove(t)
	cs, ls := c.ContentStore(), c.LeasesService()
	layer := layers[len(layers)-1]
	src := filepath.Join(cfg.GetBlobsRoot(), layer.Digest.Encoded())
	dst := contentBlobPath(c, layer.Digest)
	ref := "composectl-" + layer.Digest.String()
	f.Check(t, os.MkdirAll(filepath.Dir(dst), 0700))

	// forceGC commits a throwaway blob under its own lease and deletes that lease, which removes
	// the blob's record and so triggers the blob-directory cleanup.
	forceGC := func() {
		lease, err := ls.Create(ctx, leases.WithRandomID())
		f.Check(t, err)
		data := []byte(fmt.Sprintf("throwaway %d", time.Now().UnixNano()))
		f.Check(t, content.WriteBlob(leases.WithLease(ctx, lease.ID), cs, "throwaway", bytes.NewReader(data),
			ocispec.Descriptor{Digest: digest.FromBytes(data), Size: int64(len(data))}))
		f.Check(t, ls.Delete(ctx, lease, leases.SynchronousDelete))
	}
	waitGone := func(path string) bool {
		for i := 0; i < 50; i++ {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return true
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	}
	lease, err := ls.Create(ctx, leases.WithRandomID())
	f.Check(t, err)
	defer func() { _ = ls.Delete(ctx, lease, leases.SynchronousDelete) }()
	lctx := leases.WithLease(ctx, lease.ID)

	// (a) cleanup before the writer: the link goes; the registration finds an ordinary writer,
	// discards its ingest, links again and registers the link, copying nothing.
	f.Check(t, os.Link(src, dst))
	forceGC()
	if !waitGone(dst) {
		t.Fatal("cleanup did not remove the unreferenced link")
	}
	time.Sleep(time.Second) // the cleanup pass that removed the link may still be walking the directory
	f.Check(t, compose.RegisterLayer(lctx, c, cfg.GetBlobsRoot(), layer))
	srcInfo, _ := statNlink(t, src)
	if dstInfo, nlink := statNlink(t, dst); nlink != 2 || !os.SameFile(srcInfo, dstInfo) {
		t.Fatalf("expected the layer linked again after cleanup removed the link, nlink %d same file %v", nlink, os.SameFile(srcInfo, dstInfo))
	}
	if _, err := cs.Info(ctx, layer.Digest); err != nil {
		t.Fatalf("the relinked blob has no record: %v", err)
	}
	if statuses, err := cs.ListStatuses(ctx, "ref=="+ref); err != nil || len(statuses) != 0 {
		t.Fatalf("expected no ingest left behind, got %v, %v", statuses, err)
	}
	f.Check(t, cs.Delete(ctx, layer.Digest))
	forceGC()
	if !waitGone(dst) {
		t.Fatal("the deleted blob was not cleaned up")
	}

	// (b) cleanup between the writer and the commit: the expected digest keeps the file.
	f.Check(t, os.Link(src, dst))
	w, err := content.OpenWriter(lctx, cs, content.WithRef("composectl-"+layer.Digest.String()), content.WithDescriptor(layer))
	f.Check(t, err)
	st, err := w.Status()
	f.Check(t, err)
	if st.Offset != layer.Size {
		t.Fatalf("expected a shared writer at offset %d, got %d", layer.Size, st.Offset)
	}
	forceGC()
	time.Sleep(time.Second)
	if _, nlink := statNlink(t, dst); nlink != 2 {
		t.Fatalf("the linked blob of an open writer was removed, nlink %d", nlink)
	}
	f.Check(t, w.Commit(lctx, layer.Size, layer.Digest))
	w.Close()

	// (c) cleanup after the commit: the record keeps the file.
	forceGC()
	time.Sleep(time.Second)
	if _, nlink := statNlink(t, dst); nlink != 2 {
		t.Fatalf("the committed linked blob was removed, nlink %d", nlink)
	}
	if _, err := cs.Info(ctx, layer.Digest); err != nil {
		t.Fatalf("the linked blob has no record: %v", err)
	}
}

// checkLayerEvents verifies the per-image event order: the image loading first, then for each
// registered layer one pending, the extraction samples, if any, and one loaded, the image loaded
// last. No layer is reported as existing: every one is linked. A sample with a known total counts
// bytes against the size of the layer's tar; one without counts milliseconds. Every layer event
// carries the size of the layer's blob.
func checkLayerEvents(t *testing.T, events []compose.LoadImageProgress, layers []ocispec.Descriptor, blobsRoot string) {
	t.Helper()
	if len(events) < 2 || events[0].State != compose.ImageLoadStateImageLoading || events[len(events)-1].State != compose.ImageLoadStateImageLoaded {
		t.Fatalf("expected image loading first and image loaded last, got %+v", events)
	}
	for _, l := range layers {
		id := l.Digest.Encoded()[:12]
		tarSize := compose.UncompressedSize(blobsRoot, l)
		pendingAt, loadedAt, lastSample := -1, -1, -1
		for i, e := range events {
			if e.ID != id {
				continue
			}
			if e.Size != l.Size {
				t.Errorf("layer %s: event %s carries size %d, the blob is %d", id, e.State, e.Size, l.Size)
			}
			switch e.State {
			case compose.ImageLoadStateLayerPending:
				if pendingAt >= 0 || lastSample >= 0 || loadedAt >= 0 {
					t.Errorf("layer %s: pending at %d after pending %d, sample %d, loaded %d", id, i, pendingAt, lastSample, loadedAt)
				}
				pendingAt = i
			case compose.ImageLoadStateLayerSyncing:
				lastSample = i
				if e.Total != tarSize || e.Current < 0 || e.Total > 0 && e.Current > e.Total {
					t.Errorf("layer %s: sample %d/%d, tar size %d", id, e.Current, e.Total, tarSize)
				}
			case compose.ImageLoadStateLayerLoaded:
				if loadedAt >= 0 {
					t.Errorf("layer %s reported loaded twice", id)
				}
				loadedAt = i
				if e.Current < 0 || e.Total != 0 || lastSample < 0 && e.Current != 0 {
					t.Errorf("layer %s: loaded with %d ms after %d samples", id, e.Current, lastSample+1)
				}
			default:
				t.Errorf("layer %s: unexpected event %+v", id, e)
			}
		}
		if loadedAt < 0 || loadedAt < lastSample {
			t.Errorf("layer %s: loaded at %d, last extraction sample at %d", id, loadedAt, lastSample)
		}
		if pendingAt < 0 {
			t.Errorf("layer %s: never reported pending", id)
		}
	}
}

func collectEvents(events *[]compose.LoadImageProgress) compose.ProgressCallbackFn {
	return func(p *compose.LoadImageProgress) { *events = append(*events, *p) }
}

// TestCtrdInstallReportsLayers: the install reports the extraction of every layer.
func TestCtrdInstallReportsLayers(t *testing.T) {
	c := ctrdRig(t)
	cfg := f.NewTestConfig(t)
	app, tree, layers := linkRig(t, cfg)
	defer app.Remove(t)
	toRegister := layersToRegister(t, c, cfg, tree, layers)
	if len(toRegister) == 0 {
		t.Fatal("every layer of the fresh image already has a snapshot")
	}
	var events []compose.LoadImageProgress
	f.Check(t, compose.InstallImagesCtrd(context.Background(), c, tree, cfg, collectEvents(&events)))
	defer deleteImage(t, c, tree)
	checkLayerEvents(t, events, toRegister, cfg.GetBlobsRoot())
}
