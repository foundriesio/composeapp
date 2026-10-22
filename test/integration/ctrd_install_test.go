package e2e_tests

// Tests of the install of app images into dockerd's containerd, see
// docs/containerd-install-plan-2026-10-08.md. They need the dockerd on the containerd image store
// (CONTAINERD_SNAPSHOTTER=true) and skip on the graphdriver one.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/snapshots"
	"github.com/docker/docker/api/types/filters"
	composectl "github.com/foundriesio/composeapp/cmd/composectl/cmd"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/foundriesio/composeapp/pkg/update"
	f "github.com/foundriesio/composeapp/test/fixtures"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	ctrdTestApp = `
services:
  base:
    image: ghcr.io/foundriesio/busybox:1.36
    command: sh -c "while true; do sleep 60; done"
  multi:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
`
	ctrdDerivedApp = `
services:
  derived:
    image: %s
    command: sh -c "while true; do sleep 60; done"
`
	ctrdKilledApp = `
services:
  a-base:
    image: %s
    command: sh -c "while true; do sleep 60; done"
  b-derived:
    image: %s
    command: sh -c "while true; do sleep 60; done"
`
)

type (
	// ctrdRig is the containerd behind the test dockerd.
	ctrdRig struct {
		t           *testing.T
		c           *containerd.Client
		snapshotter string
		contentRoot string
		blobsRoot   string
	}

	ctrdTestImage struct {
		refs     []string // the pinned digest reference and the short tag reference
		manifest ocispec.Manifest
		chainIDs []digest.Digest
	}
)

// newCtrdRig connects to dockerd's containerd, or skips the test on a graphdriver dockerd.
func newCtrdRig(t *testing.T) *ctrdRig {
	t.Helper()
	cli, err := compose.GetDockerClient("")
	f.Check(t, err)
	defer cli.Close()
	info, err := compose.GetDockerInfo(context.Background(), cli)
	f.Check(t, err)
	if info.Snapshotter() == "" {
		t.Skip("dockerd uses a graphdriver, not the containerd image store")
	}
	c, err := containerd.New(info.Containerd.Address, containerd.WithDefaultNamespace(info.Containerd.Namespaces.Containers))
	f.Check(t, err)
	t.Cleanup(func() { c.Close() })
	r := &ctrdRig{t: t, c: c, snapshotter: info.Snapshotter(), blobsRoot: compose.GetBlobsRootFor(f.AppStoreRoot)}
	resp, err := c.IntrospectionService().Plugins(context.Background(), []string{"type==io.containerd.content.v1,id==content"})
	f.Check(t, err)
	if len(resp.Plugins) != 1 {
		t.Fatalf("content plugin not found: %+v", resp.Plugins)
	}
	r.contentRoot = resp.Plugins[0].Exports["root"]
	return r
}

func (r *ctrdRig) readJSON(d digest.Digest, v any) {
	b, err := os.ReadFile(filepath.Join(r.blobsRoot, d.Encoded()))
	f.Check(r.t, err)
	f.Check(r.t, json.Unmarshal(b, v))
}

// images describes the images of the app from the composectl store.
func (r *ctrdRig) images(appURI string) []ctrdTestImage {
	cfg := f.NewTestConfig(r.t)
	app, err := v1.NewAppLoader().LoadAppTree(context.Background(), compose.NewStoreBlobProvider(r.blobsRoot),
		platforms.OnlyStrict(cfg.Platform), appURI)
	f.Check(r.t, err)
	var out []ctrdTestImage
	for _, root := range app.GetComposeRoot().Children {
		ref, err := compose.ParseImageRef(root.Ref())
		f.Check(r.t, err)
		img := ctrdTestImage{refs: []string{root.Ref(), ref.GetTagRef()}}
		mNode := root
		if images.IsIndexType(root.Descriptor.MediaType) {
			mNode = root.Children[0]
		}
		r.readJSON(mNode.Descriptor.Digest, &img.manifest)
		var imageConfig ocispec.Image
		r.readJSON(img.manifest.Config.Digest, &imageConfig)
		img.chainIDs = identity.ChainIDs(imageConfig.RootFS.DiffIDs)
		out = append(out, img)
	}
	return out
}

func (r *ctrdRig) blobInContentRoot(d digest.Digest) bool {
	_, err := os.Stat(filepath.Join(r.contentRoot, "blobs", d.Algorithm().String(), d.Encoded()))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		f.Check(r.t, err)
	}
	return err == nil
}

func (r *ctrdRig) snapshot(chainID digest.Digest) (snapshots.Info, bool) {
	info, err := r.c.SnapshotService(r.snapshotter).Stat(context.Background(), chainID.String())
	if err != nil && !errdefs.IsNotFound(err) {
		f.Check(r.t, err)
	}
	return info, err == nil
}

func (r *ctrdRig) installLeases() []leases.Lease {
	all, err := r.c.LeasesService().List(context.Background())
	f.Check(r.t, err)
	var found []leases.Lease
	for _, l := range all {
		if strings.HasPrefix(l.ID, "composectl/install/") {
			found = append(found, l)
		}
	}
	return found
}

func dockerImageExists(ref string) bool {
	return exec.Command("docker", "image", "inspect", ref).Run() == nil
}

// checkInstalled verifies the outcome of the install: dockerd runs every image by both references
// offline, no compressed layer stays in containerd, and every snapshot of the image is there.
func (r *ctrdRig) checkInstalled(app *f.App) {
	t := r.t
	for _, img := range r.images(app.PublishedUri) {
		for _, ref := range img.refs {
			c := exec.Command("docker", "run", "--rm", "--pull", "never", "--network", "none", ref, "echo", "ok")
			out, err := c.CombinedOutput()
			f.Checkf(t, err, "docker run %s: %s", ref, out)
		}
		for _, layer := range img.manifest.Layers {
			if r.blobInContentRoot(layer.Digest) {
				t.Errorf("layer %s is still in the containerd content store", layer.Digest)
			}
		}
		if !r.blobInContentRoot(img.manifest.Config.Digest) {
			t.Errorf("config %s is not in the containerd content store", img.manifest.Config.Digest)
		}
		for _, chainID := range img.chainIDs {
			if _, ok := r.snapshot(chainID); !ok {
				t.Errorf("snapshot %s is missing", chainID)
			}
		}
	}
	if leftovers := r.installLeases(); len(leftovers) > 0 {
		t.Errorf("install leases left after the install: %+v", leftovers)
	}
}

func (r *ctrdRig) checkNotInstalled(app *f.App) {
	for _, img := range r.images(app.PublishedUri) {
		for _, ref := range img.refs {
			if dockerImageExists(ref) {
				r.t.Errorf("image %s is still in dockerd", ref)
			}
		}
	}
}

// pruneImages removes the images no container uses, as aktualizr-lite does after a store prune,
// and waits for containerd's GC to remove the snapshots of the app images.
func (r *ctrdRig) pruneImages(topChainIDs []digest.Digest) {
	cli, err := compose.GetDockerClient("")
	f.Check(r.t, err)
	defer cli.Close()
	_, err = cli.ImagesPrune(context.Background(), filters.NewArgs(filters.Arg("dangling", "false")))
	f.Check(r.t, err)
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if !r.anySnapshot(topChainIDs) {
			return
		}
	}
	r.t.Fatal("snapshots of the app images are still there after the prune")
}

func (r *ctrdRig) anySnapshot(chainIDs []digest.Digest) bool {
	for _, chainID := range chainIDs {
		if _, ok := r.snapshot(chainID); ok {
			return true
		}
	}
	return false
}

// topChainIDs returns the chain ID of the top snapshot of each app image.
func (r *ctrdRig) topChainIDs(app *f.App) []digest.Digest {
	var tops []digest.Digest
	for _, img := range r.images(app.PublishedUri) {
		tops = append(tops, img.chainIDs[len(img.chainIDs)-1])
	}
	return tops
}

// TestCtrdInstall: an app with an image pinned by a manifest and one pinned by an index is installed
// from the store, runs, is seen by `check --install` and `ps`, and is removed by uninstall.
func TestCtrdInstall(t *testing.T) {
	r := newCtrdRig(t)
	app := f.NewApp(t, ctrdTestApp)
	app.Publish(t)
	app.Pull(t)
	defer app.Remove(t)

	app.Install(t)
	r.checkInstalled(app)
	app.CheckInstalled(t)

	app.Run(t)
	app.CheckRunning(t)
	app.Stop(t)
	app.Uninstall(t)
	r.checkNotInstalled(app)
}

// TestCtrdInstallSharedLayers: an update whose image is built on top of the installed version's image
// keeps the base snapshots and adds the new one.
func TestCtrdInstallSharedLayers(t *testing.T) {
	r := newCtrdRig(t)
	const base = "ghcr.io/foundriesio/busybox:1.36"
	appV1 := f.NewApp(t, fmt.Sprintf(ctrdDerivedApp, base), "ctrdshared")
	appV1.Publish(t)
	appV1.Pull(t)
	appV1.Install(t)
	r.checkInstalled(appV1)

	baseChain := r.images(appV1.PublishedUri)[0].chainIDs
	var baseCreated []time.Time
	for _, chainID := range baseChain {
		info, ok := r.snapshot(chainID)
		if !ok {
			t.Fatalf("base snapshot %s is missing", chainID)
		}
		baseCreated = append(baseCreated, info.Created)
	}

	derived := f.PublishDerivedImage(t, base, 4)
	appV2 := f.NewApp(t, fmt.Sprintf(ctrdDerivedApp, derived), "ctrdshared")
	appV2.Publish(t)
	appV2.Pull(t)
	defer appV2.Remove(t)
	app2Chain := r.images(appV2.PublishedUri)[0].chainIDs
	if len(app2Chain) != len(baseChain)+1 || app2Chain[len(baseChain)-1] != baseChain[len(baseChain)-1] {
		t.Fatalf("the derived image does not share the base chain: %v vs %v", app2Chain, baseChain)
	}

	appV2.Install(t)
	defer r.pruneImages(baseChain[len(baseChain)-1:]) // v1's images stay in dockerd until v2 is uninstalled
	defer appV2.Uninstall(t)
	appV1.Remove(t) // `run` by name needs one version of the app in the store
	r.checkInstalled(appV2)
	for i, chainID := range baseChain {
		info, ok := r.snapshot(chainID)
		if !ok || !info.Created.Equal(baseCreated[i]) {
			t.Errorf("base snapshot %s was recreated by the update: %v -> %v", chainID, baseCreated[i], info.Created)
		}
	}
	appV2.Run(t)
	appV2.CheckRunning(t)
	appV2.Stop(t)
}

// TestCtrdInstallAfterPrune: `/images/prune` with aktualizr-lite's filter removes the installed app
// images, `check --install` reports them missing, and the app is reinstalled from the store.
func TestCtrdInstallAfterPrune(t *testing.T) {
	r := newCtrdRig(t)
	app := f.NewApp(t, ctrdTestApp)
	app.Publish(t)
	app.Pull(t)
	defer app.Remove(t)
	app.Install(t)
	r.checkInstalled(app)

	r.pruneImages(r.topChainIDs(app))
	if res := app.GetInstallCheckRes(t); len(res.MissingImages) == 0 {
		t.Fatal("check --install reports no missing image after the prune")
	}

	app.Install(t)
	defer app.Uninstall(t)
	r.checkInstalled(app)
	app.CheckInstalled(t)
	app.Run(t)
	app.CheckRunning(t)
	app.Stop(t)
}

// TestCtrdInstallRecoversFromKilledInstall: an install is killed after it created snapshots; its
// lease stays, marked as synced, and the next install goes on under it: the snapshots the killed
// install committed are kept, the rest are created, and the lease is gone at the end.
func TestCtrdInstallRecoversFromKilledInstall(t *testing.T) {
	r := newCtrdRig(t)
	const base = "ghcr.io/foundriesio/busybox:1.36"
	// The small base image is installed first; the big derived layer keeps the install busy after that.
	app := f.NewApp(t, fmt.Sprintf(ctrdKilledApp, base, f.PublishDerivedImage(t, base, 48)))
	app.Publish(t)
	app.Pull(t)
	defer app.Remove(t)
	r.pruneImages(r.topChainIDs(app))
	killedAt := r.killInstall(app)
	if leftovers := r.installLeases(); len(leftovers) != 1 || leftovers[0].Labels["composectl/install.synced"] != "true" {
		t.Fatalf("expected the lease of the killed install to stay, marked as synced, got %+v", leftovers)
	}
	committed := map[digest.Digest]time.Time{}
	for _, img := range r.images(app.PublishedUri) {
		for _, chainID := range img.chainIDs {
			if info, ok := r.snapshot(chainID); ok {
				committed[chainID] = info.Created
			}
		}
	}

	app.Install(t)
	defer app.Uninstall(t)
	r.checkInstalled(app)
	if leftovers := r.installLeases(); len(leftovers) != 0 {
		t.Errorf("expected no install lease after the resumed install, got %+v", leftovers)
	}
	for _, img := range r.images(app.PublishedUri) {
		for _, chainID := range img.chainIDs {
			info, ok := r.snapshot(chainID)
			if createdAt, kept := committed[chainID]; kept && (!ok || !info.Created.Equal(createdAt)) {
				t.Errorf("snapshot %s of the killed install was not kept: created %v, was %v", chainID, info.Created, createdAt)
			} else if !kept && (!ok || !info.Created.After(killedAt)) {
				t.Errorf("snapshot %s was not created after the killed install: created %v, killed at %v", chainID, info.Created, killedAt)
			}
		}
	}
	app.Run(t)
	app.CheckRunning(t)
	app.Stop(t)
}

// killInstall starts an install of the app and kills it once its lease and the first snapshot of
// its first image exist. It returns the time of the kill.
func (r *ctrdRig) killInstall(app *f.App) time.Time {
	t := r.t
	t.Helper()
	firstChain := r.images(app.PublishedUri)[0].chainIDs
	install := exec.Command(os.Getenv("COMPOSECTL_EXE"), "install", app.PublishedUri)
	f.Check(t, install.Start())
	defer func() { _ = install.Process.Kill(); _ = install.Wait() }()
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, ok := r.snapshot(firstChain[0]); ok && len(r.installLeases()) > 0 {
			f.Check(t, install.Process.Kill())
			return time.Now()
		}
	}
	t.Fatal("the install created no lease and snapshot in time")
	return time.Time{}
}

// TestCtrdInstallWaitsForLock: a second composectl waits for the install lock instead of installing
// concurrently.
func TestCtrdInstallWaitsForLock(t *testing.T) {
	r := newCtrdRig(t)
	app := f.NewApp(t, ctrdTestApp)
	app.Publish(t)
	app.Pull(t)
	defer app.Remove(t)

	lock, err := os.OpenFile(filepath.Join(f.AppStoreRoot, ".install.lock"), os.O_RDWR|os.O_CREATE, 0o644)
	f.Check(t, err)
	defer lock.Close()
	f.Check(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_EX))

	install := exec.Command(os.Getenv("COMPOSECTL_EXE"), "install", app.PublishedUri)
	install.Stdout = os.Stdout
	install.Stderr = os.Stderr
	f.Check(t, install.Start())
	done := make(chan error, 1)
	go func() { done <- install.Wait() }()
	select {
	case err := <-done:
		t.Fatalf("install did not wait for the lock: %v", err)
	case <-time.After(3 * time.Second):
	}
	if _, ok := r.snapshot(r.images(app.PublishedUri)[0].chainIDs[0]); ok {
		t.Fatal("install created a snapshot while the lock was held")
	}

	f.Check(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_UN))
	select {
	case err := <-done:
		f.Checkf(t, err, "install failed after the lock was released: %v", err)
	case <-time.After(5 * time.Minute):
		t.Fatal("install did not finish after the lock was released")
	}
	defer app.Uninstall(t)
	r.checkInstalled(app)
}

// TestCtrdUpdateCancel: `update cancel` after an install into containerd removes the loaded images.
func TestCtrdUpdateCancel(t *testing.T) {
	r := newCtrdRig(t)
	app := f.NewApp(t, ctrdTestApp)
	app.Publish(t)

	ctx := context.Background()
	ur, err := update.NewUpdate(f.NewTestConfig(t), "target-ctrd-cancel")
	f.Check(t, err)
	f.Check(t, ur.Init(ctx, []string{app.PublishedUri}))
	f.Check(t, ur.Fetch(ctx))
	f.Check(t, ur.Install(ctx))
	r.checkInstalled(app)
	imgs := r.images(app.PublishedUri) // the cancel removes the app blobs from the store
	if len(ur.Status().LoadedImages) != 2 {
		t.Fatalf("expected 2 loaded images, got %+v", ur.Status().LoadedImages)
	}
	f.Check(t, ur.Cancel(ctx))
	if ur.Status().State != update.StateCanceled {
		t.Fatalf("update not canceled: %s", ur.Status().State)
	}
	for _, img := range imgs {
		if dockerImageExists(img.refs[1]) {
			t.Errorf("image %s is still in dockerd after the cancel", img.refs[1])
		}
	}
	f.Check(t, os.RemoveAll(f.NewTestConfig(t).GetAppComposeDir(app.Name)))
	f.Check(t, compose.RemoveApps(ctx, f.NewTestConfig(t), []string{app.PublishedUri}, compose.WithCheckStatus(false)))
}

// TestCtrdAktualizrLiteFlow runs the composectl command lines that aktualizr-lite runs, in its
// order, on the containerd route: the fetch check and the pull, the install check and the install,
// the status, then the stop, the uninstall, the removal from the store and the prune. After that,
// the uninstall of an app that is not installed, and a prune after a killed install, which must
// remove the lease the install left.
func TestCtrdAktualizrLiteFlow(t *testing.T) {
	newCtrdRig(t)
	app := f.NewApp(t, ctrdTestApp)
	app.Publish(t)
	t.Cleanup(func() { akliteCleanup(app) })
	t.Run("fetch", func(t *testing.T) { akliteFetch(t, app) })
	t.Run("install", func(t *testing.T) { akliteInstall(t, app) })
	t.Run("status", func(t *testing.T) { akliteStatus(t, app) })
	t.Run("stop, uninstall, rm and prune", func(t *testing.T) { akliteRemove(t, app) })
	t.Run("uninstall of an app that is not installed, prune after a killed install", func(t *testing.T) {
		aklitePruneAfterKilledInstall(t)
	})
}

// aklite runs composectl as aktualizr-lite does, with the store root on every command line.
func aklite(t *testing.T, args ...string) []byte {
	t.Helper()
	return f.RunCmd(t, append([]string{"--store", f.AppStoreRoot}, args...)...)
}

// akliteCleanup stops, uninstalls and removes the app when a step of the flow failed before it
// could: each command is attempted on its own, and their errors are not the test's.
func akliteCleanup(app *f.App) {
	for _, args := range [][]string{
		{"--compose", f.AppComposeRootRoot, "stop", app.Name},
		{"--compose", f.AppComposeRootRoot, "uninstall", "--ignore-non-installed", app.Name},
		{"rm", app.PublishedUri, "--quiet"},
	} {
		_ = exec.Command(os.Getenv("COMPOSECTL_EXE"), append([]string{"--store", f.AppStoreRoot}, args...)...).Run()
	}
}

// akliteCheck runs `check <uri> [flags] --format json` and decodes its output.
func akliteCheck(t *testing.T, uri string, flags ...string) composectl.CheckAndInstallResult {
	t.Helper()
	out := aklite(t, append(append([]string{"check", uri}, flags...), "--format", "json")...)
	var res composectl.CheckAndInstallResult
	f.Checkf(t, json.Unmarshal(out, &res), "check %s %v printed no JSON: %s", uri, flags, out)
	return res
}

// akliteInstallCheck returns what `check --local --install` reports for the app.
func akliteInstallCheck(t *testing.T, app *f.App) *composectl.AppInstallCheckResult {
	t.Helper()
	res := akliteCheck(t, app.PublishedUri, "--local", "--install")
	if res.InstallCheck == nil || (*res.InstallCheck)[app.PublishedUri] == nil {
		t.Fatalf("no install check result for %s: %+v", app.PublishedUri, res.InstallCheck)
	}
	return (*res.InstallCheck)[app.PublishedUri]
}

func akliteFetch(t *testing.T, app *f.App) {
	if res := akliteCheck(t, app.PublishedUri); len(res.FetchCheck.MissingBlobs) == 0 {
		t.Fatal("check reports no missing blob before the pull")
	}
	aklite(t, "pull", "-p", app.PublishedUri, "--storage-usage-watermark", "90")
	if res := akliteCheck(t, app.PublishedUri); len(res.FetchCheck.MissingBlobs) != 0 {
		t.Errorf("check reports missing blobs after the pull: %+v", res.FetchCheck.MissingBlobs)
	}
	if res := akliteCheck(t, app.PublishedUri, "--local"); len(res.FetchCheck.MissingBlobs) != 0 {
		t.Errorf("check --local reports missing blobs after the pull: %+v", res.FetchCheck.MissingBlobs)
	}
}

func akliteInstall(t *testing.T, app *f.App) {
	r := newCtrdRig(t)
	r.pruneImages(r.topChainIDs(app)) // an earlier test may have left the images in dockerd
	if len(akliteInstallCheck(t, app).MissingImages) == 0 {
		t.Fatal("check --install reports no missing image before the install")
	}
	aklite(t, "--compose", f.AppComposeRootRoot, "--host", os.Getenv("DOCKER_HOST"), "install", app.PublishedUri)
	r.checkInstalled(app)
	if res := akliteInstallCheck(t, app); len(res.MissingImages) != 0 || len(res.BundleErrors) != 0 {
		t.Errorf("check --install reports the app as not installed: missing images %v, bundle errors %v",
			res.MissingImages, res.BundleErrors)
	}
}

// akliteStatus starts the app as aktualizr-lite does, with `docker compose up` from the compose
// root, and reads its status: `ps` of all apps, and `ps` of the one app (`ps` takes a URI, not a
// name).
func akliteStatus(t *testing.T, app *f.App) {
	app.Up(t)
	app.CheckRunning(t)
	var all map[string]composectl.App
	f.Check(t, json.Unmarshal(aklite(t, "ps", "--format", "json"), &all))
	if len(all) != 1 || all[app.Name].URI != app.PublishedUri || all[app.Name].State != "running" {
		t.Errorf("ps does not report the one running app %s: %+v", app.Name, all)
	}
	var one map[string]composectl.App
	f.Check(t, json.Unmarshal(aklite(t, "--compose", f.AppComposeRootRoot, "ps", app.PublishedUri, "--format", "json"), &one))
	if len(one) != 1 || one[app.PublishedUri].State != "running" {
		t.Errorf("ps %s does not report the app running: %+v", app.PublishedUri, one)
	}
}

// akliteRemove stops and uninstalls the app, which removes its images from dockerd, removes it from
// the store without a prune, which keeps its blobs, and prunes them.
func akliteRemove(t *testing.T, app *f.App) {
	r := newCtrdRig(t)
	config := filepath.Join(r.blobsRoot, r.images(app.PublishedUri)[0].manifest.Config.Digest.Encoded())
	aklite(t, "--compose", f.AppComposeRootRoot, "stop", app.Name)
	aklite(t, "--compose", f.AppComposeRootRoot, "uninstall", "--ignore-non-installed", app.Name)
	r.checkNotInstalled(app)
	aklite(t, "rm", app.PublishedUri, "--prune=false", "--quiet")
	if _, err := os.Stat(config); err != nil {
		t.Errorf("rm --prune=false did not keep the app blobs: %v", err)
	}
	aklite(t, "prune", "--format=json")
	if _, err := os.Stat(config); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("prune did not remove the app blobs: %v", err)
	}
	var apps []composectl.AppJsonOutput
	f.Check(t, json.Unmarshal(aklite(t, "ls", "--format", "json"), &apps))
	for _, a := range apps {
		if a.URI == app.PublishedUri {
			t.Errorf("ls still lists the removed app: %+v", apps)
		}
	}
}

// aklitePruneAfterKilledInstall: the uninstall of an app that is in the store but not installed
// exits 0, and the prune after a killed install removes the lease the install left.
func aklitePruneAfterKilledInstall(t *testing.T) {
	r := newCtrdRig(t)
	const base = "ghcr.io/foundriesio/busybox:1.36"
	app := f.NewApp(t, fmt.Sprintf(ctrdKilledApp, base, f.PublishDerivedImage(t, base, 48)))
	app.Publish(t)
	aklite(t, "pull", "-p", app.PublishedUri, "--storage-usage-watermark", "90")
	defer aklite(t, "rm", app.PublishedUri, "--prune=false", "--quiet")
	aklite(t, "--compose", f.AppComposeRootRoot, "uninstall", "--ignore-non-installed", app.Name)

	r.pruneImages(r.topChainIDs(app))
	r.killInstall(app)
	if leftovers := r.installLeases(); len(leftovers) != 1 {
		t.Fatalf("expected the lease of the killed install to stay, got %+v", leftovers)
	}
	aklite(t, "prune", "--format=json")
	if leftovers := r.installLeases(); len(leftovers) != 0 {
		t.Errorf("install leases left after the prune: %+v", leftovers)
	}
}

// TestCtrdCheckSizesByMissingSnapshots: `check` sizes the runtime by the snapshots the install
// still has to create, not by the blobs missing from the store: after a prune every blob is still
// in the store, yet the layers have to be unpacked again.
func TestCtrdCheckSizesByMissingSnapshots(t *testing.T) {
	r := newCtrdRig(t)
	app := f.NewApp(t, ctrdTestApp)
	app.Publish(t)
	app.Pull(t)
	defer app.Remove(t)
	app.Install(t)
	defer app.Uninstall(t)

	runtimeSize := func(when string) int64 {
		t.Helper()
		res := akliteCheck(t, app.PublishedUri, "--local")
		if len(res.FetchCheck.MissingBlobs) != 0 {
			t.Fatalf("%s: check reports missing blobs: %+v", when, res.FetchCheck.MissingBlobs)
		}
		return res.FetchCheck.TotalRuntimeSize
	}
	if size := runtimeSize("installed"); size != 0 {
		t.Fatalf("installed: total_runtime_size is %d, want 0", size)
	}
	r.pruneImages(r.topChainIDs(app))
	if size := runtimeSize("pruned"); size <= 0 {
		t.Fatalf("pruned: total_runtime_size is %d, want more than 0", size)
	}
	app.Install(t)
	if size := runtimeSize("reinstalled"); size != 0 {
		t.Fatalf("reinstalled: total_runtime_size is %d, want 0", size)
	}
}

// TestCtrdCheckSizesImageWithMissingConfig: `check --local` of an app whose image config is not
// in the store (an interrupted fetch) reports the missing config and sizes the runtime as if every
// layer had to be unpacked.
func TestCtrdCheckSizesImageWithMissingConfig(t *testing.T) {
	r := newCtrdRig(t)
	app := f.NewApp(t, ctrdTestApp)
	app.Publish(t)
	app.Pull(t)
	defer app.Remove(t)
	r.pruneImages(r.topChainIDs(app)) // no snapshot of the app exists: every layer counts

	cfg := f.NewTestConfig(t)
	tree, err := v1.NewAppLoader().LoadAppTree(context.Background(), compose.NewStoreBlobProvider(r.blobsRoot),
		platforms.OnlyStrict(cfg.Platform), app.PublishedUri)
	f.Check(t, err)
	var unpacked int64
	seen := map[digest.Digest]bool{}
	imgs := r.images(app.PublishedUri)
	for _, img := range imgs {
		for _, layer := range img.manifest.Layers {
			if !seen[layer.Digest] {
				seen[layer.Digest] = true
				unpacked += tree.GetBlobRuntimeSize(&layer, cfg.Platform.Architecture, cfg.BlockSize)
			}
		}
	}

	configDigest := imgs[0].manifest.Config.Digest
	configPath := filepath.Join(r.blobsRoot, configDigest.Encoded())
	config, err := os.ReadFile(configPath)
	f.Check(t, err)
	f.Check(t, os.Remove(configPath))
	defer func() { f.Check(t, os.WriteFile(configPath, config, 0o444)) }()

	res := akliteCheck(t, app.PublishedUri, "--local")
	if _, ok := res.FetchCheck.MissingBlobs[configDigest]; !ok {
		t.Errorf("check does not list the config among the missing blobs: %+v", res.FetchCheck.MissingBlobs)
	}
	if res.FetchCheck.TotalRuntimeSize < unpacked {
		t.Errorf("total_runtime_size %d is below the unpacked size of the layers %d",
			res.FetchCheck.TotalRuntimeSize, unpacked)
	}
}
