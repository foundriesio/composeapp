package e2e_tests

import (
	"bytes"
	"encoding/json"
	composectl "github.com/foundriesio/composeapp/cmd/composectl/cmd"
	"github.com/foundriesio/composeapp/pkg/compose"
	f "github.com/foundriesio/composeapp/test/fixtures"
	"os"
	"path"
	"strings"
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
    - VERSION=0.1
`
	appComposeDef02 := `
services:
  srvs-01:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
    environment:
    - VERSION=0.2
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

	cfg := f.NewTestConfig(t)
	composeFilePath := path.Join(cfg.GetAppComposeDir(app.Name), "docker-compose.yml")
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

func TestAppWithoutBundleIndexes(t *testing.T) {
	appComposeDef := `
services:
  srvs-01:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
    environment:
    - VERSION=0.1
`
	appComposeDef02 := `
services:
  srvs-01:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
    environment:
    - VERSION=0.2
`
	app := f.NewApp(t, appComposeDef)
	app.Publish(t, f.WithAppBundleIndexes(false))

	app02 := f.NewApp(t, appComposeDef02, app.Name)
	app02.Publish(t, f.WithAppBundleIndexes(false))

	app.Pull(t)
	defer app.Remove(t)

	app02.Pull(t)
	defer app02.Remove(t)

	app.Install(t)
	defer app.Uninstall(t)

	app.Up(t)
	defer app.Stop(t)
	// Check that the app is running
	app.CheckRunning(t)

	// Make sure that another app with the same image is not running
	appRunStatus := app02.GetRunningStatus(t)
	if appRunStatus.State != "not running" {
		t.Fatalf("expected %s is at state `not running`, got: %s", app02.Name, appRunStatus.State)
	}

	installCheckRes := app02.GetInstallCheckRes(t)
	if len(installCheckRes.BundleErrors) != 1 {
		t.Fatalf("expected app bundle integrity error, got: %d", len(installCheckRes.BundleErrors))
	}
	expectedErrMsg := "blob digest mismatch"
	for _, errMsg := range installCheckRes.BundleErrors {
		if !strings.HasPrefix(errMsg, expectedErrMsg) {
			t.Fatalf("unexpected app bundle integrity error, got: %s, expected: %s", errMsg, expectedErrMsg)
		}
		break
	}
}

func TestCheckAndPullOffline(t *testing.T) {
	appComposeDef := `
services:
  srvs-01:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
`
	app := f.NewApp(t, appComposeDef)
	app.Publish(t)

	if output, err := f.RunCmdOffline(t, "check", app.PublishedUri); err == nil {
		t.Fatalf("expected check of a not fetched app to fail offline; output: %s", output)
	}

	app.Pull(t)
	defer app.Remove(t)

	output, err := f.RunCmdOffline(t, "check", app.PublishedUri, "--format", "json")
	f.Checkf(t, err, "check of a fetched app failed offline: %s\n", output)
	checkResult := composectl.CheckAndInstallResult{}
	f.Check(t, json.Unmarshal(output, &checkResult))
	if len(checkResult.FetchCheck.MissingBlobs) > 0 {
		t.Errorf("there are missing app blobs: %+v\n", checkResult.FetchCheck.MissingBlobs)
	}

	output, err = f.RunCmdOffline(t, "pull", app.PublishedUri)
	f.Checkf(t, err, "pull of a fetched app failed offline: %s\n", output)
}

func TestCheckAndPullRepairCorruptManifest(t *testing.T) {
	appComposeDef := `
services:
  srvs-01:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
`
	app := f.NewApp(t, appComposeDef)
	app.Publish(t)

	app.Pull(t)
	defer app.Remove(t)

	// Overwrite the stored app manifest with bytes of the same size but another digest.
	appRef, err := compose.ParseAppRef(app.PublishedUri)
	f.Check(t, err)
	manifestPath := path.Join(compose.GetBlobsRootFor(f.AppStoreRoot), appRef.Digest.Encoded())
	manifest, err := os.ReadFile(manifestPath)
	f.Check(t, err)
	f.Check(t, os.WriteFile(manifestPath, bytes.Repeat([]byte("x"), len(manifest)), 0644))

	output := f.RunCmd(t, "check", app.PublishedUri, "--format", "json")
	checkResult := composectl.CheckAndInstallResult{}
	f.Check(t, json.Unmarshal(output, &checkResult))
	if _, ok := checkResult.FetchCheck.MissingBlobs[appRef.Digest]; !ok {
		t.Fatalf("expected the corrupt app manifest among missing blobs, got: %+v\n", checkResult.FetchCheck.MissingBlobs)
	}

	app.Pull(t)
	app.CheckFetched(t)
	output, err = f.RunCmdOffline(t, "check", app.PublishedUri, "--format", "json")
	f.Checkf(t, err, "check of a repaired app failed offline: %s\n", output)
}
