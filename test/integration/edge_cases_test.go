package e2e_tests

import (
	composectl "github.com/foundriesio/composeapp/cmd/composectl/cmd"
	f "github.com/foundriesio/composeapp/test/fixtures"
	"os"
	"path"
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

func TestAppBundleBroken(t *testing.T) {
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
	app.CheckInstalled(t)

	composeFilePath := path.Join(composectl.GetConfig().ComposeRoot, app.Name, "docker-compose.yml")
	if err := os.WriteFile(composeFilePath, []byte("foo bar"), 0x644); err != nil {
		t.Fatal(err)
	}
	checkRes := app.GetInstallCheckRes(t)
	if len(checkRes.BundleErrors) != 1 {
		t.Fatalf("expected 1 app bundle integrity error, got: %d", len(checkRes.BundleErrors))
	}
	if _, ok := checkRes.BundleErrors["docker-compose.yml"]; !ok {
		t.Fatalf("expected error for: %s, got: %+v", "docker-compose.yml", checkRes.BundleErrors)
	}

	app.Run(t)
	defer app.Stop(t)
	app.CheckRunning(t)

	if err := os.WriteFile(composeFilePath, []byte("foo bar"), 0x644); err != nil {
		t.Fatal(err)
	}
	appStatus := app.GetRunningStatus(t)
	if appStatus.State != "running with an invalid app bundle" {
		t.Fatalf("expected `running with an invalid app bundle`, got: %s", appStatus.State)
	}
	// Install app again, so it can be stopped without any error
	app.Install(t)
}

func TestAppRunIfPulledBySkopeo(t *testing.T) {
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

	app.PullAppImagesWithSkopeo(t)
	defer app.Remove(t)
	app.CheckFetched(t)

	app.Install(t)
	defer app.Uninstall(t)

	app.Run(t)
	app.CheckRunning(t)
	defer app.Stop(t)
}
