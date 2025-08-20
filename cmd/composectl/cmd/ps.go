package composectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/docker/docker/api/types/container"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
	"path"
)

const (
	WorkingDirLabel = "com.docker.compose.project.working_dir"
	ServiceLabel    = "com.docker.compose.service"
)

type (
	Service struct {
		Name   string `json:"name"`
		Image  string `json:"image"`
		Hash   string `json:"hash"`
		CtrID  string `json:"ctr-id"`
		State  string `json:"state"`
		Status string `json:"status"`
		Health string `json:"health,omitempty"`
	}
	App struct {
		URI           string                `json:"uri"`
		Name          string                `json:"name"`
		State         string                `json:"state"`
		Services      []*Service            `json:"services"`
		InStore       bool                  `json:"in_store"`
		BundleErrors  compose.AppBundleErrs `json:"bundle_errors"`
		MissingImages []string              `json:"missing_images"`
	}

	psOptions struct {
		Format       string
		CheckInstall bool
	}
)

func init() {
	psCmd := &cobra.Command{
		Use:   "ps [<ref>]...",
		Short: "ps [<ref>]...",
		Long:  ``,
	}
	opts := psOptions{}
	psCmd.Flags().StringVar(&opts.Format, "format", "table", "Format the output. Values: [table | json]")
	psCmd.Flags().BoolVar(&opts.CheckInstall, "install", true, "Also check if app is installed")
	psCmd.Run = func(cmd *cobra.Command, args []string) {
		if opts.Format != "table" && opts.Format != "json" {
			DieNotNil(fmt.Errorf("invalid value of `--format` option: %s", opts.Format))
		}
		psApps(cmd, args, &opts)
	}

	rootCmd.AddCommand(psCmd)
}

func psApps(cmd *cobra.Command, args []string, opts *psOptions) {
	runningApps := getAllAppStatuses(cmd.Context(), opts.Format == "json")
	if len(args) == 0 {
		printAppStatuses(runningApps, opts.Format)
	} else {
		appStatuses := getAppsStatus(cmd.Context(), args, runningApps, opts.CheckInstall, opts.Format == "json")
		printAppStatuses(appStatuses, opts.Format)
	}

}

func getAppsStatus(ctx context.Context, appRefs []string, runningApps map[string]*App, checkInstall bool, quiet bool) map[string]*App {
	store, err := v1.NewAppStore(config.StoreRoot, config.Platform)
	DieNotNil(err)
	apps := map[string]compose.App{}
	appStatuses := map[string]*App{}

	for _, appRef := range appRefs {
		appStatus := &App{
			URI:     appRef,
			InStore: false,
			State:   "undefined",
		}
		app, err := v1.NewAppLoader().LoadAppTree(ctx, store, platforms.OnlyStrict(config.Platform), appRef)
		if err == nil {
			appStatus.Name = app.Name()
			appStatus.InStore = true
			appStatus.State = "found in store"
		} else {
			if errors.Is(err, compose.ErrAppNotFound) {
				appStatus.State = "not found in store"
			} else {
				appStatus.State = err.Error()
			}
			if ref, err := compose.ParseAppRef(appRef); err == nil {
				appStatus.Name = ref.Name
			} else {
				appStatus.Name = "unknown app name for " + appRef
			}
		}
		appStatuses[appRef] = appStatus
		apps[appRef] = app
	}

	for appRef, app := range apps {
		if !appStatuses[appRef].InStore {
			// If the app is not in store then we don't need to check if it is running
			continue
		}
		runningApp, ok := runningApps[app.Name()]
		if !ok {
			appStatuses[appRef].State = "not running"
			continue
		}
		if !runningApp.InStore {
			// Since we iterate over apps stored in apps, it is not possible that running app is not in store
			// in this context/loop.
			if !quiet {
				fmt.Printf("ERR: Running app is not found in the store: %s\n", appRef)
			}
			continue
		}
		// If the running app URI is empty and the app is found in the store then it
		// means that more than one version of the app are in the store.
		// In this case we assume that a caller/user would like to check status of the app version
		// specified via `appRefs`.
		if len(runningApp.URI) > 0 && runningApp.URI != appRef {
			appStatuses[appRef].State = "running another app version than found in the store" + runningApp.URI
			continue
		}

		composeTree := app.GetComposeRoot()
		if composeTree == nil {
			panic(fmt.Errorf("failed to get app tree for %s", app.Name()))
		}
		var appServices []*Service
		appState := "running"
		for _, imageNode := range composeTree.Children {
			imageUri := imageNode.Ref()

			var foundSrvs []*Service
			for _, srv := range runningApp.Services {
				if srv.Image == imageUri {
					foundSrvs = append(foundSrvs, srv)
				}
			}
			if len(foundSrvs) == 0 {
				appServices = append(appServices, &Service{
					Image: imageUri,
					State: "missing",
				})
				appState = "not running"
				continue
			}
			appServiceHash := imageNode.Descriptor.Annotations[v1.AppServiceHashLabelKey]
			var foundMatchingSrv *Service
			for _, fsrv := range foundSrvs {
				if len(fsrv.Hash) == 0 {
					appServices = append(appServices, &Service{
						Image:  imageUri,
						State:  "unknown",
						Status: "no config hash",
					})
					appState = "unknown"
					continue
				}
				if fsrv.Hash == appServiceHash {
					foundMatchingSrv = fsrv
					break
				}
			}
			if foundMatchingSrv != nil {
				appServices = append(appServices, foundMatchingSrv)
				if len(foundMatchingSrv.State) == 0 || foundMatchingSrv.State != "running" {
					appState = "not running"
				}
			} else {
				appServices = append(appServices, &Service{
					Image:  imageUri,
					State:  "missing",
					Status: "config hash mismatch",
				})
				appState = "not running"
			}
		}
		appStatuses[appRef].State = appState
		appStatuses[appRef].Services = appServices
	}

	if checkInstall {
		var appsToCheckInstall []string
		for appRef, appStatus := range appStatuses {
			if appStatus.InStore {
				appsToCheckInstall = append(appsToCheckInstall, appRef)
			}
		}
		checkInstallResult, err := checkIfInstalled(ctx, appsToCheckInstall, store, config.DockerHost)
		DieNotNil(err)
		for app, ir := range checkInstallResult {
			appStatuses[app].BundleErrors = ir.BundleErrors
			appStatuses[app].MissingImages = ir.MissingImages
			if appStatuses[app].State == "running" && len(ir.BundleErrors) > 0 {
				appStatuses[app].State = "running with an invalid app bundle"
			}
		}
	}
	return appStatuses
}

func printAppStatuses(appStatuses map[string]*App, format string) {
	if format == "json" {
		if b, err := json.MarshalIndent(appStatuses, "", "  "); err == nil {
			fmt.Println(string(b))
		} else {
			DieNotNil(err)
		}
	} else {
		for _, app := range appStatuses {
			fmt.Printf("%s (%s) -> %s\n", app.Name, app.State, app.URI)
			for _, srv := range app.Services {
				id := "------------"
				if len(srv.CtrID) > 0 {
					id = srv.CtrID[:12]
				}
				hash := "------------"
				if len(srv.Hash) > 0 {
					hash = srv.Hash[:12]
				}
				fmt.Printf("  - %s\t%s\t%s\t%s\t%s\t%s\n", srv.Name, srv.Image, id, hash, srv.State, srv.Status)
			}
		}
	}
}

func checkAppInStore(storeApps []*compose.AppRef, appName string) []*compose.AppRef {
	var foundApps []*compose.AppRef
	for _, a := range storeApps {
		if a.Name == appName {
			// TODO: handle the case when there are more than two one version of the same app in the store
			foundApps = append(foundApps, a)
		}
	}
	return foundApps
}
func getAllAppStatuses(ctx context.Context, quiet bool) map[string]*App {
	store, err := v1.NewAppStore(config.StoreRoot, config.Platform)
	DieNotNil(err)
	storeApps, err := store.ListApps(ctx)
	DieNotNil(err)
	dockerClient, err := compose.GetDockerClient(config.DockerHost)
	DieNotNil(err)
	containers, err := dockerClient.ContainerList(ctx, container.ListOptions{All: true})
	DieNotNil(err)

	foundApps := map[string]*App{}
	for _, c := range containers {
		// check if container is part of a compose project
		var workDir string
		var foundProjectWorkDir bool
		if workDir, foundProjectWorkDir = c.Labels[WorkingDirLabel]; !foundProjectWorkDir {
			if !quiet {
				fmt.Printf("container is not part of any compose app; ID: %s, image: %s\n", c.ID, c.Image)
			}
			continue
		}
		health := compose.GetServiceHealth(ctx, dockerClient, c.ID)

		appName := path.Base(workDir)
		srv := &Service{
			Name:   c.Labels[ServiceLabel],
			Image:  c.Image,
			Hash:   c.Labels[v1.AppServiceHashLabelKey],
			CtrID:  c.ID,
			State:  c.State,
			Status: c.Status,
			Health: health,
		}
		if app, ok := foundApps[appName]; ok {
			app.Services = append(app.Services, srv)
			if c.State != "running" {
				app.State = "not running"
			}
		} else {
			var appUri string
			appsFoundInStore := checkAppInStore(storeApps, appName)
			if len(appsFoundInStore) == 1 {
				appUri = appsFoundInStore[0].String()
			}
			foundApps[appName] = &App{
				URI:      appUri,
				Name:     appName,
				State:    c.State,
				Services: []*Service{srv},
				InStore:  len(appsFoundInStore) > 0,
			}
		}
	}
	return foundApps
}
