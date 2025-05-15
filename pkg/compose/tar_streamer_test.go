package compose

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"sync"
	"testing"
)

func TestTarStreamer(t *testing.T) {
	ts := NewTarStreamer()
	if ts == nil {
		t.Fatal("NewTarStreamer returned nil")
	}
	defer ts.Close()

	testData := "foobar"
	testDataFile := "test"

	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := ts.WriteFileData([]byte(testData), testDataFile); err != nil {
			errCh <- fmt.Errorf("WriteFileData failed: %v", err)
		}
	}()

	tr := tar.NewReader(ts.Reader())
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("tar.NewReader failed: %v", err)
	}
	if hdr.Name != testDataFile {
		t.Fatalf("unexpected header name: %s; expected: %s", hdr.Name, testDataFile)
	}

	var buf bytes.Buffer
	n, err := io.Copy(&buf, tr)
	if err != nil {
		t.Fatalf("io.Copy failed: %v", err)
	}
	if n != int64(len(testData)) {
		t.Fatalf("unexpected number of bytes copied: %d; expected: %d", n, len(testData))
	}
	if buf.String() != testData {
		t.Fatalf("unexpected data copied: %s; expected: %s", buf.String(), testData)
	}
	wg.Wait()
	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}
