package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type FileSystemTool struct {
	AllowedPaths []string
}

// resolvePath expands ~ and makes path absolute relative to $HOME.
func resolvePath(path string) string {
	if path == "" {
		return os.Getenv("HOME")
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(os.Getenv("HOME"), path[2:])
	}
	if !filepath.IsAbs(path) {
		return filepath.Join(os.Getenv("HOME"), path)
	}
	return path
}

// ReadFile reads a file and returns its content.
type readFileInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (t *FileSystemTool) ReadFile(_ context.Context, raw json.RawMessage) (string, error) {
	var inp readFileInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	path := resolvePath(inp.Path)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("cannot access %s: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, use list_dir instead", path)
	}
	const maxSize = 100 * 1024 // 100KB
	if info.Size() > maxSize {
		return fmt.Sprintf("File %s is %.1fKB (limit 100KB). Use read_file with offset/limit to read parts.", path, float64(info.Size())/1024), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	lines := strings.Split(string(data), "\n")
	total := len(lines)

	start := 0
	if inp.Offset > 0 {
		start = inp.Offset - 1 // 1-indexed
	}
	end := total
	if inp.Limit > 0 && start+inp.Limit < total {
		end = start + inp.Limit
	}
	if start >= total {
		return fmt.Sprintf("File has %d lines, offset %d is out of range", total, inp.Offset), nil
	}

	selected := lines[start:end]
	content := strings.Join(selected, "\n")

	header := fmt.Sprintf("File: %s (%d lines total", path, total)
	if inp.Offset > 0 || inp.Limit > 0 {
		header += fmt.Sprintf(", showing lines %d-%d", start+1, end)
	}
	header += ")\n"

	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	return header + "```" + ext + "\n" + content + "\n```", nil
}

// WriteFile writes content to a file.
type writeFileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Append  bool   `json:"append"`
}

func (t *FileSystemTool) WriteFile(_ context.Context, raw json.RawMessage) (string, error) {
	var inp writeFileInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	path := resolvePath(inp.Path)

	// Create parent directories
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("create directories: %w", err)
	}

	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if inp.Append {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}

	f, err := os.OpenFile(path, flag, 0644)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	n, err := f.WriteString(inp.Content)
	if err != nil {
		return "", fmt.Errorf("write: %w", err)
	}

	action := "Written"
	if inp.Append {
		action = "Appended"
	}
	return fmt.Sprintf("%s %d bytes to %s", action, n, path), nil
}

// ListDir lists directory contents.
type listDirInput struct {
	Path       string `json:"path"`
	Recursive  bool   `json:"recursive"`
	ShowHidden bool   `json:"show_hidden"`
}

type dirEntry struct {
	name  string
	size  int64
	isDir bool
}

func (t *FileSystemTool) ListDir(_ context.Context, raw json.RawMessage) (string, error) {
	var inp listDirInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	path := resolvePath(inp.Path)

	if inp.Recursive {
		return t.listRecursive(path, inp.ShowHidden)
	}
	return t.listFlat(path, inp.ShowHidden)
}

func (t *FileSystemTool) listFlat(path string, showHidden bool) (string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("list %s: %w", path, err)
	}

	var items []dirEntry
	for _, e := range entries {
		if !showHidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, _ := e.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		items = append(items, dirEntry{name: e.Name(), size: size, isDir: e.IsDir()})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].isDir != items[j].isDir {
			return items[i].isDir
		}
		return items[i].name < items[j].name
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s/ (%d items)\n", path, len(items)))
	for _, item := range items {
		if item.isDir {
			sb.WriteString(fmt.Sprintf("  📁 %s/\n", item.name))
		} else {
			sb.WriteString(fmt.Sprintf("  📄 %-40s %s\n", item.name, humanSize(item.size)))
		}
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

func (t *FileSystemTool) listRecursive(root string, showHidden bool) (string, error) {
	var lines []string
	wf := newWalkFilter(root)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && path != root && wf.shouldSkip(path, info) {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		parts := strings.Split(rel, string(filepath.Separator))
		for _, p := range parts {
			if !showHidden && strings.HasPrefix(p, ".") {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		depth := len(parts) - 1
		indent := strings.Repeat("  ", depth)
		if info.IsDir() {
			lines = append(lines, fmt.Sprintf("%s📁 %s/", indent, info.Name()))
		} else {
			lines = append(lines, fmt.Sprintf("%s📄 %-30s %s", indent, info.Name(), humanSize(info.Size())))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	header := fmt.Sprintf("%s/ (%d items)\n", root, len(lines))
	return header + strings.Join(lines, "\n"), nil
}

// SearchFiles searches for files by name or content.
type searchInput struct {
	Path          string `json:"path"`
	Pattern       string `json:"pattern"`
	SearchContent bool   `json:"search_content"`
	FilePattern   string `json:"file_pattern"`
}

func (t *FileSystemTool) SearchFiles(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp searchInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	searchPath := resolvePath(inp.Path)

	if inp.SearchContent {
		// Use grep for content search
		args := []string{"-r", "--include=" + inp.FilePattern, "-n", "-m", "5", inp.Pattern, searchPath}
		if inp.FilePattern == "" {
			args = []string{"-r", "-n", "-m", "5", inp.Pattern, searchPath}
		}
		shell := &ShellTool{DefaultTimeout: 30, MaxOutputBytes: 4096}
		cmd := fmt.Sprintf("grep -r -n --include='%s' -m 5 %q %q 2>/dev/null | head -100", inp.FilePattern, inp.Pattern, searchPath)
		if inp.FilePattern == "" {
			cmd = fmt.Sprintf("grep -r -n -m 5 %q %q 2>/dev/null | head -100", inp.Pattern, searchPath)
		}
		_ = args
		return shell.Run(ctx, mustJSON(map[string]interface{}{
			"command": cmd,
		}))
	}

	// Search by filename
	var matches []string
	wf := newWalkFilter(searchPath)
	err := filepath.Walk(searchPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && path != searchPath && wf.shouldSkip(path, info) {
			return filepath.SkipDir
		}
		if strings.Contains(strings.ToLower(info.Name()), strings.ToLower(inp.Pattern)) {
			rel, _ := filepath.Rel(searchPath, path)
			if info.IsDir() {
				matches = append(matches, "📁 "+rel+"/")
			} else {
				matches = append(matches, fmt.Sprintf("📄 %-40s %s", rel, humanSize(info.Size())))
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	if len(matches) == 0 {
		return fmt.Sprintf("No files matching %q in %s", inp.Pattern, searchPath), nil
	}

	header := fmt.Sprintf("Found %d matches for %q in %s:\n", len(matches), inp.Pattern, searchPath)
	if len(matches) > 50 {
		matches = matches[:50]
		header += "(showing first 50)\n"
	}
	return header + strings.Join(matches, "\n"), nil
}

func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func mustJSON(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
