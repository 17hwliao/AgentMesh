// Package hostscan performs read-only discovery of supported local agent
// installations. It intentionally does not execute scanned binaries or read
// credential files.
package hostscan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	StatusDetected         = "detected"
	StatusConfigFound      = "config_found"
	StatusPermissionDenied = "permission_denied"
	StatusInvalid          = "invalid"
)

type Agent struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Status         string    `json:"status"`
	ExecutablePath string    `json:"executable_path,omitempty"`
	ConfigPaths    []string  `json:"config_paths,omitempty"`
	LastModified   time.Time `json:"last_modified,omitempty"`
	Reason         string    `json:"reason,omitempty"`
}

type Result struct {
	Agents    []Agent   `json:"agents"`
	Truncated bool      `json:"truncated"`
	Warnings  []string  `json:"warnings,omitempty"`
	ScannedAt time.Time `json:"scanned_at"`
}

type ScanOptions struct {
	ExtraRoots []string
}

// Scanner is deliberately small and injectable so tests can exercise path
// filtering without creating symlinks that require Windows privileges.
type Scanner struct {
	HomeDir        string
	AppData        string
	LocalAppData   string
	PathDirs       []string
	ExecutableExts []string
	MaxRoots       int
	MaxCandidates  int
	Lstat          func(string) (os.FileInfo, error)
	IsReparsePoint func(string, os.FileInfo) bool
}

func New() *Scanner {
	home, _ := os.UserHomeDir()
	return &Scanner{
		HomeDir:        home,
		AppData:        os.Getenv("APPDATA"),
		LocalAppData:   os.Getenv("LOCALAPPDATA"),
		PathDirs:       filepath.SplitList(os.Getenv("PATH")),
		ExecutableExts: defaultExecutableExts(),
		MaxRoots:       64,
		MaxCandidates:  256,
		Lstat:          os.Lstat,
		IsReparsePoint: isReparsePoint,
	}
}

type agentDefinition struct {
	ID         string
	Name       string
	Executable string
	Config     func(*Scanner) []string
}

func knownAgents() []agentDefinition {
	return []agentDefinition{
		{
			ID: "codex", Name: "Codex", Executable: "codex",
			Config: func(s *Scanner) []string { return joinIfSet(s.HomeDir, ".codex") },
		},
		{
			ID: "claude-code", Name: "Claude Code", Executable: "claude",
			Config: func(s *Scanner) []string { return joinIfSet(s.HomeDir, ".claude") },
		},
		{
			ID: "gemini-cli", Name: "Gemini CLI", Executable: "gemini",
			Config: func(s *Scanner) []string { return joinIfSet(s.HomeDir, ".gemini") },
		},
		{
			ID: "opencode", Name: "OpenCode", Executable: "opencode",
			Config: func(s *Scanner) []string {
				paths := joinIfSet(s.HomeDir, ".config", "opencode")
				if s.AppData != "" {
					paths = append(paths, filepath.Join(s.AppData, "opencode"))
				}
				if s.LocalAppData != "" {
					paths = append(paths, filepath.Join(s.LocalAppData, "opencode"))
				}
				return paths
			},
		},
	}
}

func (s *Scanner) Scan(ctx context.Context, options ScanOptions) (Result, error) {
	if s == nil {
		return Result{}, errors.New("hostscan_scanner_nil")
	}
	results := Result{ScannedAt: time.Now().UTC()}
	if s.Lstat == nil {
		s.Lstat = os.Lstat
	}
	if s.IsReparsePoint == nil {
		s.IsReparsePoint = isReparsePoint
	}
	maxRoots := s.MaxRoots
	if maxRoots <= 0 {
		maxRoots = 64
	}
	maxCandidates := s.MaxCandidates
	if maxCandidates <= 0 {
		maxCandidates = 256
	}
	var extraRoots, pathDirs []string
	for _, root := range uniquePaths(options.ExtraRoots) {
		if !validExtraRoot(root) {
			results.Warnings = append(results.Warnings, "extra_root_rejected")
			continue
		}
		if len(extraRoots) == maxRoots {
			results.Truncated = true
			break
		}
		extraRoots = append(extraRoots, root)
	}
	for _, root := range uniquePaths(s.PathDirs) {
		if !validExtraRoot(root) {
			continue
		}
		if len(extraRoots)+len(pathDirs) == maxRoots {
			results.Truncated = true
			break
		}
		pathDirs = append(pathDirs, root)
	}
	if results.Truncated {
		results.Warnings = append(results.Warnings, "max_roots_reached")
	}

	checked := 0
	checkCandidate := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if checked >= maxCandidates {
			return errCandidateLimit
		}
		checked++
		return nil
	}
	for _, definition := range knownAgents() {
		if err := scanCanceled(ctx); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				results.Truncated = true
				results.Warnings = append(results.Warnings, "scan_deadline_reached")
				break
			}
			return results, err
		}
		candidates := definition.Config(s)
		for _, root := range extraRoots {
			candidates = append(candidates, filepath.Join(root, configDirName(definition.ID)))
		}
		seenPaths := map[string]bool{}
		found := Agent{ID: definition.ID, Name: definition.Name}
		var scanErr error
		for _, candidate := range candidates {
			if !validCandidatePath(candidate) || seenPaths[candidate] {
				continue
			}
			if scanErr = checkCandidate(); scanErr != nil {
				break
			}
			seenPaths[candidate] = true
			state := s.inspectConfig(candidate)
			if state.found {
				found.ConfigPaths = append(found.ConfigPaths, candidate)
				if state.modified.After(found.LastModified) {
					found.LastModified = state.modified
				}
			}
			if state.permission {
				found.Status = StatusPermissionDenied
			}
			if state.invalid && found.Reason == "" {
				found.Reason = "reparse_point_rejected"
			}
		}
		if len(found.ConfigPaths) > 0 {
			found.Status = StatusConfigFound
		}
		var path string
		if scanErr == nil {
			path, scanErr = s.findExecutable(pathDirs, definition.Executable, checkCandidate)
		}
		stopped := false
		switch {
		case errors.Is(scanErr, context.DeadlineExceeded):
			results.Truncated = true
			results.Warnings = append(results.Warnings, "scan_deadline_reached")
			stopped = true
		case errors.Is(scanErr, errCandidateLimit):
			results.Truncated = true
			results.Warnings = append(results.Warnings, "max_candidates_reached")
			stopped = true
		case errors.Is(scanErr, context.Canceled):
			return results, scanErr
		case scanErr == nil && path != "":
			found.ExecutablePath = path
			if found.Status == "" {
				found.Status = StatusDetected
			}
			if info, err := s.Lstat(path); err == nil && info.ModTime().After(found.LastModified) {
				found.LastModified = info.ModTime().UTC()
			}
		case scanErr != nil && !errors.Is(scanErr, errExecutableNotFound):
			found.Status = StatusInvalid
			found.Reason = "executable_lookup_failed"
		}
		if stopped {
			if found.Status != "" || len(found.ConfigPaths) > 0 {
				sort.Strings(found.ConfigPaths)
				results.Agents = append(results.Agents, found)
			}
			break
		}
		if found.Status == "" && len(found.ConfigPaths) == 0 {
			if found.Reason != "" {
				found.Status = StatusInvalid
			} else {
				continue
			}
		}
		if found.Status == "" {
			found.Status = StatusDetected
		}
		sort.Strings(found.ConfigPaths)
		results.Agents = append(results.Agents, found)
	}
	sort.Slice(results.Agents, func(i, j int) bool { return results.Agents[i].ID < results.Agents[j].ID })
	return results, nil
}

type configState struct {
	found      bool
	permission bool
	invalid    bool
	modified   time.Time
}

func (s *Scanner) inspectConfig(path string) configState {
	if s.hasReparseAncestor(path) {
		return configState{invalid: true}
	}
	info, err := s.Lstat(path)
	if err != nil {
		if os.IsPermission(err) {
			return configState{permission: true}
		}
		return configState{}
	}
	if info.Mode()&os.ModeSymlink != 0 || s.IsReparsePoint(path, info) {
		return configState{invalid: true}
	}
	if !info.IsDir() {
		return configState{}
	}
	if !canReadDirectory(path) {
		return configState{permission: true}
	}
	return configState{found: true, modified: info.ModTime().UTC()}
}

func (s *Scanner) findExecutable(dirs []string, name string, checkCandidate func() error) (string, error) {
	extensions := s.ExecutableExts
	if len(extensions) == 0 {
		extensions = defaultExecutableExts()
	}
	for _, dir := range dirs {
		for _, extension := range extensions {
			if err := checkCandidate(); err != nil {
				return "", err
			}
			candidate := filepath.Join(dir, name+extension)
			if s.hasReparseAncestor(candidate) {
				continue
			}
			info, err := s.Lstat(candidate)
			if err != nil {
				if os.IsPermission(err) {
					return "", err
				}
				continue
			}
			if info.Mode()&os.ModeSymlink != 0 || s.IsReparsePoint(candidate, info) || !info.Mode().IsRegular() {
				continue
			}
			return candidate, nil
		}
	}
	return "", errExecutableNotFound
}

func (s *Scanner) hasReparseAncestor(path string) bool {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	volume := filepath.VolumeName(absolute)
	current := volume + string(os.PathSeparator)
	rest := strings.TrimPrefix(absolute, current)
	for _, part := range strings.Split(rest, string(os.PathSeparator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := s.Lstat(current)
		if err != nil {
			return false
		}
		if info.Mode()&os.ModeSymlink != 0 || s.IsReparsePoint(current, info) {
			return true
		}
		if !info.IsDir() && current != absolute {
			return true
		}
	}
	return false
}

func canReadDirectory(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	return file.Close() == nil
}

func joinIfSet(root string, parts ...string) []string {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	return []string{filepath.Join(append([]string{root}, parts...)...)}
}

func configDirName(id string) string {
	switch id {
	case "codex":
		return ".codex"
	case "claude-code":
		return ".claude"
	case "gemini-cli":
		return ".gemini"
	case "opencode":
		return "opencode"
	default:
		return id
	}
}

func uniquePaths(paths []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

func validCandidatePath(path string) bool {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) || isUNC(path) {
		return false
	}
	return true
}

func validExtraRoot(path string) bool {
	return validCandidatePath(path)
}

func isUNC(path string) bool {
	return strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//")
}

func defaultExecutableExts() []string {
	if runtime.GOOS != "windows" {
		return []string{""}
	}
	raw := strings.TrimSpace(os.Getenv("PATHEXT"))
	if raw == "" {
		return []string{".exe", ".cmd", ".bat", ".com"}
	}
	seen := map[string]bool{}
	out := make([]string, 0, 5)
	for _, item := range strings.Split(raw, ";") {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	if len(out) == 0 {
		return []string{".exe", ".cmd", ".bat", ".com"}
	}
	return out
}

func scanCanceled(ctx context.Context) error {
	return ctx.Err()
}

var errExecutableNotFound = errors.New("hostscan_executable_not_found")
var errCandidateLimit = errors.New("hostscan_candidate_limit")
