package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/leases"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestInstallLeaseID(t *testing.T) {
	d := digest.FromString("app")
	id := installLeaseID("myapp", d)
	if id != "composectl/install/myapp@"+d.String() {
		t.Fatalf("unexpected lease ID: %s", id)
	}
	if !strings.HasPrefix(id, installLeasePrefix) {
		t.Fatalf("lease ID %s does not start with %s", id, installLeasePrefix)
	}
}

func TestInstallLeasesFilter(t *testing.T) {
	all := []leases.Lease{
		{ID: "composectl/install/app1@sha256:aaaa"},
		{ID: "moby-pull-123"},
		{ID: "composectl/install/app2@sha256:bbbb"},
		{ID: "composectl/other"},
	}
	got := installLeases(all)
	if len(got) != 2 || got[0].ID != all[0].ID || got[1].ID != all[2].ID {
		t.Fatalf("unexpected install leases: %+v", got)
	}
	if got := installLeases(nil); len(got) != 0 {
		t.Fatalf("expected no leases, got %+v", got)
	}
}

func TestIsCtrdInstallTarget(t *testing.T) {
	ctrdStore := func(version string) *DockerInfo {
		return &DockerInfo{
			ServerVersion: version,
			Driver:        "overlayfs",
			DriverStatus:  [][2]string{{"driver-type", snapshotterPluginType}},
			Containerd:    &ContainerdInfo{Address: "/run/docker/containerd/containerd.sock"},
		}
	}
	const local = "unix:///var/run/docker.sock"
	if !isCtrdInstallTarget(ctrdStore("29.5.2"), local) {
		t.Fatal("the containerd image store on the minimum version over a unix socket must qualify")
	}
	if isCtrdInstallTarget(ctrdStore("29.5.1"), local) {
		t.Fatal("a dockerd older than the minimum must not qualify")
	}
	if isCtrdInstallTarget(ctrdStore("29.7.2"), "tcp://10.0.0.1:2375") {
		t.Fatal("a remote dockerd must not qualify")
	}
	graphdriver := ctrdStore("29.7.2")
	graphdriver.Driver = "overlay2"
	graphdriver.DriverStatus = [][2]string{{"Backing Filesystem", "extfs"}}
	if isCtrdInstallTarget(graphdriver, local) {
		t.Fatal("a graphdriver dockerd must not qualify")
	}
	noAddress := ctrdStore("29.7.2")
	noAddress.Containerd = nil
	if isCtrdInstallTarget(noAddress, local) {
		t.Fatal("a dockerd that reports no containerd address must not qualify")
	}
}

// fakeImageStore is an images.Store whose Create cancels the install context after the first
// record, as a cancellation between the two record writes does, and records the deletes.
type fakeImageStore struct {
	cancel  context.CancelFunc
	created []string
	deleted []string
	// deletedWithLiveCtx is false when a delete came with a context that was already done.
	deletedWithLiveCtx bool
}

func (s *fakeImageStore) Get(context.Context, string) (images.Image, error) {
	return images.Image{}, errdefs.ErrNotFound
}
func (s *fakeImageStore) List(context.Context, ...string) ([]images.Image, error) { return nil, nil }
func (s *fakeImageStore) Create(ctx context.Context, img images.Image) (images.Image, error) {
	if err := ctx.Err(); err != nil {
		return images.Image{}, err
	}
	s.created = append(s.created, img.Name)
	s.cancel()
	return img, nil
}
func (s *fakeImageStore) Update(ctx context.Context, img images.Image, _ ...string) (images.Image, error) {
	return images.Image{}, ctx.Err()
}
func (s *fakeImageStore) Delete(ctx context.Context, name string, _ ...images.DeleteOpt) error {
	s.deleted = append(s.deleted, name)
	s.deletedWithLiveCtx = ctx.Err() == nil
	return nil
}

func TestPutImageRecordsRollsBackOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &fakeImageStore{cancel: cancel}
	img := &ctrdImage{ref: "registry/app@sha256:aaaa", tagRef: "registry/app:aaaa"}
	err := putImageRecords(ctx, store, img)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the cancellation to be returned, got %v", err)
	}
	if len(store.created) != 1 || store.created[0] != img.ref {
		t.Fatalf("expected the digest record only to be created, got %v", store.created)
	}
	if len(store.deleted) != 1 || store.deleted[0] != img.ref {
		t.Fatalf("expected the digest record to be deleted again, got %v", store.deleted)
	}
	if !store.deletedWithLiveCtx {
		t.Fatal("the rollback ran on the cancelled context")
	}
}

func writeStoreBlob(t *testing.T, blobsRoot string, data []byte) ocispec.Descriptor {
	t.Helper()
	d := digest.FromBytes(data)
	if err := os.MkdirAll(blobsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobsRoot, d.Encoded()), data, 0600); err != nil {
		t.Fatal(err)
	}
	return ocispec.Descriptor{Digest: d, Size: int64(len(data))}
}

func TestLinkLayerLinksVerifiedBlob(t *testing.T) {
	root := t.TempDir()
	blobsRoot := filepath.Join(root, "store")
	contentRoot := filepath.Join(root, "content")
	desc := writeStoreBlob(t, blobsRoot, []byte("layer bytes"))

	if err := linkLayer(contentRoot, blobsRoot, desc); err != nil {
		t.Fatal(err)
	}
	src, err := os.Stat(filepath.Join(blobsRoot, desc.Digest.Encoded()))
	if err != nil {
		t.Fatal(err)
	}
	dst, err := os.Stat(filepath.Join(contentRoot, "blobs", "sha256", desc.Digest.Encoded()))
	if err != nil {
		t.Fatalf("the blob was not linked into the content root: %v", err)
	}
	if !os.SameFile(src, dst) {
		t.Fatal("the content root blob is not a link to the store blob")
	}
	// A second install finds the link in place and leaves it alone.
	if err := linkLayer(contentRoot, blobsRoot, desc); err != nil {
		t.Fatalf("linking over an existing link must not fail: %v", err)
	}
}

func TestLinkLayerRejectsWrongSize(t *testing.T) {
	root := t.TempDir()
	blobsRoot := filepath.Join(root, "store")
	contentRoot := filepath.Join(root, "content")
	desc := writeStoreBlob(t, blobsRoot, []byte("layer bytes"))
	desc.Size++

	if err := linkLayer(contentRoot, blobsRoot, desc); err == nil || !strings.Contains(err.Error(), desc.Digest.String()) {
		t.Fatalf("expected a size mismatch naming %s, got %v", desc.Digest, err)
	}
	if _, err := os.Stat(filepath.Join(contentRoot, "blobs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("nothing must be linked for a blob of the wrong size, stat: %v", err)
	}
}

func TestLinkLayerFailsOnAnotherFilesystem(t *testing.T) {
	root := t.TempDir()
	blobsRoot := filepath.Join(root, "store")
	contentRoot := filepath.Join(root, "content")
	desc := writeStoreBlob(t, blobsRoot, []byte("layer bytes"))

	orig := linkFile
	t.Cleanup(func() { linkFile = orig })
	linkFile = func(string, string) error { return &os.LinkError{Op: "link", Err: syscall.EXDEV} }

	err := linkLayer(contentRoot, blobsRoot, desc)
	if err == nil || !strings.Contains(err.Error(), blobsRoot) || !strings.Contains(err.Error(), contentRoot) {
		t.Fatalf("expected a failure naming the store and the content root, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(contentRoot, "blobs", "sha256", desc.Digest.Encoded())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no link expected, stat: %v", err)
	}
}

func TestLinkLayerFailsOnMissingBlob(t *testing.T) {
	root := t.TempDir()
	desc := ocispec.Descriptor{Digest: digest.FromString("absent"), Size: 6}
	err := linkLayer(filepath.Join(root, "content"), filepath.Join(root, "store"), desc)
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), desc.Digest.String()) {
		t.Fatalf("expected a not-found failure naming the layer, got %v", err)
	}
}

// fakeIngests is a content.IngestManager whose Abort returns err for the first failures calls, as
// containerd does while the server side of a closed writer still writes the ingest's status, and
// calls cancel, when set, on the first call.
type fakeIngests struct {
	failures int
	err      error
	cancel   context.CancelFunc
	aborts   int
}

func (s *fakeIngests) Status(context.Context, string) (content.Status, error) {
	return content.Status{}, errdefs.ErrNotFound
}
func (s *fakeIngests) ListStatuses(context.Context, ...string) ([]content.Status, error) {
	return nil, nil
}
func (s *fakeIngests) Abort(context.Context, string) error {
	s.aborts++
	if s.cancel != nil {
		s.cancel()
	}
	if s.aborts <= s.failures {
		return s.err
	}
	return nil
}

func TestAbortIngestRetriesWhileTheAbortFails(t *testing.T) {
	ingests := &fakeIngests{failures: 2, err: errors.New("unlinkat ingest: directory not empty")}
	if err := abortIngest(context.Background(), ingests, "ref"); err != nil {
		t.Fatalf("the abort failed after the retries: %v", err)
	}
	if ingests.aborts != 3 {
		t.Fatalf("expected 3 abort calls, got %d", ingests.aborts)
	}
}

func TestAbortIngestGivesUpAfterTheBoundedRetries(t *testing.T) {
	ingests := &fakeIngests{failures: ingestAbortAttempts + 1, err: errors.New("unlinkat ingest: directory not empty")}
	if err := abortIngest(context.Background(), ingests, "ref"); !errors.Is(err, ingests.err) {
		t.Fatalf("expected the last abort error, got %v", err)
	}
	if ingests.aborts != ingestAbortAttempts {
		t.Fatalf("expected %d abort calls, got %d", ingestAbortAttempts, ingests.aborts)
	}
}

func TestAbortIngestOfUnknownRefSucceeds(t *testing.T) {
	ingests := &fakeIngests{failures: 1, err: errdefs.ErrNotFound}
	if err := abortIngest(context.Background(), ingests, "ref"); err != nil {
		t.Fatalf("an unknown ingest must not fail the abort: %v", err)
	}
	if ingests.aborts != 1 {
		t.Fatalf("expected 1 abort call, got %d", ingests.aborts)
	}
}

func TestAbortIngestStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ingests := &fakeIngests{failures: ingestAbortAttempts + 1, err: errors.New("unlinkat ingest: directory not empty"), cancel: cancel}
	if err := abortIngest(ctx, ingests, "ref"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the cancellation to be returned, got %v", err)
	}
	if ingests.aborts != 1 {
		t.Fatalf("expected the retries to stop at the cancellation, got %d abort calls", ingests.aborts)
	}
}
