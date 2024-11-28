//go:build publish

package internal

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/docker/distribution"
	"github.com/docker/distribution/manifest/ocischema"
	"github.com/docker/distribution/reference"
	"github.com/docker/docker/pkg/archive"
	"github.com/moby/patternmatcher/ignorefile"
)


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

func CreateApp(ctx context.Context,
config map[string]interface{},
target string, dryRun bool,
layerManifests []distribution.Descriptor,
appLayersMetaData []byte,
appManifestMaxSize int) (string, error) {
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

	mb := NewManifestBuilder(blobStore)
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

	if layerManifests != nil {
		mb.(*ManifestBuilder).SetLayerMetaManifests(layerManifests)
	}

	manifest, err := mb.Build(ctx)
	if err != nil {
		return "", err
	}

	man, ok := manifest.(*ocischema.DeserializedManifest)
	if !ok {
		return "", fmt.Errorf("invalid manifest type, expected *ocischema.DeserializedManifest, got: %T", manifest)
	}
	_, b, err := man.Payload()
	if err != nil {
		return "", err
	}

	fmt.Printf("  |-> manifest size: %d\n", len(b))
	if len(b) >= appManifestMaxSize {
		return "", fmt.Errorf("app manifest size (%d) exceeds the maximum size limit (%d)", len(b), appManifestMaxSize)
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
	fmt.Println("  |-> uri: ", named.Name() + "@" + digest.String())

	return digest.String(), err
}
