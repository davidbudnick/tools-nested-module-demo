package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	stock := os.Getenv("GOPLS_STOCK")
	if stock == "" {
		stock = installStock(root)
	}
	patched := os.Getenv("GOPLS_PATCHED")
	if patched == "" {
		patched = buildPatched(root)
	}
	before := runViews(root, stock)
	after := runViews(root, patched)
	fmt.Printf("stock   %s  views=%d  %s\n", stock, len(before), roots(before))
	fmt.Printf("patched %s  views=%d  %s\n", patched, len(after), roots(after))
	if err := os.WriteFile(filepath.Join(root, "testdata", "before.json"), pretty(before), 0o644); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "testdata", "after.json"), pretty(after), 0o644); err != nil {
		fatal(err)
	}
	if len(before) != 2 || roots(before) != ". other" {
		fatal(fmt.Errorf("stock: want 2 views [. other], got %d %s", len(before), roots(before)))
	}
	if len(after) != 1 || roots(after) != "." {
		fatal(fmt.Errorf("patched: want 1 view [.], got %d %s", len(after), roots(after)))
	}
	fmt.Println("PASS  2 Views -> 1 View")
}

// runViews execs showviews against gopls and returns the View list.
func runViews(root, gopls string) []map[string]any {
	cmd := exec.Command("go", "run", "./cmd/showviews")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOPLS="+gopls)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		fatal(fmt.Errorf("GOPLS=%s: %v\n%s", gopls, err, stderr.Bytes()))
	}
	var views []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &views); err != nil {
		fatal(fmt.Errorf("GOPLS=%s: decode views: %v\n%s", gopls, err, stdout.Bytes()))
	}
	return views
}

// installStock installs gopls v0.23.0 into .bin/gopls-stock.
func installStock(root string) string {
	dir := filepath.Join(root, ".bin")
	out := filepath.Join(dir, "gopls-stock")
	if _, err := os.Stat(out); err == nil {
		return out
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fatal(err)
	}
	fmt.Fprintln(os.Stderr, "installing golang.org/x/tools/gopls@v0.23.0")
	cmd := exec.Command("go", "install", "golang.org/x/tools/gopls@v0.23.0")
	cmd.Env = append(os.Environ(), "GOBIN="+dir)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "gopls"), out); err != nil {
		fatal(err)
	}
	return out
}

// buildPatched builds gopls from GOPLS_SRC or clones the CL branch.
func buildPatched(root string) string {
	if p := os.Getenv("GOPLS_PATCHED"); p != "" {
		return p
	}
	dir := filepath.Join(root, ".bin")
	out := filepath.Join(dir, "gopls-patched")
	src := os.Getenv("GOPLS_SRC")
	if src == "" {
		src = filepath.Join(dir, "tools")
		if _, err := os.Stat(filepath.Join(src, "gopls", "go.mod")); err != nil {
			fmt.Fprintln(os.Stderr, "cloning github.com/davidbudnick/tools@gopls-dirfilters")
			cmd := exec.Command("git", "clone", "--depth", "1", "-b", "gopls-dirfilters", "https://github.com/davidbudnick/tools.git", src)
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				fatal(err)
			}
		}
	}
	fmt.Fprintln(os.Stderr, "building patched gopls from", src)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fatal(err)
	}
	cmd := exec.Command("go", "build", "-trimpath", "-o", out, ".")
	cmd.Dir = filepath.Join(src, "gopls")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fatal(err)
	}
	return out
}

// roots joins View Root fields for a one-line summary.
func roots(views []map[string]any) string {
	var b strings.Builder
	for i, v := range views {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(fmt.Sprint(v["Root"]))
	}
	return b.String()
}

// pretty returns indented JSON with a trailing newline.
func pretty(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fatal(err)
	}
	return append(b, '\n')
}

// fatal prints err and exits.
func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
