package logger

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanExpired(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().AddDate(0, 0, -10).Format(dayLayout)
	recent := time.Now().Format(dayLayout)
	for _, name := range []string{filePrefix + old + fileExt, filePrefix + recent + fileExt, "other.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cleanExpired(dir, 7)

	if _, err := os.Stat(filepath.Join(dir, filePrefix+old+fileExt)); !os.IsNotExist(err) {
		t.Fatalf("expired log should be removed, err=%v", err)
	}
	for _, name := range []string{filePrefix + recent + fileExt, "other.log"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s should be kept: %v", name, err)
		}
	}
}

func TestDailyWriter(t *testing.T) {
	dir := t.TempDir()
	dw := newDailyWriter(dir)
	if _, err := dw.Write([]byte("hello log\n")); err != nil {
		t.Fatal(err)
	}
	if err := dw.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, filePrefix+time.Now().Format(dayLayout)+fileExt))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello log\n" {
		t.Fatalf("unexpected content: %q", data)
	}
}
