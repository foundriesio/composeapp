package internal

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"path/filepath"
	"time"

	compose "github.com/compose-spec/compose-go/types"
	"github.com/docker/distribution"
	"github.com/docker/distribution/manifest/manifestlist"
	"github.com/docker/distribution/manifest/ocischema"
	"github.com/docker/distribution/manifest/schema2"
	"github.com/docker/distribution/reference"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
	"github.com/moby/patternmatcher/ignorefile"
	"github.com/opencontainers/go-digest"
)

const (
	// MaxArchNumb limits derived from aklite's limitation on an App manifest size
	MaxArchNumb         = 6
	MaxManifestBodySize = 2010 // (2048 - 38) just in case
)

func iterateServices(services map[string]interface{}, proj *compose.Project, fn compose.ServiceFunc) error {
	return proj.WithServices(nil, func(s compose.ServiceConfig) error {
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

func PinServiceImages(cli *client.Client, ctx context.Context, services map[string]interface{}, proj *compose.Project, pinnedImages map[string]digest.Digest) error {
	regc := NewRegistryClient()

	return iterateServices(services, proj, func(s compose.ServiceConfig) error {
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

func PinServiceConfigs(cli *client.Client, ctx context.Context, services map[string]interface{}, proj *compose.Project) error {
	return iterateServices(services, proj, func(s compose.ServiceConfig) error {
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

func getIgnores(appDir string) []string {
	file, err := os.Open(filepath.Join(appDir, ".composeappignores"))
	if err != nil {
		return []string{}
	}
	ignores, _ := ignorefile.ReadAll(file)
	file.Close()
	if ignores != nil {
		ignores = append(ignores, ".composeappignores")
	}
	return ignores
}

func createTgz(composeContent []byte, appDir string) ([]byte, error) {
	reader, err := archive.TarWithOptions(appDir, &archive.TarOptions{
		Compression:     archive.Uncompressed,
		ExcludePatterns: getIgnores(appDir),
	})
	if err != nil {
		return nil, err
	}

	composeFound := false
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	tr := tar.NewReader(reader)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		// reset the file's timestamps, otherwise hashes of the resultant
		// TGZs will differ even if their content is the same
		hdr.ChangeTime = time.Time{}
		hdr.AccessTime = time.Time{}
		hdr.ModTime = time.Time{}
		if hdr.Name == "docker-compose.yml" {
			composeFound = true
			hdr.Size = int64(len(composeContent))
			if err := tw.WriteHeader(hdr); err != nil {
				return nil, fmt.Errorf("Unable to add docker-compose.yml header archive: %s", err)
			}
			if _, err := tw.Write(composeContent); err != nil {
				return nil, fmt.Errorf("Unable to add docker-compose.yml to archive: %s", err)
			}
		} else {
			if err := tw.WriteHeader(hdr); err != nil {
				return nil, fmt.Errorf("Unable to add %s header archive: %s", hdr.Name, err)
			}
			if _, err := io.Copy(tw, tr); err != nil {
				return nil, fmt.Errorf("Unable to add %s archive: %s", hdr.Name, err)
			}
		}
	}

	if !composeFound {
		return nil, errors.New("A .composeappignores rule is discarding docker-compose.yml")
	}

	tw.Close()
	gzw.Close()
	return buf.Bytes(), nil
}

func CreateApp(ctx context.Context, config map[string]interface{}, target string, dryRun bool, layerManifests []distribution.Descriptor, appLayersMetaData []byte) (string, error) {
	pinned, err := yaml.Marshal(config)
	if err != nil {
		return "", err
	}

	pinnedHash := sha256.Sum256(pinned)
	fmt.Printf("  |-> pinned content hash: %x\n", pinnedHash)

	buff, err := createTgz(pinned, "./")
	if err != nil {
		return "", err
	}

	archHash := sha256.Sum256(buff)
	fmt.Printf("  |-> app archive hash: %x\n", archHash)

	named, err := reference.ParseNormalizedNamed(target)
	if err != nil {
		return "", err
	}
	tag := "latest"
	if tagged, ok := reference.TagNameOnly(named).(reference.Tagged); ok {
		tag = tagged.Tag()
	}

	regc := NewRegistryClient()
	repo, err := regc.GetRepository(ctx, named)
	if err != nil {
		return "", err
	}

	if dryRun {
		fmt.Println("Pinned compose:")
		fmt.Println(string(pinned))
		fmt.Println("Skipping publishing for dryrun")

		if err := os.WriteFile("/tmp/compose-bundle.tgz", buff, 0755); err != nil {
			return "", err
		}

		return "", nil
	}

	blobStore := repo.Blobs(ctx)
	desc, err := blobStore.Put(ctx, "application/tar+gzip", buff)
	if err != nil {
		return "", err
	}
	fmt.Println("  |-> app blob: ", desc.Digest.String())

	mb := ocischema.NewManifestBuilder(blobStore, []byte{}, map[string]string{"compose-app": "v1"})
	if err := mb.AppendReference(desc); err != nil {
		return "", err
	}

	if appLayersMetaData != nil {
		if d, err := blobStore.Put(ctx, "application/json", appLayersMetaData); err == nil {
			d.Annotations = map[string]string{"layers-meta": "v1"}
			if err := mb.AppendReference(d); err != nil {
				return "", fmt.Errorf("failed to add App layers meta descriptor to the App manifest: %s", err.Error())
			}
			fmt.Println("  |-> app layers meta: ", d.Digest.String())
		} else {
			return "", fmt.Errorf("failed to put App layers meta to the App blob store: %s", err.Error())
		}
	}

	manifest, err := mb.Build(ctx)
	if err != nil {
		return "", err
	}

	man, ok := manifest.(*ocischema.DeserializedManifest)
	if !ok {
		return "", fmt.Errorf("invalid manifest type, expected *ocischema.DeserializedManifest, got: %T", manifest)
	}

	b, err := man.MarshalJSON()
	if err != nil {
		return "", err
	}

	manMap := make(map[string]interface{})
	err = json.Unmarshal(b, &manMap)
	if err != nil {
		return "", err
	}

	manMap["manifests"] = layerManifests

	b1, err := json.MarshalIndent(manMap, "", "   ")
	if err != nil {
		return "", err
	}

	err = man.UnmarshalJSON(b1)
	if err != nil {
		return "", err
	}

	fmt.Printf("  |-> manifest size: %d\n", len(b1))
	// TODO: this check is needed in order to overcome the aklite's check on the maximum manifest size (2048)
	// Once the new version of aklite is deployed (max manifest size = 16K) then this check can be removed or MaxArchNumb increased
	if len(b1) >= MaxManifestBodySize {
		return "", fmt.Errorf("app manifest size (%d) exceeds the maximum size limit (%d)", len(b1), MaxManifestBodySize)
	}
	svc, err := repo.Manifests(ctx, nil)
	if err != nil {
		return "", err
	}

	putOptions := []distribution.ManifestServiceOption{distribution.WithTag(tag)}
	digest, err := svc.Put(ctx, man, putOptions...)
	if err != nil {
		return "", err
	}
	fmt.Println("  |-> manifest: ", digest.String())

	return digest.String(), err
}
