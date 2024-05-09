package compose

import (
	dockerclient "github.com/docker/docker/client"
)

func GetDockerClient(dockerHost string) (*dockerclient.Client, error) {
	opts := []dockerclient.Opt{
		dockerclient.FromEnv,
	}
	if len(dockerHost) > 0 {
		opts = append(opts, dockerclient.WithHost(dockerHost))
	}
	return dockerclient.NewClientWithOpts(opts...)
}
