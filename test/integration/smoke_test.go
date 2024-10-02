package e2e_tests

import (
	"github.com/foundriesio/composeapp/test/fixtures"
	"testing"
)

func TestSmoke(t *testing.T) {
	appComposeDef := `
services:
  busybox:
    image: ghcr.io/foundriesio/busybox:1.36
    command: sh -c "while true; do sleep 60; done"
`
	smokeTest := func(registry string, layersManifest bool) {
		app := fixtures.NewApp(t, appComposeDef, fixtures.WithRegistry(registry))
		app.Publish(t, !layersManifest)

		app.Pull(t)
		defer app.Remove(t)
		app.CheckFetched(t)

		app.Install(t)
		defer app.Uninstall(t)
		app.CheckInstalled(t)

		app.Run(t)
		defer app.Stop(t)
		app.CheckRunning(t)
	}

	for _, param := range []struct {
		Registry       string
		LayersManifest bool
	}{
		{"registry", true},
		{"registry-org", false},
	} {
		smokeTest(param.Registry, param.LayersManifest)
	}
}
