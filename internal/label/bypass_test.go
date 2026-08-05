package label_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Forbidden bypass patterns must not appear in product surfaces that could disable labels.
var bypassScanRoots = []string{
	"internal/label",
	"internal/render",
	"internal/config",
	"config.example.yaml",
}

var forbiddenBypassTokens = []string{
	"skip_label",
	"disable_label",
	"label.enabled",
	"omit_label",
	"LabelEnabled",
	"no_label",
	"bypass_label",
}

func TestNoLabelBypassSwitchInProductSurfaces(t *testing.T) {
	root := findRepoRoot(t)
	for _, rel := range bypassScanRoots {
		path := filepath.Join(root, rel)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if info.IsDir() {
			walkDir(t, path, rel)
		} else {
			scanFile(t, path, rel)
		}
	}
}

func walkDir(t *testing.T, dir, rel string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".tsx") && !strings.HasSuffix(name, ".ts") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") && name != "bypass_test.go" {
			// still scan test files for accidental bypass helpers except this file
		}
		scanFile(t, filepath.Join(dir, name), filepath.Join(rel, name))
	}
}

func scanFile(t *testing.T, path, rel string) {
	t.Helper()
	if strings.HasSuffix(rel, "bypass_test.go") {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(b)
	for _, token := range forbiddenBypassTokens {
		if strings.Contains(content, token) {
			t.Fatalf("%s contains forbidden bypass token %q", rel, token)
		}
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
