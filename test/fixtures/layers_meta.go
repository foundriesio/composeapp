package fixtures

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func GenerateLayersMetaFile(t *testing.T, appsRootDir string) string {
	tmpDir := t.TempDir()
	layerSizesFile := filepath.Join(tmpDir, "layers_meta.json")
	c := exec.Command("python", os.Getenv("LAYERS_SIZE_SCRIPT"), "--apps-root",
		appsRootDir, "-o", layerSizesFile)
	output, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to run the script that calculates app layer sizes: %s\n", output)
	}
	b, err := os.ReadFile(layerSizesFile)
	if err != nil {
		t.Fatalf("failed to read file witj app layer sizes: %s\n", err)
	}
	var ls map[string]interface{}
	if err := json.Unmarshal(b, &ls); err != nil {
		t.Fatalf("failed to unmarshal app layer sizes: %s\n", err)
	}
	lsPerArch := map[string]interface{}{
		"amd64": ls,
	}
	b, err = json.Marshal(lsPerArch)
	if err != nil {
		t.Fatalf("failed to marshal app layer sizes per architecure: %s\n", err)
	}
	layersMetaFile := filepath.Join(tmpDir, "layers_meta_per_arch.json")
	if err := os.WriteFile(layersMetaFile, b, 0x644); err != nil {
		t.Fatalf("failed to write file: %s\n", err)
	}
	return layersMetaFile
}
