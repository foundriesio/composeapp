package e2e_tests

import (
	f "github.com/foundriesio/composeapp/test/fixtures"
	"path/filepath"
	"testing"
)

func TestSmoke(t *testing.T) {
	appComposeDef := `
services:
  busybox:
    image: ghcr.io/foundriesio/busybox:1.36
    command: sh -c "while true; do sleep 60; done"
`
	app := f.NewApp(t, appComposeDef)
	layersMetaFile := f.GenerateLayersMetaFile(t, filepath.Dir(app.Dir))

	smokeTest := func(registry string, layersManifest bool) {
		app.Publish(t, f.WithRegistry(registry), f.WithLayersManifest(layersManifest), f.WithLayersMeta(layersMetaFile))

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
