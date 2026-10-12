package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	dockerclient "github.com/docker/docker/client"
)

type (
	// DockerInfo is the part of dockerd's `/info` that composectl reads.
	// It is decoded from a raw GET because the Docker v25 client's Info type has no Containerd field.
	DockerInfo struct {
		ServerVersion string
		Driver        string
		DriverStatus  [][2]string
		Containerd    *ContainerdInfo
	}
	ContainerdInfo struct {
		Address    string
		Namespaces struct{ Containers string }
	}
)

const snapshotterPluginType = "io.containerd.snapshotter.v1"

func GetDockerClient(dockerHost string) (*dockerclient.Client, error) {
	opts := []dockerclient.Opt{
		dockerclient.FromEnv,
	}
	if len(dockerHost) > 0 {
		opts = append(opts, dockerclient.WithHost(dockerHost))
	}
	return dockerclient.NewClientWithOpts(opts...)
}

// GetDockerInfo reads dockerd's `/info`. On a local socket it is a raw GET through the client's
// transport, which is the only way to get the Containerd field with the Docker v25 client. On a
// TCP host the raw request would not reach the daemon (the client's address and TLS live in its
// request path), so the typed Info is used there; it has no Containerd field, and that host is
// never an install target anyway (see isCtrdInstallTarget).
func GetDockerInfo(ctx context.Context, cli *dockerclient.Client) (*DockerInfo, error) {
	if !isLocalDockerHost(cli.DaemonHost()) {
		info, err := cli.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get docker info: %w", err)
		}
		return &DockerInfo{ServerVersion: info.ServerVersion, Driver: info.Driver, DriverStatus: info.DriverStatus}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/info", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build the docker info request: %w", err)
	}
	resp, err := cli.HTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get docker info: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get docker info: %s", resp.Status)
	}
	info, err := parseDockerInfo(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to decode docker info: %w", err)
	}
	return info, nil
}

// isLocalDockerHost reports whether the docker host is a socket on this machine.
func isLocalDockerHost(dockerHost string) bool {
	return strings.HasPrefix(dockerHost, "unix://") || strings.HasPrefix(dockerHost, "npipe://")
}

func parseDockerInfo(r io.Reader) (*DockerInfo, error) {
	var info DockerInfo
	if err := json.NewDecoder(r).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

// Snapshotter returns the name of the containerd snapshotter that dockerd uses,
// or "" when dockerd uses a graphdriver instead of the containerd image store.
func (i *DockerInfo) Snapshotter() string {
	for _, kv := range i.DriverStatus {
		if kv[0] == "driver-type" && kv[1] == snapshotterPluginType {
			return i.Driver
		}
	}
	return ""
}

// versionAtLeast reports whether a dotted version such as "29.7.2", "v29.5.2" or "20.10.24+dfsg1"
// is not older than min. Only the leading numeric components are compared.
func versionAtLeast(version, min string) bool {
	have := versionNumbers(version)
	want := versionNumbers(min)
	for len(have) < len(want) {
		have = append(have, 0)
	}
	for i, w := range want {
		if have[i] != w {
			return have[i] > w
		}
	}
	return true
}

func versionNumbers(version string) []int {
	version = strings.TrimLeft(version, "vV") // some builds report "v29.5.2"
	end := strings.IndexFunc(version, func(r rune) bool { return r != '.' && (r < '0' || r > '9') })
	if end >= 0 {
		version = version[:end]
	}
	var numbers []int
	for _, part := range strings.Split(version, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			break
		}
		numbers = append(numbers, n)
	}
	return numbers
}
