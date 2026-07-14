package compose

import (
	"os"
	"testing"
)

func TestProjectSnapshotCloseRemovesDirectory(t *testing.T) {
	snapshot, err := newProjectSnapshot(t.TempDir())
	if err != nil {
		t.Fatalf("newProjectSnapshot() error = %v", err)
	}
	directory := snapshot.directory

	if err := snapshot.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("Stat(%q) error = %v, want not exist", directory, err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}
