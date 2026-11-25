package ensemble

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestContextPackGenerator_Generate_FullProject(t *testing.T) {
	t.Parallel()
	root := copyFixture(t, "full-project")
	gen := NewContextPackGenerator(root, nil, nil)

	pack, err := gen.Generate(t.Context(), strings.Repeat("Q", minProblemStatementLen+5), "mode-a", CacheConfig{Enabled: false})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if pack.ProjectBrief == nil {
		t.Fatal("expected ProjectBrief to be populated")
	}

	if pack.ProjectBrief.Name != "Full Project" {
		t.Errorf("ProjectBrief.Name = %q", pack.ProjectBrief.Name)
	}
	if pack.ProjectBrief.Description == "" {
		t.Error("ProjectBrief.Description should not be empty")
	}

	if !containsString(pack.ProjectBrief.Languages, "Go") {
		t.Errorf("expected Go in languages: %v", pack.ProjectBrief.Languages)
	}
	if !containsString(pack.ProjectBrief.Languages, "TypeScript") {
		t.Errorf("expected TypeScript in languages: %v", pack.ProjectBrief.Languages)
	}
	if !containsString(pack.ProjectBrief.Frameworks, "Cobra") {
		t.Errorf("expected Cobra in frameworks: %v", pack.ProjectBrief.Frameworks)
	}
	if !containsString(pack.ProjectBrief.Frameworks, "Next.js") {
		t.Errorf("expected Next.js in frameworks: %v", pack.ProjectBrief.Frameworks)
	}
	if pack.ProjectBrief.OpenIssues != 2 {
		t.Errorf("OpenIssues = %d, want 2", pack.ProjectBrief.OpenIssues)
	}

	if pack.ProjectBrief.Structure == nil {
		t.Fatal("expected ProjectStructure to be populated")
	}
	if !containsString(pack.ProjectBrief.Structure.EntryPoints, "cmd/full/main.go") {
		t.Errorf("expected entrypoint cmd/full/main.go, got %v", pack.ProjectBrief.Structure.EntryPoints)
	}
	if !containsString(pack.ProjectBrief.Structure.CorePackages, "internal/core") {
		t.Errorf("expected core package internal/core, got %v", pack.ProjectBrief.Structure.CorePackages)
	}
}

func TestContextPackGenerator_Generate_MinimalProject(t *testing.T) {
	t.Parallel()
	root := copyFixture(t, "minimal-project")
	gen := NewContextPackGenerator(root, nil, nil)

	pack, err := gen.Generate(t.Context(), "Short question", "", CacheConfig{Enabled: false})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if pack.ProjectBrief == nil {
		t.Fatal("expected ProjectBrief to be populated")
	}
	if !containsString(pack.ProjectBrief.Languages, "Go") {
		t.Errorf("expected Go in languages: %v", pack.ProjectBrief.Languages)
	}
	if len(pack.Questions) == 0 {
		t.Error("expected thin-context questions for minimal project")
	}
}

func TestContextPackGenerator_DetectLanguages_Go(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	_, languages, _ := scanProjectStructure(t.Context(), root)
	if !containsString(languages, "Go") {
		t.Errorf("expected Go in languages: %v", languages)
	}
}

func TestContextPackGenerator_DetectLanguages_TypeScript(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app.ts"), "export const ok = true\n")
	_, languages, _ := scanProjectStructure(t.Context(), root)
	if !containsString(languages, "TypeScript") {
		t.Errorf("expected TypeScript in languages: %v", languages)
	}
}

func TestContextPackGenerator_DetectLanguages_Mixed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	writeFile(t, filepath.Join(root, "web", "app.tsx"), "export const App = () => null\n")
	_, languages, _ := scanProjectStructure(t.Context(), root)
	if !containsString(languages, "Go") || !containsString(languages, "TypeScript") {
		t.Errorf("expected mixed languages, got %v", languages)
	}
}

func TestContextPackGenerator_DetectFrameworks_GoMod(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	goMod := "module example.com/test\n\ngo 1.25\n\nrequire github.com/spf13/cobra v1.8.0\n"
	writeFile(t, filepath.Join(root, "go.mod"), goMod)
	frameworks := frameworksFromGoMod(filepath.Join(root, "go.mod"))
	if !containsString(frameworks, "Cobra") {
		t.Errorf("expected Cobra in frameworks: %v", frameworks)
	}
}

func TestContextPackGenerator_DetectFrameworks_PackageJSON(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	packageJSON := `{"dependencies":{"react":"18.2.0","next":"14.1.0"}}`
	writeFile(t, filepath.Join(root, "package.json"), packageJSON)
	frameworks := frameworksFromPackageJSON(filepath.Join(root, "package.json"))
	if !containsString(frameworks, "React") || !containsString(frameworks, "Next.js") {
		t.Errorf("expected React and Next.js in frameworks: %v", frameworks)
	}
}

func TestContextPackGenerator_DetectFrameworks_CargoToml(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cargoToml := `
[package]
name = "demo"

[dependencies]
tokio = "1"
axum = "0.7"
`
	writeFile(t, filepath.Join(root, "Cargo.toml"), cargoToml)
	frameworks := frameworksFromCargo(filepath.Join(root, "Cargo.toml"))
	if !containsString(frameworks, "Tokio") || !containsString(frameworks, "Axum") {
		t.Errorf("expected Tokio and Axum in frameworks: %v", frameworks)
	}
}

func TestContextPackGenerator_DetectFrameworks_PythonRequirements(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	requirements := "Django==4.2\nfastapi==0.110\n"
	writeFile(t, filepath.Join(root, "requirements.txt"), requirements)
	frameworks := frameworksFromPython(filepath.Join(root, "requirements.txt"))
	if !containsString(frameworks, "Django") || !containsString(frameworks, "FastAPI") {
		t.Errorf("expected Django and FastAPI in frameworks: %v", frameworks)
	}
}

func TestContextPackGenerator_ParseReadme_Full(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	readme := "# My Project\nA short description for testing the parser.\n"
	writeFile(t, filepath.Join(root, "README.md"), readme)
	name, desc := readProjectOverview(root)
	if name != "My Project" {
		t.Errorf("name = %q, want %q", name, "My Project")
	}
	if desc == "" {
		t.Error("expected non-empty description")
	}
}

func TestContextPackGenerator_ParseReadme_Missing(t *testing.T) {
	t.Parallel()
	root := fixturePath(t, "no-readme")
	name, desc := readProjectOverview(root)
	if name != "no-readme" {
		t.Errorf("name = %q, want %q", name, "no-readme")
	}
	if desc != "" {
		t.Errorf("expected empty description, got %q", desc)
	}
}

func TestContextPackGenerator_GetRecentCommits(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	if err := initGitRepo(root); err != nil {
		t.Skipf("git repo init failed: %v", err)
	}

	commits := recentCommits(t.Context(), root, 5)
	if len(commits) == 0 {
		t.Fatal("expected at least one commit")
	}
	if commits[0].Summary != "Initial commit" {
		t.Errorf("commit summary = %q, want %q", commits[0].Summary, "Initial commit")
	}
}

func TestContextPackGenerator_CountOpenIssues(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	issuesDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(issuesDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	issues := "{\"status\":\"open\"}\n{\"status\":\"closed\"}\n{\"status\":\"in_progress\"}\n"
	writeFile(t, filepath.Join(issuesDir, "issues.jsonl"), issues)

	count := countOpenIssues(t.Context(), root)
	if count != 2 {
		t.Errorf("open issues = %d, want 2", count)
	}
}

func initGitRepo(dir string) error {
	if err := runGit(dir, "init"); err != nil {
		return err
	}
	if err := runGit(dir, "config", "user.email", "test@example.com"); err != nil {
		return err
	}
	if err := runGit(dir, "config", "user.name", "Test User"); err != nil {
		return err
	}
	if err := runGit(dir, "add", "."); err != nil {
		return err
	}
	if err := runGit(dir, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-m", "Initial commit"); err != nil {
		return err
	}
	return nil
}

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %w: %s", args, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("testdata", "context", name)
}

func copyFixture(t *testing.T, name string) string {
	t.Helper()
	src := fixturePath(t, name)
	dst := t.TempDir()
	copyDir(t, src, dst)
	return dst
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
	if err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func TestContextPackGenerator_CancelledGitProbeDoesNotCachePartialContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake git fixture requires a POSIX shell")
	}
	for _, blocked := range []string{
		"rev-parse --show-toplevel",
		"rev-parse HEAD",
		"status --porcelain",
		"log -8",
	} {
		t.Run(blocked, func(t *testing.T) {
			projectDir := t.TempDir()
			fixtureDir := t.TempDir()
			callsPath := filepath.Join(fixtureDir, "calls")
			readyPath := filepath.Join(fixtureDir, "ready")
			script := "#!/bin/sh\n" +
				"printf '%s %s\\n' \"$1\" \"$2\" >> \"$NTM_CONTEXT_GIT_CALLS\"\n" +
				"if [ \"$1 $2\" = \"$NTM_CONTEXT_GIT_BLOCK\" ]; then\n" +
				"  : > \"$NTM_CONTEXT_GIT_READY\"\n" +
				"  exec sleep 30\n" +
				"fi\n" +
				"case \"$1 $2\" in\n" +
				"  'rev-parse --show-toplevel') printf '%s\\n' \"$NTM_CONTEXT_GIT_ROOT\" ;;\n" +
				"  'rev-parse HEAD') printf '%s\\n' 1111111111111111111111111111111111111111 ;;\n" +
				"esac\n"
			if err := os.WriteFile(filepath.Join(fixtureDir, "git"), []byte(script), 0o755); err != nil {
				t.Fatalf("write fake git: %v", err)
			}
			t.Setenv("PATH", fixtureDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("NTM_CONTEXT_GIT_CALLS", callsPath)
			t.Setenv("NTM_CONTEXT_GIT_READY", readyPath)
			t.Setenv("NTM_CONTEXT_GIT_BLOCK", blocked)
			t.Setenv("NTM_CONTEXT_GIT_ROOT", projectDir)

			cacheDir := t.TempDir()
			cacheCfg := CacheConfig{Enabled: true}
			cache, err := NewContextPackCacheWithDir(cacheDir, cacheCfg, nil)
			if err != nil {
				t.Fatalf("new cache: %v", err)
			}
			generator := NewContextPackGenerator(projectDir, cache, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type generationResult struct {
				pack *ContextPack
				err  error
			}
			done := make(chan generationResult, 1)
			go func() {
				pack, err := generator.Generate(ctx, "Analyze this project", "", cacheCfg)
				done <- generationResult{pack: pack, err: err}
			}()

			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			ready := false
			for !ready {
				select {
				case result := <-done:
					t.Fatalf("Generate returned before blocked git probe: %+v", result)
				case <-ticker.C:
					if _, err := os.Stat(readyPath); err == nil {
						ready = true
					}
				case <-deadline.C:
					t.Fatal("git probe did not reach the blocking fixture")
				}
			}
			cancel()
			select {
			case result := <-done:
				if !errors.Is(result.err, context.Canceled) || result.pack != nil {
					t.Fatalf("Generate = %+v, %v; want nil, context canceled", result.pack, result.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancellation did not interrupt the git probe")
			}

			entries, err := os.ReadDir(cacheDir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("cancelled generation wrote partial cache: entries=%v err=%v", entries, err)
			}
			calls, err := os.ReadFile(callsPath)
			if err != nil {
				t.Fatalf("read git calls: %v", err)
			}
			lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
			if len(lines) == 0 || lines[len(lines)-1] != blocked {
				t.Fatalf("git commands continued after cancellation: %s", calls)
			}
		})
	}
}
