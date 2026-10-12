package compose

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/leases"
	"github.com/opencontainers/go-digest"
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
