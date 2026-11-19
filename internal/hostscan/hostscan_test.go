package hostscan

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScanFindsAgentsWithoutReadingSecrets(t *testing.T) {
	home := t.TempDir()
	bin := t.TempDir()
	mustDir(t, filepath.Join(home, ".codex"))
	mustDir(t, filepath.Join(home, ".claude"))
	mustFile(t, filepath.Join(bin, "codex.exe"), "not executable")
	mustFile(t, filepath.Join(home, ".codex", "auth.json"), `{"token":"do-not-read-this-secret"}`)

	scanner := &Scanner{
		HomeDir:        home,
		PathDirs:       []string{bin},
		ExecutableExts: []string{".exe"},
		Lstat:          os.Lstat,
		IsReparsePoint: func(string, os.FileInfo) bool { return false },
	}
	result, err := scanner.Scan(context.Background(), ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	codex := findAgent(t, result, "codex")
	if codex.Status != StatusConfigFound || codex.ExecutablePath == "" {
		t.Fatalf("codex result = %+v", codex)
	}
	claude := findAgent(t, result, "claude-code")
	if claude.Status != StatusConfigFound {
		t.Fatalf("claude result = %+v", claude)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "do-not-read-this-secret") {
		t.Fatal("scan result leaked a config value")
	}
}

func TestScanRejectsReparsePoint(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "junction")
	mustDir(t, filepath.Join(root, ".codex"))
	scanner := &Scanner{
		HomeDir: home,
		Lstat:   os.Lstat,
		IsReparsePoint: func(path string, _ os.FileInfo) bool {
			return strings.Contains(strings.ToLower(path), "junction")
		},
	}
	result, err := scanner.Scan(context.Background(), ScanOptions{ExtraRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	codex := findAgent(t, result, "codex")
	if codex.Status != StatusInvalid || codex.Reason != "reparse_point_rejected" {
		t.Fatalf("reparse result = %+v", codex)
	}
	if len(codex.ConfigPaths) != 0 {
		t.Fatalf("reparse path was accepted: %v", codex.ConfigPaths)
	}
}

func TestScanIgnoresRelativeAndUNCPATH(t *testing.T) {
	scanner := &Scanner{
		HomeDir:        t.TempDir(),
		PathDirs:       []string{".", `\\server\share\bin`},
		ExecutableExts: []string{".exe"},
		Lstat:          os.Lstat,
		IsReparsePoint: func(string, os.FileInfo) bool { return false },
	}
	result, err := scanner.Scan(context.Background(), ScanOptions{ExtraRoots: []string{"relative-root"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Agents) != 0 {
		t.Fatalf("unexpected agents from unsafe paths: %+v", result.Agents)
	}
	if !containsString(result.Warnings, "extra_root_rejected") {
		t.Fatalf("warnings = %v", result.Warnings)
	}
}

func TestScanDeadlineReturnsPartialResult(t *testing.T) {
	scanner := &Scanner{
		HomeDir: t.TempDir(),
		Lstat:   os.Lstat,
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	result, err := scanner.Scan(ctx, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || !containsString(result.Warnings, "scan_deadline_reached") {
		t.Fatalf("deadline result = %+v", result)
	}
}

func TestScanBoundsExecutableSearch(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	mustFile(t, filepath.Join(second, "codex.exe"), "fixture")
	scanner := &Scanner{PathDirs: []string{first, second}, ExecutableExts: []string{".exe"}, MaxRoots: 1}
	result, err := scanner.Scan(context.Background(), ScanOptions{})
	if err != nil || len(result.Agents) != 0 || !containsString(result.Warnings, "max_roots_reached") {
		t.Fatalf("root limit result = %+v, err=%v", result, err)
	}
	scanner.MaxRoots, scanner.MaxCandidates = 2, 1
	result, err = scanner.Scan(context.Background(), ScanOptions{})
	if err != nil || len(result.Agents) != 0 || !containsString(result.Warnings, "max_candidates_reached") {
		t.Fatalf("candidate limit result = %+v, err=%v", result, err)
	}
}

func findAgent(t *testing.T, result Result, id string) Agent {
	t.Helper()
	for _, agent := range result.Agents {
		if agent.ID == id {
			return agent
		}
	}
	t.Fatalf("agent %q not found in %+v", id, result.Agents)
	return Agent{}
}

func mustDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mustFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
