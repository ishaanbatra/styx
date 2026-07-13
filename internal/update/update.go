// Package update provides Styx's non-blocking release check and explicit
// self-update machinery.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	selfupdate "github.com/creativeprojects/go-selfupdate"
	"github.com/gofrs/flock"
	"github.com/mattn/go-isatty"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

const (
	latestURL      = "https://github.com/ishaanbatra/styx/releases/latest/download/latest.json"
	cacheDirEnv    = "STYX_UPDATE_CACHE_DIR"
	checkTimeout   = 2 * time.Second
	cacheFreshness = 24 * time.Hour
)

type latestRelease struct {
	Version string `json:"version"`
}

var launchConfig struct {
	sync.RWMutex
	version string
	quiet   bool
}

var streamsTTY = allStreamsTTY

// ConfigureLaunch supplies the current display version and parsed quiet flag
// used by MaybeNotify. It should be called once, after global flag parsing.
func ConfigureLaunch(version string, quiet bool) {
	launchConfig.Lock()
	defer launchConfig.Unlock()
	launchConfig.version = version
	launchConfig.quiet = quiet
}

// Check refreshes the cached latest-release metadata when it is stale. It is
// safe for concurrent Styx processes: only the process that wins latest.lock
// performs the request.
func Check(ctx context.Context) error {
	client := &http.Client{Timeout: checkTimeout}
	return check(ctx, client, latestURL, time.Now())
}

func check(ctx context.Context, client *http.Client, url string, now time.Time) (retErr error) {
	dir, latestPath, lockPath, err := cachePaths()
	if err != nil {
		return err
	}
	if cacheFresh(latestPath, now) {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create update cache directory: %w", err)
	}

	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("lock update cache: %w", err)
	}
	if !locked {
		return nil
	}
	defer func() {
		if err := lock.Unlock(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("unlock update cache: %w", err))
		}
	}()

	// Another process may have refreshed the file while this process waited to
	// acquire the advisory lock, so freshness must be checked again here.
	if cacheFresh(latestPath, now) {
		return nil
	}

	requestCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("create latest release request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch latest release: unexpected HTTP status %s", resp.Status)
	}

	var latest latestRelease
	dec := json.NewDecoder(io.LimitReader(resp.Body, 64<<10))
	if err := dec.Decode(&latest); err != nil {
		return fmt.Errorf("decode latest release: %w", err)
	}
	if _, err := parseVersion(latest.Version); err != nil {
		return fmt.Errorf("decode latest release: %w", err)
	}
	latest.Version = displayVersion(latest.Version)
	body, err := json.Marshal(latest)
	if err != nil {
		return fmt.Errorf("encode latest release cache: %w", err)
	}
	body = append(body, '\n')
	if err := atomicWrite(latestPath, body); err != nil {
		return fmt.Errorf("write latest release cache: %w", err)
	}
	return nil
}

func cachePaths() (dir, latestPath, lockPath string, err error) {
	dir = strings.TrimSpace(os.Getenv(cacheDirEnv))
	if dir == "" {
		dir, err = os.UserCacheDir()
		if err != nil {
			return "", "", "", fmt.Errorf("resolve user cache directory: %w", err)
		}
		dir = filepath.Join(dir, "styx")
	}
	return dir, filepath.Join(dir, "latest.json"), filepath.Join(dir, "latest.lock"), nil
}

func cacheFresh(path string, now time.Time) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	age := now.Sub(info.ModTime())
	return age < cacheFreshness
}

func atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "latest-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temporary cache file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("set temporary cache permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary cache file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temporary cache file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary cache file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace update cache: %w", err)
	}
	return nil
}

// MaybeNotify reads only the local cache and writes an update notice when the
// invocation is interactive and a newer release is available.
func MaybeNotify(w io.Writer) error {
	launchConfig.RLock()
	version, quiet := launchConfig.version, launchConfig.quiet
	launchConfig.RUnlock()
	if quiet || version == "" || isDevelopmentVersion(version) ||
		os.Getenv("STYX_NO_UPDATE_CHECK") != "" || os.Getenv("DO_NOT_TRACK") == "1" ||
		!streamsTTY() {
		return nil
	}

	_, latestPath, _, err := cachePaths()
	if err != nil {
		return err
	}
	latest, err := readLatest(latestPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	comparison, err := compareVersions(latest.Version, version)
	if err != nil {
		return err
	}
	if comparison <= 0 {
		return nil
	}
	_, err = fmt.Fprintf(w, "[styx] a new, litter, trickier styx is available: %s (run 'styx update')\n", displayVersion(latest.Version))
	if err != nil {
		return fmt.Errorf("write update notice: %w", err)
	}
	return nil
}

func allStreamsTTY() bool {
	return isTTY(os.Stdin) && isTTY(os.Stdout) && isTTY(os.Stderr)
}

func isTTY(f *os.File) bool {
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

func readLatest(path string) (latestRelease, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return latestRelease{}, fmt.Errorf("read latest release cache: %w", err)
	}
	var latest latestRelease
	if err := json.Unmarshal(data, &latest); err != nil {
		return latestRelease{}, fmt.Errorf("decode latest release cache: %w", err)
	}
	if _, err := parseVersion(latest.Version); err != nil {
		return latestRelease{}, fmt.Errorf("decode latest release cache: %w", err)
	}
	return latest, nil
}

func compareVersions(a, b string) (int, error) {
	av, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	bv, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	return semver.Compare(av, bv), nil
}

func parseVersion(version string) (string, error) {
	version = strings.TrimSpace(version)
	version = strings.TrimPrefix(version, "v")
	canonical := "v" + version
	if !semver.IsValid(canonical) {
		return "", fmt.Errorf("invalid release version %q", version)
	}
	return canonical, nil
}

func displayVersion(version string) string {
	parsed, err := parseVersion(version)
	if err != nil {
		return version
	}
	return parsed
}

func isDevelopmentVersion(version string) bool {
	version = strings.TrimSpace(version)
	if strings.HasSuffix(version, "-dev") || strings.HasPrefix(version, "dev-") {
		return true
	}
	// `go build` inside a git checkout stamps a VCS pseudo-version
	// (optionally +dirty); those are source builds, never nag or replace them.
	base, meta, hasMeta := strings.Cut(version, "+")
	if hasMeta && strings.Contains(meta, "dirty") {
		return true
	}
	if !strings.HasPrefix(base, "v") {
		base = "v" + base
	}
	if module.IsPseudoVersion(base) {
		return true
	}
	_, err := parseVersion(version)
	return err != nil
}

// SelfUpdate replaces the current executable with the newest GitHub release,
// validating the selected archive against checksums.txt first.
func SelfUpdate(ctx context.Context, currentVersion string) (string, error) {
	if isDevelopmentVersion(currentVersion) {
		return "", errors.New("development builds cannot self-update; install a released build first")
	}
	if manager, err := InstallManager(); err != nil {
		return "", err
	} else if manager != "" {
		return "", fmt.Errorf("%s owns this styx installation; use %s to update it", manager, managerUpdateCommand(manager))
	}

	updater, err := selfupdate.NewUpdater(selfupdate.Config{
		Validator: &selfupdate.ChecksumValidator{UniqueFilename: "checksums.txt"},
	})
	if err != nil {
		return "", fmt.Errorf("create self-updater: %w", err)
	}
	release, err := updater.UpdateSelf(ctx, currentVersion, selfupdate.NewRepositorySlug("ishaanbatra", "styx"))
	if err != nil {
		return "", fmt.Errorf("update styx: %w", err)
	}
	return displayVersion(release.Version()), nil
}

func managerUpdateCommand(manager string) string {
	switch manager {
	case "Scoop":
		return "scoop update styx"
	case "WinGet":
		return "winget upgrade styx"
	default:
		return "the package manager"
	}
}
