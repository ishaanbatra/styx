package update

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// InstallManager reports the Windows package manager that owns the running
// executable, or an empty string for a directly installed executable.
func InstallManager() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve styx executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", fmt.Errorf("resolve styx executable symlinks: %w", err)
	}
	return installManager(runtime.GOOS, resolved, os.Getenv("SCOOP"), os.Getenv("LOCALAPPDATA")), nil
}

func installManager(goos, executable, scoopRoot, localAppData string) string {
	if goos != "windows" {
		return ""
	}
	path := normalizePath(executable)
	if strings.Contains(path, "/scoop/apps/") || strings.Contains(path, "/scoop/shims/") ||
		isUnder(path, normalizePath(scoopRoot)) {
		return "Scoop"
	}
	wingetRoot := normalizePath(filepath.Join(localAppData, "Microsoft", "WinGet", "Packages"))
	if isUnder(path, wingetRoot) {
		return "WinGet"
	}
	return ""
}

func normalizePath(path string) string {
	path = strings.ReplaceAll(strings.TrimSpace(path), `\`, "/")
	path = filepath.ToSlash(filepath.Clean(path))
	return strings.ToLower(path)
}

func isUnder(path, root string) bool {
	if root == "" || root == "." {
		return false
	}
	root = strings.TrimSuffix(root, "/")
	return path == root || strings.HasPrefix(path, root+"/")
}
