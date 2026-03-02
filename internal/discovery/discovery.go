// Package discovery scans ~/.claude/projects/ for Claude Code session
// JSONL files, providing filesystem-backed session metadata.
package discovery

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SessionInfo holds metadata extracted from a session JSONL file.
type SessionInfo struct {
	UUID       string    // session UUID (filename stem)
	ProjectDir string    // encoded project directory name
	WorkDir    string    // working directory (from JSONL cwd field)
	GitBranch  string    // git branch (from JSONL gitBranch field)
	ModTime    time.Time // last modification time of the JSONL file
	Active     bool      // true if a claude process has this session's JSONL open
}

type cacheEntry struct {
	info    SessionInfo
	fetched time.Time
}

// Scanner walks a Claude Code projects directory for session files.
type Scanner struct {
	baseDir  string
	cacheTTL time.Duration

	mu    sync.Mutex
	cache map[string]cacheEntry // keyed by UUID

	activeMu      sync.Mutex
	activeCache   map[string]bool
	activeFetched time.Time
}

// NewScanner creates a Scanner rooted at baseDir (typically ~/.claude/projects).
func NewScanner(baseDir string) *Scanner {
	return &Scanner{
		baseDir:  baseDir,
		cacheTTL: 60 * time.Second,
		cache:    make(map[string]cacheEntry),
	}
}

// Scan returns all sessions whose JSONL files were modified within maxAge.
// If maxAge is 0, all sessions are returned.
func (s *Scanner) Scan(maxAge time.Duration) ([]SessionInfo, error) {
	cutoff := time.Time{}
	if maxAge > 0 {
		cutoff = time.Now().Add(-maxAge)
	}

	projDirs, err := os.ReadDir(s.baseDir)
	if err != nil {
		return nil, err
	}

	active := s.activeUUIDs()

	var results []SessionInfo
	for _, pd := range projDirs {
		if !pd.IsDir() {
			continue
		}
		// Skip memory directories.
		if pd.Name() == "memory" {
			continue
		}
		projPath := filepath.Join(s.baseDir, pd.Name())
		entries, err := os.ReadDir(projPath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			uuid := strings.TrimSuffix(e.Name(), ".jsonl")
			if !IsUUID(uuid) {
				continue
			}

			fi, err := e.Info()
			if err != nil {
				continue
			}
			if !cutoff.IsZero() && fi.ModTime().Before(cutoff) {
				continue
			}

			info, err := s.getOrFetch(uuid, pd.Name(), projPath, fi.ModTime())
			if err != nil {
				continue
			}
			info.Active = active[uuid]
			results = append(results, info)
		}
	}
	return results, nil
}

// Get looks up a single session by UUID across all project directories.
func (s *Scanner) Get(uuid string) (*SessionInfo, error) {
	// Check cache first.
	s.mu.Lock()
	if ce, ok := s.cache[uuid]; ok && time.Since(ce.fetched) < s.cacheTTL {
		s.mu.Unlock()
		info := ce.info
		return &info, nil
	}
	s.mu.Unlock()

	// Walk project dirs to find the JSONL file.
	projDirs, err := os.ReadDir(s.baseDir)
	if err != nil {
		return nil, err
	}
	for _, pd := range projDirs {
		if !pd.IsDir() || pd.Name() == "memory" {
			continue
		}
		jsonlPath := filepath.Join(s.baseDir, pd.Name(), uuid+".jsonl")
		fi, err := os.Stat(jsonlPath)
		if err != nil {
			continue
		}
		info, err := s.getOrFetch(uuid, pd.Name(), filepath.Join(s.baseDir, pd.Name()), fi.ModTime())
		if err != nil {
			return nil, err
		}
		return &info, nil
	}
	return nil, nil
}

func (s *Scanner) getOrFetch(uuid, projDir, projPath string, modTime time.Time) (SessionInfo, error) {
	s.mu.Lock()
	if ce, ok := s.cache[uuid]; ok && time.Since(ce.fetched) < s.cacheTTL {
		// Update modtime in case it changed.
		ce.info.ModTime = modTime
		s.mu.Unlock()
		return ce.info, nil
	}
	s.mu.Unlock()

	info := SessionInfo{
		UUID:       uuid,
		ProjectDir: projDir,
		ModTime:    modTime,
	}

	// Read first ~20 lines to extract cwd and gitBranch.
	jsonlPath := filepath.Join(projPath, uuid+".jsonl")
	if err := extractMetadata(jsonlPath, &info); err != nil {
		// Still return what we have — UUID and modtime are useful.
		info.WorkDir = decodeProjectDir(projDir)
	}

	s.mu.Lock()
	s.cache[uuid] = cacheEntry{info: info, fetched: time.Now()}
	s.mu.Unlock()

	return info, nil
}

// extractMetadata reads the first lines of a JSONL file to extract
// cwd and gitBranch fields.
func extractMetadata(path string, info *SessionInfo) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for i := 0; i < 20 && scanner.Scan(); i++ {
		var line struct {
			CWD       string `json:"cwd"`
			GitBranch string `json:"gitBranch"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		if line.CWD != "" && info.WorkDir == "" {
			info.WorkDir = line.CWD
		}
		if line.GitBranch != "" && info.GitBranch == "" {
			info.GitBranch = line.GitBranch
		}
		if info.WorkDir != "" && info.GitBranch != "" {
			break
		}
	}
	return scanner.Err()
}

// decodeProjectDir is a best-effort decode of the encoded project directory
// name. The encoding is lossy (both / and . become -), so this is only
// used as a fallback when JSONL parsing fails.
func decodeProjectDir(encoded string) string {
	// Strip leading dash and replace remaining dashes with /.
	if strings.HasPrefix(encoded, "-") {
		encoded = encoded[1:]
	}
	return "/" + strings.ReplaceAll(encoded, "-", "/")
}

// IsActive checks if a session UUID is currently open by a claude process.
func (s *Scanner) IsActive(uuid string) bool {
	return s.activeUUIDs()[uuid]
}

// activeUUIDs returns the set of session UUIDs that are currently in use
// by a running claude process. Detected via process args (--resume <uuid>).
// Results are cached for 5 seconds.
func (s *Scanner) activeUUIDs() map[string]bool {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()

	if s.activeCache != nil && time.Since(s.activeFetched) < 5*time.Second {
		return s.activeCache
	}

	result := make(map[string]bool)

	// Parse ps output for claude processes with --resume <uuid>.
	out, err := exec.Command("ps", "-eo", "args=").Output()
	if err != nil {
		s.activeCache = result
		s.activeFetched = time.Now()
		return result
	}

	for _, line := range strings.Split(string(out), "\n") {
		// Match lines that look like claude invocations with --resume.
		idx := strings.Index(line, "--resume ")
		if idx < 0 {
			continue
		}
		// Extract the token after --resume.
		rest := line[idx+len("--resume "):]
		token := rest
		if sp := strings.IndexByte(rest, ' '); sp >= 0 {
			token = rest[:sp]
		}
		if IsUUID(token) {
			result[token] = true
		}
	}

	s.activeCache = result
	s.activeFetched = time.Now()
	return result
}

// IsUUID checks if a string looks like a UUID (36 chars with hyphens at
// positions 8, 13, 18, 23).
func IsUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}
