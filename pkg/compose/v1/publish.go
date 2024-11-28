//go:build publish

package v1

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/docker/distribution"
	"github.com/docker/distribution/manifest/manifestlist"
	"github.com/docker/distribution/manifest/schema2"
	"github.com/docker/distribution/reference"
	"github.com/foundriesio/composeapp/pkg/compose"
	"gopkg.in/yaml.v3"
	"os"
	"strings"

	"github.com/compose-spec/compose-go/loader"
	"github.com/compose-spec/compose-go/types"
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

func loadProj(ctx context.Context, appName string, file string, content []byte) (*types.Project, error) {
	env := make(map[string]string)
	for _, val := range os.Environ() {
		parts := strings.Split(val, "=")
		env[parts[0]] = parts[1]
	}

	var files []types.ConfigFile
	files = append(files, types.ConfigFile{Filename: file, Content: content})
	return loader.LoadWithContext(ctx, types.ConfigDetails{
		WorkingDir:  ".",
		ConfigFiles: files,
		Environment: env,
	}, func(options *loader.Options) {
		options.SetProjectName(appName, true)
	})
}

func DoPublish(ctx context.Context, appName string, file, target, digestFile string, dryRun bool, archList []string,
	pinnedImages map[string]digest.Digest, layersMetaFile string, createAppLayersManifest bool) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	config, err := loader.ParseYAML(b)
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
		return errors.New("Unable to find 'services' section of composetypes file")
	}
	if err := pinServiceImages(ctx, svcs.(map[string]interface{}), proj, pinnedImages); err != nil {
		return err
	}

	fmt.Println("== Hashing services...")
	if err := pinServiceConfigs(svcs.(map[string]interface{}), proj); err != nil {
		return err
	}

	fmt.Println("= Getting app layers metadata...")
	appLayers, err := compose.GetLayers(ctx, svcs.(map[string]interface{}), archList)
	if err != nil {
		return err
	}

	if len(appLayers) == 0 {
		return fmt.Errorf("none of the factory architectures %q are supported by App images", archList)
	}

	var layerManifests []distribution.Descriptor
	if createAppLayersManifest {
		fmt.Println("= Posting app layers manifests...")
		layerManifests, err = compose.PostAppLayersManifests(ctx, target, appLayers, dryRun)
		if err != nil {
			return err
		}
	}

	var appLayersMetaBytes []byte
	if len(layersMetaFile) > 0 {
		fmt.Println("= Getting app layers metadata...")
		appLayersMetaBytes, err = compose.GetAppLayersMeta(layersMetaFile, appLayers)
		if err != nil {
			return fmt.Errorf("= Failed to get app layers metadata: %s\n", err.Error())
		}
	}

	fmt.Println("= Publishing app...")
	dgst, err := internal.CreateApp(ctx, config, target, dryRun, layerManifests, appLayersMetaBytes, AppManifestMaxSize)
	if err != nil {
		return err
	}
	if len(digestFile) > 0 {
		return os.WriteFile(digestFile, []byte(dgst), 0o640)
	}
	return nil
}

func iterateServices(services map[string]interface{}, proj *types.Project, fn types.ServiceFunc) error {
	return proj.WithServices(nil, func(s types.ServiceConfig) error {
		obj := services[s.Name]
		_, ok := obj.(map[string]interface{})
		if !ok {
			if s.Name == "extensions" {
				fmt.Println("Hacking around https://github.com/compose-spec/compose-go/issues/91")
				return nil
			}
			return fmt.Errorf("Service(%s) has invalid format", s.Name)
		}
		return fn(s)
	})
}

func pinServiceImages(ctx context.Context,
	services map[string]interface{},
	proj *types.Project,
	pinnedImages map[string]digest.Digest) error {
	regc := internal.NewRegistryClient()

	return iterateServices(services, proj, func(s types.ServiceConfig) error {
		name := s.Name
		obj := services[name]
		svc := obj.(map[string]interface{})

		image := s.Image
		if len(image) == 0 {
			return fmt.Errorf("Service(%s) missing 'image' attribute", name)
		}
		if s.Build != nil {
			fmt.Printf("Removing service(%s) 'build' stanza\n", name)
			delete(svc, "build")
		}

		fmt.Printf("Pinning %s(%s)\n", name, image)
		named, err := reference.ParseNormalizedNamed(image)
		if err != nil {
			return err
		}

		repo, err := regc.GetRepository(ctx, named)
		if err != nil {
			return err
		}

		var digest digest.Digest
		switch v := named.(type) {
		case reference.Tagged:
			tag := v.Tag()
			desc, err := repo.Tags(ctx).Get(ctx, tag)
			if err != nil {
				return fmt.Errorf("Unable to find image reference(%s): %s", image, err)
			}
			digest = desc.Digest
		case reference.Digested:
			digest = v.Digest()
		default:
			var ok bool
			if digest, ok = pinnedImages[named.Name()]; !ok {
				return fmt.Errorf("Invalid reference type for %s: %T. Images must be pinned to a `:<tag>` or `@sha256:<hash>`", named, named)
			}
		}

		mansvc, err := repo.Manifests(ctx, nil)
		if err != nil {
			return fmt.Errorf("Unable to get image manifests(%s): %s", image, err)
		}
		man, err := mansvc.Get(ctx, digest)
		if err != nil {
			return fmt.Errorf("Unable to find image manifest(%s): %s", image, err)
		}

		// TODO - we should find the intersection of platforms so
		// that we can denote the platforms this app can run on
		pinned := reference.Domain(named) + "/" + reference.Path(named) + "@" + digest.String()

		switch mani := man.(type) {
		case *manifestlist.DeserializedManifestList:
			fmt.Printf("  | ")
			for i, m := range mani.Manifests {
				if i != 0 {
					fmt.Printf(", ")
				}
				fmt.Printf(m.Platform.Architecture)
				if m.Platform.Architecture == "arm" {
					fmt.Printf(m.Platform.Variant)
				}
			}
		case *schema2.DeserializedManifest:
			break
		default:
			return fmt.Errorf("Unexpected manifest: %v", mani)
		}

		fmt.Println("\n  |-> ", pinned)
		svc["image"] = pinned
		return nil
	})
}

func pinServiceConfigs(services map[string]interface{}, proj *types.Project) error {
	return iterateServices(services, proj, func(s types.ServiceConfig) error {
		obj := services[s.Name]
		svc := obj.(map[string]interface{})

		marshalled, err := yaml.Marshal(svc)
		if err != nil {
			return err
		}

		srvh := sha256.Sum256(marshalled)
		fmt.Printf("   |-> %s : %x\n", s.Name, srvh)
		if s.Labels == nil {
			s.Labels = make(map[string]string)
		}
		s.Labels["io.compose-spec.config-hash"] = fmt.Sprintf("%x", srvh)
		svc["labels"] = s.Labels
		return nil
	})
}
