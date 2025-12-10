package e2e_tests

import (
	"context"
	"testing"

	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/foundriesio/composeapp/pkg/update"
	f "github.com/foundriesio/composeapp/test/fixtures"
)

func finalizeUpdate(t *testing.T, ctx context.Context, ur update.Runner) {
	switch ur.Status().State {
	case update.StateInitializing,
		update.StateInitialized,
		update.StateFetching,
		update.StateFetched,
		update.StateInstalled,
		update.StateInstalling:
		f.Check(t, ur.Cancel(ctx))
		if ur.Status().State != update.StateCanceled {
			t.Fatalf("update not cancelled: %s\n", ur.Status().State)
		}
	case update.StateStarting,
		update.StateStarted:
		f.Check(t, ur.Complete(ctx))
		if ur.Status().State != update.StateCompleted {
			t.Fatalf("update not completed: %s\n", ur.Status().State)
		}
	}
}

func TestAppUpdate(t *testing.T) {
	appComposeDef := `
services:
  srvs-01:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
    ports:
    - 8080:80
`
	app := f.NewApp(t, appComposeDef)
	app.Publish(t)

	cfg := f.NewTestConfig(t)
	updateRunner, err := update.NewUpdate(cfg, "target-1")
	f.Check(t, err)

	ctx := context.Background()

	f.Check(t, updateRunner.Init(ctx, []string{app.PublishedUri}))
	updateStatus := updateRunner.Status()
	if updateStatus.State != update.StateInitialized {
		t.Fatal("update not initialized")
	}
	if updateStatus.Progress != 100 {
		t.Fatalf("update is not initiated for 100%%: %d\n", updateStatus.Progress)
	}

	f.Check(t, updateRunner.Fetch(ctx))
	defer app.Remove(t)
	updateStatus = updateRunner.Status()
	if updateStatus.State != update.StateFetched {
		t.Fatal("update not fetched")
	}
	if updateStatus.Progress != 100 {
		t.Fatalf("update is not fetched for 100%%: %d\n", updateStatus.Progress)
	}

	f.Check(t, updateRunner.Install(ctx))
	defer app.Uninstall(t)
	updateStatus = updateRunner.Status()
	if updateStatus.State != update.StateInstalled {
		t.Fatal("update not installed")
	}
	if updateStatus.Progress != 100 {
		t.Fatalf("update is not installed for 100%%: %d\n", updateStatus.Progress)
	}

	f.Check(t, updateRunner.Start(ctx))
	defer app.Stop(t)
	if updateRunner.Status().State != update.StateStarted {
		t.Fatal("update not started")
	}
	updateStatus = updateRunner.Status()
	if updateStatus.Progress != 100 {
		t.Fatalf("update is not started for 100%%: %d\n", updateStatus.Progress)
	}

	defer finalizeUpdate(t, ctx, updateRunner)
}

func TestAppSync(t *testing.T) {
	appComposeDef := `
services:
 srvs-01:
   image: registry:5000/factory/runner-image:v0.1
   command: sh -c "while true; do sleep 60; done"
   ports:
   - 8080:80
`
	app := f.NewApp(t, appComposeDef)
	app.Publish(t)

	cfg := f.NewTestConfig(t)
	ctx := context.Background()

	s, err := compose.CheckAppsStatus(ctx, cfg, []string{app.PublishedUri})
	f.Check(t, err)
	if s.AreFetched() || s.AreInstalled() || s.AreRunning() {
		t.Fatalf("apps are not supposed to be fetched nor installed nor running")
	}

	updateRunner, err := update.NewUpdate(cfg, "target-1")
	f.Check(t, err)

	f.Check(t, updateRunner.Init(ctx, []string{app.PublishedUri}))
	if updateRunner.Status().State != update.StateInitialized {
		t.Fatal("update not initialized")
	}
	f.Check(t, updateRunner.Fetch(ctx))
	defer app.Remove(t)

	// App is fetched but is not installed and is not running
	s, err = compose.CheckAppsStatus(ctx, cfg, []string{app.PublishedUri})
	f.Check(t, err)
	if !s.AreFetched() {
		t.Fatalf("apps are supposed to be fetched")
	}
	if s.AreInstalled() || s.AreRunning() {
		t.Fatalf("apps are not suppoped to be installed nor running")
	}

	f.Check(t, updateRunner.Install(ctx))
	defer app.Uninstall(t)
	if updateRunner.Status().State != update.StateInstalled {
		t.Fatal("update not installed")
	}
	s, err = compose.CheckAppsStatus(ctx, cfg, []string{app.PublishedUri})
	f.Check(t, err)
	if !s.AreFetched() || !s.AreInstalled() {
		t.Fatalf("apps are supposed to be fetched and installed")
	}
	if s.AreRunning() {
		t.Fatalf("apps are not suppoped to be installed nor running")
	}

	f.Check(t, updateRunner.Start(ctx))
	defer app.Stop(t)
	if updateRunner.Status().State != update.StateStarted {
		t.Fatal("update not started")
	}

	s, err = compose.CheckAppsStatus(ctx, cfg, []string{app.PublishedUri})
	f.Check(t, err)
	if !s.AreFetched() || !s.AreInstalled() || !s.AreRunning() {
		t.Fatalf("apps are supposed to be fetched and installed and running")
	}

	defer finalizeUpdate(t, ctx, updateRunner)
}

func TestAppControl(t *testing.T) {
	appComposeDef01 := `
services:
 srvs-01:
   image: registry:5000/factory/runner-image:v0.1
   command: sh -c "while true; do sleep 60; done"
   ports:
   - 8080:80
`
	appComposeDef02 := `
services:
  busybox:
    image: ghcr.io/foundriesio/busybox:1.36
    command: sh -c "while true; do sleep 60; done"
`
	var appURIs []string
	for _, appDef := range []string{appComposeDef01, appComposeDef02} {
		app := f.NewApp(t, appDef)
		app.Publish(t)
		appURIs = append(appURIs, app.PublishedUri)
	}

	cfg := f.NewTestConfig(t)
	ctx := context.Background()

	updateRunner, err := update.NewUpdate(cfg, "target-2")
	f.Check(t, err)

	f.Check(t, updateRunner.Init(ctx, appURIs))
	f.Check(t, updateRunner.Fetch(ctx))
	defer func() {
		defer func() {
			f.Check(t, compose.RemoveApps(ctx, cfg, appURIs))
		}()
	}()

	// Make sure the fetched apps are listed among the store apps returned by
	// the App listing API call
	foundApps, err := compose.ListApps(ctx, cfg)
	f.Check(t, err)
	appsMap := make(map[string]bool)
	for _, foundApp := range foundApps {
		appsMap[foundApp.Ref().String()] = true
	}
	for _, uri := range appURIs {
		if _, ok := appsMap[uri]; !ok {
			t.Fatalf("the fetched app is not listed among the store apps: %s", uri)
		}
	}

	f.Check(t, updateRunner.Install(ctx))
	defer func() {
		f.Check(t, compose.UninstallApps(ctx, cfg, appURIs, compose.WithImagePruning()))
	}()

	f.Check(t, updateRunner.Start(ctx))
	defer func() {
		f.Check(t, compose.StopApps(ctx, cfg, appURIs))
	}()

	defer finalizeUpdate(t, ctx, updateRunner)
}

func TestAppSyncAndRemove(t *testing.T) {
	appComposeDef01 := `
services:
 srvs-01:
   image: registry:5000/factory/runner-image:v0.1
   command: sh -c "while true; do sleep 60; done"
   ports:
   - 8080:80
`
	appComposeDef01v1 := `
services:
 srvs-02:
   image: registry:5000/factory/runner-image:v0.1
   command: sh -c "while true; do sleep 60; done"
   ports:
   - 8081:81
`
	appComposeDef02 := `
services:
  busybox:
    image: ghcr.io/foundriesio/busybox:1.36
    command: sh -c "while true; do sleep 60; done"
`
	var appURIs []string
	for _, appDef := range []string{appComposeDef01, appComposeDef02} {
		app := f.NewApp(t, appDef)
		app.Publish(t)
		appURIs = append(appURIs, app.PublishedUri)
	}

	cfg := f.NewTestConfig(t)
	ctx := context.Background()

	updateRunner, err := update.NewUpdate(cfg, "target-3")
	f.Check(t, err)

	f.Check(t, updateRunner.Init(ctx, appURIs))
	f.Check(t, updateRunner.Fetch(ctx))
	f.Check(t, updateRunner.Install(ctx))
	f.Check(t, updateRunner.Start(ctx))
	f.Check(t, updateRunner.Complete(ctx))

	// change app1 and publish it
	app := f.NewApp(t, appComposeDef01v1)
	app.Publish(t)
	appURIs = []string{app.PublishedUri}

	appsStatus, err := compose.CheckAppsStatus(ctx, cfg, nil)
	f.Check(t, err)
	var appsToRemove []string
	for _, a := range appsStatus.Apps {
		if a.Ref().String() != app.PublishedUri {
			appsToRemove = append(appsToRemove, a.Ref().String())
		}
	}

	updateRunner, err = update.NewUpdate(cfg, "target-4")
	f.Check(t, err)

	f.Check(t, updateRunner.Init(ctx, appURIs))
	f.Check(t, updateRunner.Fetch(ctx))
	defer func() {
		defer func() {
			f.Check(t, compose.RemoveApps(ctx, cfg, appURIs))
		}()
	}()

	f.Check(t, updateRunner.Install(ctx))
	defer func() {
		f.Check(t, compose.UninstallApps(ctx, cfg, appURIs, compose.WithImagePruning()))
	}()

	// Stop apps to be removed
	f.Check(t, compose.StopApps(ctx, cfg, appsToRemove))

	f.Check(t, updateRunner.Start(ctx))
	defer func() {
		f.Check(t, compose.StopApps(ctx, cfg, appURIs))
	}()

	// Uninstall apps that are not part of target
	f.Check(t, compose.UninstallApps(ctx, cfg, appsToRemove, compose.WithImagePruning()))
	// Complete update
	f.Check(t, updateRunner.Complete(ctx))

	// Remove apps that are not part of target
	f.Check(t, compose.RemoveApps(ctx, cfg, appsToRemove))

	appsStatus, err = compose.CheckAppsStatus(ctx, cfg, nil)
	f.Check(t, err)
	if len(appsStatus.Apps) != 1 || appsStatus.Apps[0].Ref().String() != app.PublishedUri {
		t.Fatalf("invalid apps status; expected just one app: %s\n", app.PublishedUri)
	}
	if !appsStatus.AreFetched() {
		t.Fatalf("the update app is not fetched")
	}
	if !appsStatus.AreInstalled() {
		t.Fatalf("the update app is not installed")
	}
	if !appsStatus.AreRunning() {
		t.Fatalf("the update app is not running")
	}

	defer finalizeUpdate(t, ctx, updateRunner)
}

func TestAppSyncAndPrune(t *testing.T) {
	appComposeDef01 := `
services:
 srvs-01:
   image: registry:5000/factory/runner-image:v0.1
   command: sh -c "while true; do sleep 60; done"
   ports:
   - 8080:80
`
	appComposeDef01v1 := `
services:
 srvs-02:
   image: registry:5000/factory/runner-image:v0.1
   command: sh -c "while true; do sleep 60; done"
   ports:
   - 8081:81
`
	appComposeDef02 := `
services:
  busybox:
    image: ghcr.io/foundriesio/busybox:1.36
    command: sh -c "while true; do sleep 60; done"
`
	var appURIs []string
	for _, appDef := range []string{appComposeDef01, appComposeDef02} {
		app := f.NewApp(t, appDef)
		app.Publish(t, f.WithAppBundleIndexes(false))
		appURIs = append(appURIs, app.PublishedUri)
	}

	cfg := f.NewTestConfig(t)
	ctx := context.Background()

	updateRunner, err := update.NewUpdate(cfg, "target-3")
	f.Check(t, err)

	f.Check(t, updateRunner.Init(ctx, appURIs))
	f.Check(t, updateRunner.Fetch(ctx))
	f.Check(t, updateRunner.Install(ctx))
	f.Check(t, updateRunner.Start(ctx))
	f.Check(t, updateRunner.Complete(ctx))

	// change app1 and publish it
	app := f.NewApp(t, appComposeDef01v1)
	app.Publish(t, f.WithAppBundleIndexes(false))
	appURIs = []string{app.PublishedUri}

	appsStatus, err := compose.CheckAppsStatus(ctx, cfg, nil)
	f.Check(t, err)
	var appsToRemove []string
	for _, a := range appsStatus.Apps {
		if a.Ref().String() != app.PublishedUri {
			appsToRemove = append(appsToRemove, a.Ref().String())
		}
	}

	updateRunner, err = update.NewUpdate(cfg, "target-4")
	f.Check(t, err)

	f.Check(t, updateRunner.Init(ctx, appURIs))
	f.Check(t, updateRunner.Fetch(ctx))
	defer func() {
		defer func() {
			f.Check(t, compose.RemoveApps(ctx, cfg, appURIs))
		}()
	}()

	f.Check(t, updateRunner.Install(ctx))
	defer func() {
		f.Check(t, compose.UninstallApps(ctx, cfg, appURIs, compose.WithImagePruning()))
	}()

	// Stop apps to be removed
	f.Check(t, compose.StopApps(ctx, cfg, appsToRemove))

	f.Check(t, updateRunner.Start(ctx))
	defer func() {
		f.Check(t, compose.StopApps(ctx, cfg, appURIs))
	}()

	f.Check(t, updateRunner.Complete(ctx))

	// Remove and uninstall apps that are not part of target/update
	f.Check(t, compose.UninstallApps(ctx, cfg, appsToRemove))
	f.Check(t, compose.RemoveApps(ctx, cfg, appsToRemove))

	appsStatus, err = compose.CheckAppsStatus(ctx, cfg, nil)
	f.Check(t, err)
	if len(appsStatus.Apps) != 1 || appsStatus.Apps[0].Ref().String() != app.PublishedUri {
		t.Fatalf("invalid apps status; expected just one app: %s\n", app.PublishedUri)
	}
	if !appsStatus.AreFetched() {
		t.Fatalf("the update app is not fetched")
	}
	if !appsStatus.AreInstalled() {
		t.Fatalf("the update app is not installed")
	}
	if !appsStatus.AreRunning() {
		t.Fatalf("the update app is not running")
	}

	defer finalizeUpdate(t, ctx, updateRunner)
}

func TestAppUpdateAndRemove(t *testing.T) {
	appComposeDef01 := `
services:
 srvs-01:
   image: registry:5000/factory/runner-image:v0.1
   command: sh -c "while true; do sleep 60; done"
   ports:
   - 8080:80
`
	appComposeDef02 := `
services:
  busybox:
    image: ghcr.io/foundriesio/busybox:1.36
    command: sh -c "while true; do sleep 60; done"
`
	var appURIs []string
	for _, appDef := range []string{appComposeDef01, appComposeDef02} {
		app := f.NewApp(t, appDef)
		app.Publish(t)
		appURIs = append(appURIs, app.PublishedUri)
	}

	cfg := f.NewTestConfig(t)
	ctx := context.Background()

	updateRunner, err := update.NewUpdate(cfg, "target-10")
	f.Check(t, err)

	// do update
	f.Check(t, updateRunner.Init(ctx, appURIs))
	f.Check(t, updateRunner.Fetch(ctx))
	f.Check(t, updateRunner.Install(ctx))
	f.Check(t, updateRunner.Start(ctx))
	f.Check(t, updateRunner.Complete(ctx))

	// stop, uninstall, and remove all apps
	f.Check(t, compose.StopApps(ctx, cfg, appURIs))
	f.Check(t, compose.UninstallApps(ctx, cfg, appURIs, compose.WithImagePruning()))
	f.Check(t, compose.RemoveApps(ctx, cfg, appURIs))
}

func TestAppUpdateFailure(t *testing.T) {
	appComposeDefBroken := `
services:
  srvs-01:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
    ports:
    - 8080:80
  srvs-02:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
    ports:
    - 8080:80 # port conflict with srvs-01 to make the update fail
`
	appComposeDef := `
services:
  srvs-01:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
    ports:
    - 8080:80
`
	badApp := f.NewApp(t, appComposeDefBroken)
	badApp.Publish(t)

	cfg := f.NewTestConfig(t)

	var updateRunner update.Runner
	var err error
	ctx := context.Background()

	// Try to run the update 3 times, it should fail every time
	for i := 1; i < 4; i++ {
		updateRunner, err = update.NewUpdate(cfg, "target-1")
		f.Check(t, err)

		f.Check(t, updateRunner.Init(ctx, []string{badApp.PublishedUri}))
		f.Check(t, updateRunner.Fetch(ctx))
		f.Check(t, updateRunner.Install(ctx))
		if err := updateRunner.Start(ctx); err == nil {
			t.Fatal("update start is expected to fail")
		}
		if updateRunner.Status().State != update.StateFailed {
			t.Fatalf("update is supposed to be in failed state, but it's in %s\n", updateRunner.Status().State)
		}
		failureCount, err := update.CountFailedUpdates(cfg, "target-1")
		f.Check(t, err)
		if failureCount != i {
			t.Fatalf("there is/are %d failed updates, expected %d\n", failureCount, i)
		}
	}
	// We need to run "bad" app stop before trying to install a new valid app because one of the bad app containers
	// could have started before the other failed to start, and it would interfere with the new app installation.
	badApp.Stop(t)
	defer badApp.Uninstall(t)
	defer badApp.Remove(t)

	app := f.NewApp(t, appComposeDef)
	app.Publish(t)

	updateRunner, err = update.NewUpdate(cfg, "target-1")
	defer finalizeUpdate(t, ctx, updateRunner)
	f.Check(t, err)
	f.Check(t, updateRunner.Init(ctx, []string{app.PublishedUri}))
	f.Check(t, updateRunner.Fetch(ctx))
	defer app.Remove(t)
	f.Check(t, updateRunner.Install(ctx))
	defer app.Uninstall(t)
	f.Check(t, updateRunner.Start(ctx))
	defer app.Stop(t)
	f.Check(t, updateRunner.Complete(ctx))
	failureCount, err := update.CountFailedUpdates(cfg, "target-1")
	f.Check(t, err)
	if failureCount != 0 {
		t.Fatalf("there is/are %d failed updates, expected %d\n", failureCount, 0)
	}
}

func TestAppPruning(t *testing.T) {
	appComposeDef01 := `
services:
  srvs-01:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
    ports:
    - 8081:81
  busybox-1:
    image: ghcr.io/foundriesio/busybox:1.36
    command: sh -c "while true; do sleep 60; done"
  busybox-2:
    image: ghcr.io/foundriesio/busybox:1.36-multiarch
    command: sh -c "while true; do sleep 120; done"
`
	appComposeDef02 := `
services:
  srvs-01:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
    ports:
    - 8082:82
  busybox-1:
    image: ghcr.io/foundriesio/busybox:1.36
    command: sh -c "while true; do sleep 70; done"
  busybox-2:
    image: ghcr.io/foundriesio/busybox:1.36-multiarch
    command: sh -c "while true; do sleep 110; done"
`
	var appURIs []string
	for _, appDef := range []string{appComposeDef01, appComposeDef02} {
		app := f.NewApp(t, appDef)
		app.Publish(t)
		appURIs = append(appURIs, app.PublishedUri)
	}

	cfg := f.NewTestConfig(t)
	ctx := context.Background()

	updateRunner, err := update.NewUpdate(cfg, "target-10")
	f.Check(t, err)

	// do update
	f.Check(t, updateRunner.Init(ctx, appURIs))
	f.Check(t, updateRunner.Fetch(ctx))
	f.Check(t, updateRunner.Install(ctx))
	f.Check(t, updateRunner.Start(ctx))
	f.Check(t, updateRunner.Complete(ctx, update.CompleteWithPruning()))

	// check that both apps are running
	appsStatus, err := compose.CheckAppsStatus(ctx, cfg, nil)
	f.Check(t, err)
	if !appsStatus.AreRunning() {
		t.Fatal("apps are expected to be running")
	}

	// do sync update, remove the second app
	updateRunner, err = update.NewUpdate(cfg, "target-10")
	f.Check(t, err)
	oneAppURI := []string{appURIs[0]}
	f.Check(t, updateRunner.Init(ctx, oneAppURI, update.WithInitCheckStatus(false)))
	f.Check(t, updateRunner.Fetch(ctx))
	// Stop apps before installing the update, which effectively removes the second app
	f.Check(t, compose.StopApps(ctx, cfg, appURIs))
	f.Check(t, updateRunner.Install(ctx))
	// Start only the first app
	f.Check(t, updateRunner.Start(ctx))
	// Complete with pruning to remove the second app
	f.Check(t, updateRunner.Complete(ctx, update.CompleteWithPruning()))

	appsStatus, err = compose.CheckAppsStatus(ctx, cfg, nil)
	f.Check(t, err)
	if !appsStatus.AreRunning() {
		t.Fatal("app is expected to be running")
	}
	if len(appsStatus.Apps) > 1 || len(appsStatus.Apps) == 0 {
		t.Fatalf("only one app is expected to be running, found %d", len(appsStatus.Apps))
	}
	if appsStatus.Apps[0].Ref().String() != appURIs[0] {
		t.Fatalf("expected app URI %s, found %s", appURIs[0], appsStatus.Apps[0].Ref().String())
	}

	// stop, uninstall, and remove all apps
	f.Check(t, compose.StopApps(ctx, cfg, oneAppURI))
	f.Check(t, compose.UninstallApps(ctx, cfg, oneAppURI, compose.WithImagePruning()))
	f.Check(t, compose.RemoveApps(ctx, cfg, oneAppURI))
}
