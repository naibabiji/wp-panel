package executor

import "testing"

func TestWPCoreWorkingBytesAndAvailableSpace(t *testing.T) {
	working, err := wpCoreWorkingBytes(100, 200, 300)
	if err != nil || working != 1800 {
		t.Fatalf("working bytes = %d, %v", working, err)
	}
	if _, err := wpCoreWorkingBytes(^uint64(0), 1, 0); err == nil {
		t.Fatal("expected addition overflow rejection")
	}
	if _, err := wpCoreWorkingBytes(^uint64(0)/3, 0, 1); err == nil {
		t.Fatal("expected multiplication overflow rejection")
	}

	const tenGiB = uint64(10 << 30)
	if !wpCoreHasAvailableSpace(1<<30, tenGiB, 2<<30) {
		t.Fatal("expected exact working plus minimum reserve to pass")
	}
	if wpCoreHasAvailableSpace(1<<30, tenGiB, (2<<30)-1) {
		t.Fatal("expected space below minimum reserve threshold to fail")
	}

	const hundredGiB = uint64(100 << 30)
	if !wpCoreHasAvailableSpace(1<<30, hundredGiB, 6<<30) {
		t.Fatal("expected exact working plus five-percent reserve to pass")
	}
	if wpCoreHasAvailableSpace(^uint64(0), hundredGiB, ^uint64(0)) {
		t.Fatal("expected required-space overflow to fail")
	}
}
