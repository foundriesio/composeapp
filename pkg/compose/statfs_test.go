package compose

import "testing"

func TestGetUsageInfoPercentAndBytes(t *testing.T) {
	dir := t.TempDir()

	// Percent mode: reserved is (100-watermark)% of total size.
	const watermark = 90
	pui, err := GetUsageInfo(dir, 0, watermark, false)
	if err != nil {
		t.Fatalf("percent GetUsageInfo failed: %v", err)
	}
	wantReserved := uint64((float64(100-watermark) / 100.0) * float64(pui.SizeB))
	if pui.Reserved != wantReserved {
		t.Fatalf("percent reserved: got %d, want %d", pui.Reserved, wantReserved)
	}
	if pui.Free > pui.Reserved && pui.Available != pui.Free-pui.Reserved {
		t.Fatalf("percent available: got %d, want %d", pui.Available, pui.Free-pui.Reserved)
	}

	// Bytes mode: reserved is the exact byte count and available is free minus it.
	reserved := pui.Free / 4
	bui, err := GetUsageInfo(dir, 0, reserved, true)
	if err != nil {
		t.Fatalf("bytes GetUsageInfo failed: %v", err)
	}
	if bui.Reserved != reserved {
		t.Fatalf("bytes reserved: got %d, want %d", bui.Reserved, reserved)
	}
	if bui.Available != bui.Free-reserved {
		t.Fatalf("bytes available: got %d, want %d", bui.Available, bui.Free-reserved)
	}

	// Bytes mode where the reservation exceeds free space clamps available to 0.
	cui, err := GetUsageInfo(dir, 0, bui.Free+1, true)
	if err != nil {
		t.Fatalf("clamp GetUsageInfo failed: %v", err)
	}
	if cui.Available != 0 {
		t.Fatalf("clamp available: got %d, want 0", cui.Available)
	}
}
