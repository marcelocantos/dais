// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/jevons/internal/discovery"
)

// SetScanner attaches the discovery scanner to the MCP server and registers
// the jevons_active_work tool.
func (s *Server) SetScanner(scanner *discovery.Scanner) {
	s.scanner = scanner

	s.mcpSrv.AddTool(
		mcp.NewTool("jevons_active_work",
			mcp.WithDescription("Show a unified dashboard of active work across all repos. Cross-references recent Claude Code sessions, dirty working trees, and open PRs to produce a ranked view of where work is happening."),
			mcp.WithNumber("hours", mcp.Description("How far back to look for recent sessions (default 72)")),
			mcp.WithBoolean("include_clean", mcp.Description("Include repos with no detected activity (default false)")),
		),
		s.handleActiveWork,
	)
}

// repoInfo aggregates all three signals for a single repo.
type repoInfo struct {
	orgRepo     string    // "org/repo"
	repoPath    string    // absolute filesystem path
	lastActivity time.Time
	sessionCount int
	activeCount  int
	changedFiles int
	branch       string
	prs          []prInfo
	agentName    string
}

type prInfo struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Head   string `json:"headRefName"`
	State  string `json:"state"`
}

func (s *Server) handleActiveWork(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()

	hoursF, _ := args["hours"].(float64)
	if hoursF <= 0 {
		hoursF = 72
	}
	hours := time.Duration(hoursF) * time.Hour

	includeClean, _ := args["include_clean"].(bool)

	if s.scanner == nil {
		return mcp.NewToolResultError("discovery scanner not configured"), nil
	}

	// ── Signal 1: recent sessions ──────────────────────────────────────────
	sessions, err := s.scanner.Scan(hours)
	if err != nil {
		slog.Warn("active_work: session scan failed", "err", err)
	}

	type sessionGroup struct {
		count      int
		activeCount int
		lastMod    time.Time
	}
	sessionsByPath := make(map[string]*sessionGroup) // WorkDir → group
	for _, si := range sessions {
		if si.WorkDir == "" {
			continue
		}
		g := sessionsByPath[si.WorkDir]
		if g == nil {
			g = &sessionGroup{}
			sessionsByPath[si.WorkDir] = g
		}
		g.count++
		if si.Active {
			g.activeCount++
		}
		if si.ModTime.After(g.lastMod) {
			g.lastMod = si.ModTime
		}
	}

	// ── Signal 2: dirty working trees ─────────────────────────────────────
	workBase := filepath.Join(os.Getenv("HOME"), "work", "github.com")
	gitRepos := collectGitRepos(workBase)

	type gitStatus struct {
		changedFiles int
		branch       string
	}
	gitResults := make(map[string]gitStatus, len(gitRepos))
	var gitMu sync.Mutex

	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, repoPath := range gitRepos {
		wg.Add(1)
		sem <- struct{}{}
		go func(path string) {
			defer wg.Done()
			defer func() { <-sem }()

			changed := gitChangedCount(path)
			branch := gitCurrentBranch(path)

			gitMu.Lock()
			gitResults[path] = gitStatus{changedFiles: changed, branch: branch}
			gitMu.Unlock()
		}(repoPath)
	}
	wg.Wait()

	// ── Build unified repo map ─────────────────────────────────────────────
	repoMap := make(map[string]*repoInfo) // orgRepo → info

	orgRepoFromPath := func(path string) string {
		parts := strings.SplitN(path, "github.com/", 2)
		if len(parts) == 2 {
			return parts[1]
		}
		return ""
	}

	// Add repos from sessions.
	for workDir, g := range sessionsByPath {
		or := orgRepoFromPath(workDir)
		if or == "" {
			continue
		}
		ri := repoMap[or]
		if ri == nil {
			ri = &repoInfo{orgRepo: or, repoPath: workDir}
			repoMap[or] = ri
		}
		ri.sessionCount += g.count
		ri.activeCount += g.activeCount
		if g.lastMod.After(ri.lastActivity) {
			ri.lastActivity = g.lastMod
		}
	}

	// Add repos from git results.
	for repoPath, gs := range gitResults {
		or := orgRepoFromPath(repoPath)
		if or == "" {
			continue
		}
		ri := repoMap[or]
		if ri == nil {
			ri = &repoInfo{orgRepo: or, repoPath: repoPath}
			repoMap[or] = ri
		}
		ri.changedFiles = gs.changedFiles
		ri.branch = gs.branch
	}

	// ── Signal 3: open PRs (only for repos in the map) ────────────────────
	prResults := fetchPRs(repoMap)
	for or, prs := range prResults {
		if ri, ok := repoMap[or]; ok {
			ri.prs = prs
		}
	}

	// ── Signal 4: active agents from registry ─────────────────────────────
	if s.registry != nil {
		for _, def := range s.registry.List() {
			if def.WorkDir == "" {
				continue
			}
			or := orgRepoFromPath(def.WorkDir)
			if or == "" {
				continue
			}
			if ri, ok := repoMap[or]; ok {
				ri.agentName = def.Name
			}
		}
	}

	// ── Filter and sort ────────────────────────────────────────────────────
	var repos []*repoInfo
	for _, ri := range repoMap {
		if !includeClean && ri.sessionCount == 0 && ri.changedFiles == 0 && len(ri.prs) == 0 {
			continue
		}
		repos = append(repos, ri)
	}

	sort.Slice(repos, func(i, j int) bool {
		ti, tj := repos[i].lastActivity, repos[j].lastActivity
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return repos[i].orgRepo < repos[j].orgRepo
	})

	if len(repos) == 0 {
		return mcp.NewToolResultText("No active work detected."), nil
	}

	// ── Render table ───────────────────────────────────────────────────────
	text := renderActiveWorkTable(repos)
	return mcp.NewToolResultText(text), nil
}

// collectGitRepos walks two levels under base (org/repo) and returns
// paths that contain a .git directory.
func collectGitRepos(base string) []string {
	orgs, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var repos []string
	for _, org := range orgs {
		if !org.IsDir() {
			continue
		}
		orgPath := filepath.Join(base, org.Name())
		repoEntries, err := os.ReadDir(orgPath)
		if err != nil {
			continue
		}
		for _, repo := range repoEntries {
			if !repo.IsDir() {
				continue
			}
			repoPath := filepath.Join(orgPath, repo.Name())
			if _, err := os.Stat(filepath.Join(repoPath, ".git")); err == nil {
				repos = append(repos, repoPath)
			}
		}
	}
	return repos
}

// gitChangedCount returns the number of changed lines from `git status --porcelain`.
func gitChangedCount(repoPath string) int {
	out, err := exec.Command("git", "-C", repoPath, "status", "--porcelain").Output()
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// gitCurrentBranch returns the current branch name for a repo.
func gitCurrentBranch(repoPath string) string {
	out, err := exec.Command("git", "-C", repoPath, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// fetchPRs queries GitHub for open PRs for each repo in the map.
// Uses bounded concurrency (4 goroutines).
func fetchPRs(repoMap map[string]*repoInfo) map[string][]prInfo {
	results := make(map[string][]prInfo)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)

	for or := range repoMap {
		wg.Add(1)
		sem <- struct{}{}
		go func(orgRepo string) {
			defer wg.Done()
			defer func() { <-sem }()

			prs := queryGHPRs(orgRepo)

			mu.Lock()
			results[orgRepo] = prs
			mu.Unlock()
		}(or)
	}
	wg.Wait()
	return results
}

// queryGHPRs runs `gh pr list` and parses the JSON output.
func queryGHPRs(orgRepo string) []prInfo {
	out, err := exec.Command(
		"gh", "pr", "list",
		"--repo", orgRepo,
		"--json", "number,title,headRefName,state",
		"--limit", "5",
	).Output()
	if err != nil {
		// gh exits non-zero when there are no PRs or the repo is not found; ignore.
		return nil
	}
	var prs []prInfo
	if err := json.Unmarshal(out, &prs); err != nil {
		slog.Warn("active_work: gh pr parse failed", "repo", orgRepo, "err", err)
		return nil
	}
	return prs
}

// renderActiveWorkTable builds the text table output.
func renderActiveWorkTable(repos []*repoInfo) string {
	const (
		colRepo     = 30
		colActivity = 22
		colSessions = 9
		colTree     = 16
		colBranch   = 14
		colPRs      = 14
		colAgent    = 14
	)

	header := fmt.Sprintf("%-*s  %-*s  %*s  %-*s  %-*s  %-*s  %-*s",
		colRepo, "Repo",
		colActivity, "Last Activity",
		colSessions, "Sessions",
		colTree, "Working Tree",
		colBranch, "Branch",
		colPRs, "PRs",
		colAgent, "Agent",
	)
	divider := strings.Repeat("─", len(header))

	var b strings.Builder
	b.WriteString(header)
	b.WriteByte('\n')
	b.WriteString(divider)
	b.WriteByte('\n')

	for _, ri := range repos {
		activityStr := "-"
		if !ri.lastActivity.IsZero() {
			activityStr = ri.lastActivity.Format("2006-01-02 15:04")
		}

		sessionStr := "-"
		if ri.sessionCount > 0 {
			sessionStr = fmt.Sprintf("%d", ri.sessionCount)
			if ri.activeCount > 0 {
				sessionStr += fmt.Sprintf("(%da)", ri.activeCount)
			}
		}

		treeStr := "clean"
		if ri.changedFiles > 0 {
			treeStr = fmt.Sprintf("%d changed", ri.changedFiles)
		}

		branchStr := ri.branch
		if branchStr == "" {
			branchStr = "-"
		}

		prStr := formatPRs(ri.prs)

		agentStr := ri.agentName
		if agentStr == "" {
			agentStr = "-"
		}

		fmt.Fprintf(&b, "%-*s  %-*s  %*s  %-*s  %-*s  %-*s  %-*s\n",
			colRepo, truncateStr(ri.orgRepo, colRepo),
			colActivity, activityStr,
			colSessions, sessionStr,
			colTree, truncateStr(treeStr, colTree),
			colBranch, truncateStr(branchStr, colBranch),
			colPRs, truncateStr(prStr, colPRs),
			colAgent, truncateStr(agentStr, colAgent),
		)
	}

	return b.String()
}

// formatPRs summarises a list of PRs into a short string.
func formatPRs(prs []prInfo) string {
	if len(prs) == 0 {
		return "-"
	}
	open := 0
	for _, p := range prs {
		if strings.EqualFold(p.State, "OPEN") {
			open++
		}
	}
	if open == 1 {
		return fmt.Sprintf("#%d open", prs[0].Number)
	}
	return fmt.Sprintf("%d open", open)
}

// truncateStr truncates s to max runes, appending "…" if needed.
func truncateStr(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}
