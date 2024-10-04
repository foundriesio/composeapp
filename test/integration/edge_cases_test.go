package e2e_tests

import (
	f "github.com/foundriesio/composeapp/test/fixtures"
	"testing"
)

func TestAppImageMultiUse(t *testing.T) {
	appComposeDef := `
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
	app := f.NewApp(t, appComposeDef)
	app.Publish(t)

	app.Pull(t)
	defer app.Remove(t)

	app.Install(t)
	defer app.Uninstall(t)

	app.Run(t)
	defer app.Stop(t)
	app.CheckRunning(t)
}

func TestAppMultipleVersionsInStore(t *testing.T) {
	appComposeDef := `
services:
  srvs-01:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
    environment:
    - VERSION = 0.1
`
	appComposeDef02 := `
services:
  srvs-01:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
    environment:
    - VERSION = 0.2
`
	app := f.NewApp(t, appComposeDef)
	app.Publish(t)

	app02 := f.NewApp(t, appComposeDef02, app.Name)
	app02.Publish(t)

	app.Pull(t)
	defer app.Remove(t)

	app02.Pull(t)
	defer app02.Remove(t)

	app.Install(t)
	defer app.Uninstall(t)

	app.Up(t)
	defer app.Stop(t)
	app.CheckRunning(t)
}
