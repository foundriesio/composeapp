package compose

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dockerclient "github.com/docker/docker/client"
)

// Trimmed `/info` responses of dockerd 29.7.2 on the containerd image store and on the overlay2 graphdriver.
const (
	infoContainerdStore = `{
  "ID": "6ef1",
  "Driver": "overlayfs",
  "DriverStatus": [["driver-type", "io.containerd.snapshotter.v1"]],
  "ServerVersion": "29.7.2",
  "Containerd": {
    "Address": "/var/run/docker/containerd/containerd.sock",
    "Namespaces": {"Containers": "moby", "Plugins": "plugins.moby"}
  }
}`
	infoGraphdriver = `{
  "ID": "6ef1",
  "Driver": "overlay2",
  "DriverStatus": [["Backing Filesystem", "extfs"], ["Supports d_type", "true"]],
  "ServerVersion": "29.7.2",
  "Containerd": {
    "Address": "/var/run/docker/containerd/containerd.sock",
    "Namespaces": {"Containers": "moby", "Plugins": "plugins.moby"}
  }
}`
	infoDocker25 = `{"Driver": "overlay2", "DriverStatus": [], "ServerVersion": "25.0.3"}`
)

func TestParseDockerInfoContainerdStore(t *testing.T) {
	info, err := parseDockerInfo(strings.NewReader(infoContainerdStore))
	if err != nil {
		t.Fatal(err)
	}
	if info.Snapshotter() != "overlayfs" {
		t.Fatalf("snapshotter: got %q, want overlayfs", info.Snapshotter())
	}
	if info.Containerd == nil || info.Containerd.Address != "/var/run/docker/containerd/containerd.sock" {
		t.Fatalf("containerd address: got %+v", info.Containerd)
	}
	if info.Containerd.Namespaces.Containers != "moby" {
		t.Fatalf("containerd namespace: got %q, want moby", info.Containerd.Namespaces.Containers)
	}
	if info.ServerVersion != "29.7.2" {
		t.Fatalf("server version: got %q", info.ServerVersion)
	}
}

func TestParseDockerInfoGraphdriver(t *testing.T) {
	info, err := parseDockerInfo(strings.NewReader(infoGraphdriver))
	if err != nil {
		t.Fatal(err)
	}
	if info.Snapshotter() != "" {
		t.Fatalf("snapshotter: got %q, want none", info.Snapshotter())
	}
	info, err = parseDockerInfo(strings.NewReader(infoDocker25))
	if err != nil {
		t.Fatal(err)
	}
	if info.Snapshotter() != "" || info.Containerd != nil {
		t.Fatalf("docker 25 info: got snapshotter %q, containerd %+v", info.Snapshotter(), info.Containerd)
	}
}

func TestVersionAtLeast(t *testing.T) {
	for _, tc := range []struct {
		version string
		min     string
		want    bool
	}{
		{"29.5.2", "29.5.2", true},
		{"29.7.2", "29.5.2", true},
		{"29.5.1", "29.5.2", false},
		{"29.4.9", "29.5.2", false},
		{"30.0.0", "29.5.2", true},
		{"28.9.9", "29.5.2", false},
		{"25.0.3", "29.5.2", false},
		{"29.7.2+fio", "29.5.2", true},
		{"20.10.24+dfsg1", "29.5.2", false},
		{"29.5.2-rc1", "29.5.2", true},
		{"v29.5.2", "29.5.2", true},
		{"v29.5.2", "29.7.2", false},
		{"V29.7.2", "29.5.2", true},
		{"v", "29.5.2", false},
		{"29.5", "29.5.2", false},
		{"29.6", "29.5.2", true},
		{"", "29.5.2", false},
		{"garbage", "29.5.2", false},
	} {
		if got := versionAtLeast(tc.version, tc.min); got != tc.want {
			t.Errorf("versionAtLeast(%q, %q) = %v, want %v", tc.version, tc.min, got, tc.want)
		}
	}
}

// On a TCP host the raw GET through the transport cannot reach the daemon, so the typed Info is
// used; it has no Containerd field, and such a host never qualifies for the containerd install.
func TestGetDockerInfoOverTCP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/info") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, infoContainerdStore)
	}))
	defer srv.Close()
	cli, err := dockerclient.NewClientWithOpts(dockerclient.WithHost("tcp://" + srv.Listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	info, err := GetDockerInfo(context.Background(), cli)
	if err != nil {
		t.Fatal(err)
	}
	if info.ServerVersion != "29.7.2" || info.Snapshotter() != "overlayfs" {
		t.Fatalf("unexpected info over tcp: %+v", info)
	}
	if info.Containerd != nil {
		t.Fatalf("the typed info must not carry a containerd address: %+v", info.Containerd)
	}
	if isCtrdInstallTarget(info, cli.DaemonHost()) {
		t.Fatal("a tcp host must not be an install target")
	}
}

func TestIsLocalDockerHost(t *testing.T) {
	for host, want := range map[string]bool{
		"unix:///var/run/docker.sock": true,
		"npipe:////./pipe/docker":     true,
		"tcp://10.0.0.1:2376":         false,
		"":                            false,
	} {
		if got := isLocalDockerHost(host); got != want {
			t.Errorf("isLocalDockerHost(%q) = %v, want %v", host, got, want)
		}
	}
}
