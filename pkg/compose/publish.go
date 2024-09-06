//go:build publish

package compose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/compose-spec/compose-go/loader"
	compose "github.com/compose-spec/compose-go/types"
	"github.com/docker/docker/client"
	"github.com/foundriesio/composeapp/internal"
	"github.com/opencontainers/go-digest"
)

func getClient() (*client.Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return nil, err
	}
	cli.NegotiateAPIVersion(context.Background())
	return cli, nil
}

func loadProj(ctx context.Context, appName string, file string, content []byte) (*compose.Project, error) {
	env := make(map[string]string)
	for _, val := range os.Environ() {
		parts := strings.Split(val, "=")
		env[parts[0]] = parts[1]
	}

	var files []compose.ConfigFile
	files = append(files, compose.ConfigFile{Filename: file, Content: content})
	return loader.LoadWithContext(ctx, compose.ConfigDetails{
		WorkingDir:  ".",
		ConfigFiles: files,
		Environment: env,
	}, func(options *loader.Options) {
		options.SetProjectName(appName, true)
	})
}

func DoPublish(ctx context.Context, appName string, file, target, digestFile string, dryRun bool, archList []string, pinnedImages map[string]digest.Digest, layersMetaFile string) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	config, err := loader.ParseYAML(b)
	if err != nil {
		return err
	}
	cli, err := getClient()
	if err != nil {
		return err
	}

	proj, err := loadProj(ctx, appName, file, b)
	if err != nil {
		return err
	}

	fmt.Println("= Pinning service images...")
	svcs, ok := config["services"]
	if !ok {
		return errors.New("Unable to find 'services' section of compose file")
	}
	if err := internal.PinServiceImages(cli, ctx, svcs.(map[string]interface{}), proj, pinnedImages); err != nil {
		return err
	}

	fmt.Println("== Hashing services...")
	if err := internal.PinServiceConfigs(cli, ctx, svcs.(map[string]interface{}), proj); err != nil {
		return err
	}

	fmt.Println("= Getting app layers metadata...")
	appLayers, err := GetLayers(ctx, svcs.(map[string]interface{}), archList)
	if err != nil {
		return err
	}

	if len(appLayers) == 0 {
		return fmt.Errorf("none of the factory architectures %q are supported by App images", archList)
	}

	// TODO: this check is needed in order to overcome the aklite's check on the maximum manifest size (2048)
	// Once the new version of aklite is deployed (max manifest size = 16K) then this check can be removed or MaxArchNumb increased
	if len(appLayers) > internal.MaxArchNumb {
		return fmt.Errorf("app cannot support more than %d architectures, found %d", internal.MaxArchNumb, len(appLayers))
	}

	fmt.Println("= Posting app layers manifests...")
	layerManifests, err := PostAppLayersManifests(ctx, target, appLayers, dryRun)
	if err != nil {
		return err
	}

	var appLayersMetaBytes []byte
	if len(layersMetaFile) > 0 {
		fmt.Println("= Getting app layers metadata...")
		appLayersMetaBytes, err = GetAppLayersMeta(layersMetaFile, appLayers)
		if err != nil {
			fmt.Printf("= Failed to get app layers metadata: %s\n", err.Error())
		}
	}

	fmt.Println("= Publishing app...")
	dgst, err := internal.CreateApp(ctx, config, target, dryRun, layerManifests, appLayersMetaBytes)
	if err != nil {
		return err
	}
	if len(digestFile) > 0 {
		return os.WriteFile(digestFile, []byte(dgst), 0o640)
	}
	return nil
}
