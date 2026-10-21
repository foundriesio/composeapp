package compose_test

// Drives the containerd install directly, with its synchronous progress callback, against the
// test dockerd on the containerd image store. Skips when that dockerd or the composectl binary
// (built by `make`) is not there, as under `make test-unit`.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/platforms"
	"github.com/foundriesio/composeapp/pkg/compose"
	f "github.com/foundriesio/composeapp/test/fixtures"
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
// before the unpack, as `docker image prune` could do, and the install streams every layer and
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
