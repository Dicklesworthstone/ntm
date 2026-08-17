package webui

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// appRoutesFromSource derives the route list by globbing web/src/app for
// page.tsx files — the route inventory stays true by construction instead of
// rotting as a hand-maintained list (F1, bd-ws5-ship-or-cut-jv0rc.1.2).
func appRoutesFromSource(t *testing.T) []string {
	t.Helper()

	appDir := filepath.Join("..", "..", "web", "src", "app")
	var routes []string
	err := filepath.WalkDir(appDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "page.tsx" {
			return nil
		}
		rel, err := filepath.Rel(appDir, filepath.Dir(path))
		if err != nil {
			return err
		}
		segments := []string{}
		if rel != "." {
			for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
				// Route groups like (dashboard) do not appear in the URL.
				if strings.HasPrefix(seg, "(") && strings.HasSuffix(seg, ")") {
					continue
				}
				if strings.HasPrefix(seg, "[") {
					t.Fatalf("dynamic route segment %q in %s: output:'export' forbids dynamic segments without generateStaticParams", seg, path)
				}
				segments = append(segments, seg)
			}
		}
		routes = append(routes, "/"+strings.Join(segments, "/"))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", appDir, err)
	}
	sort.Strings(routes)
	if len(routes) == 0 {
		t.Fatal("no page.tsx routes found under web/src/app")
	}
	return routes
}

func embeddedFile(t *testing.T, name string) string {
	t.Helper()
	fsys, err := FS()
	if err != nil {
		t.Fatalf("webui.FS(): %v", err)
	}
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		t.Fatalf("embedded file %s: %v", name, err)
	}
	return string(data)
}

// TestEmbeddedUIPresent is the embed-presence proof: the binary carries the
// exported site, not an empty directory.
func TestEmbeddedUIPresent(t *testing.T) {
	index := embeddedFile(t, "index.html")
	if !strings.Contains(index, "NTM") {
		t.Error("embedded index.html does not look like the NTM dashboard (missing 'NTM')")
	}
	fsys, err := FS()
	if err != nil {
		t.Fatalf("webui.FS(): %v", err)
	}
	entries, err := fs.ReadDir(fsys, "_next")
	if err != nil || len(entries) == 0 {
		t.Errorf("embedded export has no _next asset directory (err=%v)", err)
	}
}

// TestEmbeddedRoutesMatchAppSource asserts BOTH directions between the
// web/src/app source tree and the embedded export:
//   - every source route has an exported index.html in the embed;
//   - every exported route page maps back to a source route (no stale pages).
//
// It also pins the two F1a completions: the memory route exists, and the
// agents page is real (positive heading, no coming-soon stub marker).
func TestEmbeddedRoutesMatchAppSource(t *testing.T) {
	routes := appRoutesFromSource(t)

	fsys, err := FS()
	if err != nil {
		t.Fatalf("webui.FS(): %v", err)
	}

	// Source -> embed.
	routeSet := make(map[string]bool, len(routes))
	for _, route := range routes {
		routeSet[route] = true
		name := "index.html"
		if route != "/" {
			name = strings.TrimPrefix(route, "/") + "/index.html"
		}
		if _, err := fs.Stat(fsys, name); err != nil {
			t.Errorf("route %s: embedded export missing %s (rebuild with scripts/build_web.sh): %v", route, name, err)
		}
	}

	// Embed -> source.
	err = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "index.html" {
			return nil
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		route := "/"
		if dir != "." {
			route = "/" + dir
		}
		switch {
		case route == "/404", route == "/_not-found":
			return nil // Next-generated error pages, not app routes
		case strings.HasPrefix(route, "/_next"):
			return nil
		}
		if !routeSet[route] {
			t.Errorf("embedded export has stale route %s with no page.tsx under web/src/app", route)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded export: %v", err)
	}

	if !routeSet["/memory"] {
		t.Error("memory route missing from web/src/app (F1a exit criterion)")
	}

	agents := embeddedFile(t, "agents/index.html")
	if !strings.Contains(agents, "Agents") {
		t.Error("agents page lost its positive content marker 'Agents'")
	}
	if strings.Contains(strings.ToLower(agents), "coming soon") {
		t.Error("agents page still contains the coming-soon stub marker (F1a exit criterion)")
	}
}

// TestVersionLockstep pins the structural version contract: repo VERSION ==
// web/package.json version == the data-ntm-version stamp baked into the
// embedded export. Version skew between UI and binary is exactly how the
// dashboard froze at v1.16.0.
func TestVersionLockstep(t *testing.T) {
	versionBytes, err := os.ReadFile(filepath.Join("..", "..", "VERSION"))
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	version := strings.TrimSpace(string(versionBytes))
	if version == "" {
		t.Fatal("VERSION file is empty")
	}

	pkgBytes, err := os.ReadFile(filepath.Join("..", "..", "web", "package.json"))
	if err != nil {
		t.Fatalf("read web/package.json: %v", err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(pkgBytes, &pkg); err != nil {
		t.Fatalf("parse web/package.json: %v", err)
	}
	if pkg.Version != version {
		t.Errorf("version skew: web/package.json has %q, VERSION has %q", pkg.Version, version)
	}

	index := embeddedFile(t, "index.html")
	stamp := `data-ntm-version="` + version + `"`
	if !strings.Contains(index, stamp) {
		t.Errorf("embedded index.html missing version stamp %s (stale embed — rerun scripts/build_web.sh)", stamp)
	}
}
