package e2e_tests

import (
	f "github.com/foundriesio/composeapp/test/fixtures"
	"testing"
)

func TestAppApi(t *testing.T) {
	appComposeDef := `
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
	app := f.NewApp(t, appComposeDef)
	app.Publish(t)

	app.Pull(t)
	defer app.Remove(t)

	f.NewTestConfig(t)
	// TODO: extend this test to check the app management API
}
