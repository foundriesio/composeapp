package e2e_tests

import (
	"context"
	"github.com/foundriesio/composeapp/pkg/compose"
	f "github.com/foundriesio/composeapp/test/fixtures"
	"testing"
)

func TestAppApi(t *testing.T) {
	appComposeDef01 := `
services:
  srvs-01:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
    ports:
    - 8080:80
  busybox:
    image: ghcr.io/foundriesio/busybox:1.36
    command: sh -c "while true; do sleep 60; done"
`
	appComposeDef02 := `
services:
  srvs-01:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
    ports:
    - 8080:80
  srvs-02:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
`
	var apps []*f.App
	for _, appDef := range []string{appComposeDef01, appComposeDef02} {
		app := f.NewApp(t, appDef)
		app.Publish(t)
		apps = append(apps, app)
	}
	for _, a := range apps {
		a.Pull(t)
	}
	defer func() {
		for _, a := range apps {
			a.Remove(t)
		}
	}()
	appsMap := make(map[string]bool)
	for _, a := range apps {
		appsMap[a.PublishedUri] = false
	}

	ctx := context.Background()
	cfg := f.NewTestConfig(t)

	listedApps, err := compose.ListApps(ctx, cfg)
	f.Check(t, err)
	if len(listedApps) != len(appsMap) {
		t.Fatalf("expected %d apps, got %d", len(appsMap), len(listedApps))
	}
	for _, app := range listedApps {
		if checked, ok := appsMap[app.Ref().String()]; !ok {
			t.Fatalf("got unexpected app: %s", app.Ref().String())
		} else {
			if checked {
				t.Fatalf("app has been listed twice: %s", app.Ref().String())
			} else {
				appsMap[app.Ref().String()] = true
			}
		}
	}
}
