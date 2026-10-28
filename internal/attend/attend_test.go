package attend

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFreshness(t *testing.T) {
	p := filepath.Join(t.TempDir(), FileName)
	if Fresh(p, time.Minute) || Fresh("", time.Minute) {
		t.Fatal("a missing marker or an empty path counts as attended")
	}
	if err := Touch(p); err != nil {
		t.Fatal(err)
	}
	if !Fresh(p, time.Minute) {
		t.Fatal("a just-touched marker is not fresh")
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if Fresh(p, time.Minute) {
		t.Fatal("an hour-old marker is fresh")
	}
	if err := Touch(p); err != nil || !Fresh(p, time.Minute) {
		t.Fatalf("touching an existing marker: %v", err)
	}
}

func TestRunTouchesAndRemoves(t *testing.T) {
	p := filepath.Join(t.TempDir(), FileName)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { Run(p, stop); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for !Fresh(p, time.Minute) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !Fresh(p, time.Minute) {
		t.Fatal("Run did not create the marker")
	}
	close(stop)
	<-done
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("the marker was not removed on stop")
	}
}
