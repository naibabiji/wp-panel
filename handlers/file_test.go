package handlers

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUploadSessionIDIsStableForResume(t *testing.T) {
	session := uploadSession{
		Filename:     "backup.zip",
		FileSize:     uploadChunkSize + 1,
		TotalChunks:  2,
		SiteID:       7,
		Path:         "/wp-content",
		LastModified: 1770000000000,
		CreatedAt:    1,
	}

	resumed := session
	resumed.CreatedAt = 2
	if makeUploadID(session) != makeUploadID(resumed) {
		t.Fatal("upload ID should be stable for the same file upload")
	}

	otherPath := session
	otherPath.Path = "/uploads"
	if makeUploadID(session) == makeUploadID(otherPath) {
		t.Fatal("upload ID should include the target path")
	}
}

func TestUploadChunkTrackingIgnoresMetadataAndFindsMissingChunks(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"session.json", "chunk-0", "chunk-2", "chunk-2.tmp"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	completed := completedUploadChunks(dir, 3)
	if !reflect.DeepEqual(completed, []int{0, 2}) {
		t.Fatalf("completed chunks = %v, want [0 2]", completed)
	}

	missing := missingUploadChunks(dir, 3)
	if !reflect.DeepEqual(missing, []int{1}) {
		t.Fatalf("missing chunks = %v, want [1]", missing)
	}
}

func TestExpectedUploadChunksAllowsEmptyFiles(t *testing.T) {
	tests := []struct {
		size int64
		want int
	}{
		{size: 0, want: 0},
		{size: 1, want: 1},
		{size: uploadChunkSize, want: 1},
		{size: uploadChunkSize + 1, want: 2},
	}

	for _, tt := range tests {
		if got := expectedUploadChunks(tt.size); got != tt.want {
			t.Fatalf("expectedUploadChunks(%d) = %d, want %d", tt.size, got, tt.want)
		}
	}
}
