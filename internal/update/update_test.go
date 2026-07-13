package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want int
	}{
		{name: "newer", a: "v1.2.4", b: "v1.2.3", want: 1},
		{name: "older", a: "1.2.2", b: "v1.2.3", want: -1},
		{name: "equal normalized prefix", a: "v1.2.3", b: "1.2.3", want: 0},
		{name: "semantic not lexical", a: "v1.10.0", b: "v1.9.9", want: 1},
		{name: "prerelease older", a: "v2.0.0-rc.1", b: "2.0.0", want: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := compareVersions(tt.a, tt.b)
			if err != nil {
				t.Fatalf("compareVersions(%q, %q): %v", tt.a, tt.b, err)
			}
			if got != tt.want {
				t.Fatalf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestCheckFetchesLatestJSON(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv(cacheDirEnv, cacheDir)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"1.7.0"}`))
	}))
	defer server.Close()

	if err := check(context.Background(), server.Client(), server.URL, time.Now()); err != nil {
		t.Fatalf("check: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
	data, err := os.ReadFile(filepath.Join(cacheDir, "latest.json"))
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	var latest latestRelease
	if err := json.Unmarshal(data, &latest); err != nil {
		t.Fatalf("decode cache: %v", err)
	}
	if latest.Version != "v1.7.0" {
		t.Fatalf("cached version = %q, want v1.7.0", latest.Version)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "latest.lock")); err != nil {
		t.Fatalf("stat separate lock file: %v", err)
	}
}

func TestCacheDirOverride(t *testing.T) {
	want := t.TempDir()
	t.Setenv(cacheDirEnv, want)
	dir, latestPath, lockPath, err := cachePaths()
	if err != nil {
		t.Fatalf("cachePaths: %v", err)
	}
	if dir != want {
		t.Fatalf("cache dir = %q, want %q", dir, want)
	}
	if latestPath != filepath.Join(want, "latest.json") {
		t.Fatalf("latest path = %q", latestPath)
	}
	if lockPath != filepath.Join(want, "latest.lock") {
		t.Fatalf("lock path = %q", lockPath)
	}
}

func TestCheckLockContentionExitsQuietly(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv(cacheDirEnv, cacheDir)
	lock := flock.New(filepath.Join(cacheDir, "latest.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		t.Fatalf("lock cache: %v", err)
	}
	if !locked {
		t.Fatal("lock cache: lock not acquired")
	}
	defer func() {
		if err := lock.Unlock(); err != nil {
			t.Errorf("unlock cache: %v", err)
		}
	}()

	var requests atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("request should not run")
	})}

	if err := check(context.Background(), client, "https://example.invalid/latest.json", time.Now()); err != nil {
		t.Fatalf("check with contended lock: %v", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("requests = %d, want 0", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestInstallManager(t *testing.T) {
	tests := []struct {
		name         string
		goos         string
		executable   string
		scoopRoot    string
		localAppData string
		want         string
	}{
		{name: "non windows", goos: "darwin", executable: "/Users/me/scoop/apps/styx/current/styx", want: ""},
		{name: "scoop apps", goos: "windows", executable: `C:\Users\me\scoop\apps\styx\current\styx.exe`, want: "Scoop"},
		{name: "scoop shims", goos: "windows", executable: `C:\Users\me\scoop\shims\styx.exe`, want: "Scoop"},
		{name: "custom scoop root", goos: "windows", executable: `D:\tools\scoop\apps\styx\current\styx.exe`, scoopRoot: `D:\tools\scoop`, want: "Scoop"},
		{name: "winget packages", goos: "windows", executable: `C:\Users\me\AppData\Local\Microsoft\WinGet\Packages\styx\styx.exe`, localAppData: `C:\Users\me\AppData\Local`, want: "WinGet"},
		{name: "direct install", goos: "windows", executable: `C:\Users\me\bin\styx.exe`, localAppData: `C:\Users\me\AppData\Local`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := installManager(tt.goos, tt.executable, tt.scoopRoot, tt.localAppData); got != tt.want {
				t.Fatalf("installManager() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDevelopmentVersion(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{version: "0.4.0-dev", want: true},
		{version: "dev-a1b2c3d", want: true},
		{version: "v0.2.2-0.20260713192140-a86a5c708b43+dirty", want: true},
		{version: "v0.2.2-0.20260713192140-a86a5c708b43", want: true},
		{version: "0.2.2-0.20260713192140-a86a5c708b43", want: true},
		{version: "v0.4.0", want: false},
		{version: "0.4.1", want: false},
		{version: "v0.4.1-rc.1", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			if got := isDevelopmentVersion(tt.version); got != tt.want {
				t.Fatalf("isDevelopmentVersion(%q) = %t, want %t", tt.version, got, tt.want)
			}
		})
	}
}

func TestMaybeNotify(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv(cacheDirEnv, cacheDir)
	t.Setenv("STYX_NO_UPDATE_CHECK", "")
	t.Setenv("DO_NOT_TRACK", "")
	if err := os.WriteFile(filepath.Join(cacheDir, "latest.json"), []byte("{\"version\":\"v1.7.0\"}\n"), 0o644); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	originalStreamsTTY := streamsTTY
	streamsTTY = func() bool { return true }
	t.Cleanup(func() {
		streamsTTY = originalStreamsTTY
		ConfigureLaunch("", false)
	})

	ConfigureLaunch("v1.6.0", false)
	var output bytes.Buffer
	if err := MaybeNotify(&output); err != nil {
		t.Fatalf("MaybeNotify: %v", err)
	}
	want := "[styx] a new, littler, trickier styx is available: v1.7.0 (run 'styx update')\n"
	if got := output.String(); got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
}

func TestMaybeNotifyGates(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv(cacheDirEnv, cacheDir)
	if err := os.WriteFile(filepath.Join(cacheDir, "latest.json"), []byte("{\"version\":\"v1.7.0\"}\n"), 0o644); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	originalStreamsTTY := streamsTTY
	t.Cleanup(func() {
		streamsTTY = originalStreamsTTY
		ConfigureLaunch("", false)
	})

	tests := []struct {
		name          string
		version       string
		quiet         bool
		streamsAreTTY bool
		noCheck       string
		doNotTrack    string
	}{
		{name: "development build", version: "0.4.0-dev", streamsAreTTY: true},
		{name: "revision development build", version: "dev-abc1234", streamsAreTTY: true},
		{name: "quiet", version: "v1.6.0", quiet: true, streamsAreTTY: true},
		{name: "non interactive", version: "v1.6.0"},
		{name: "update check disabled", version: "v1.6.0", streamsAreTTY: true, noCheck: "1"},
		{name: "do not track", version: "v1.6.0", streamsAreTTY: true, doNotTrack: "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("STYX_NO_UPDATE_CHECK", tt.noCheck)
			t.Setenv("DO_NOT_TRACK", tt.doNotTrack)
			streamsTTY = func() bool { return tt.streamsAreTTY }
			ConfigureLaunch(tt.version, tt.quiet)

			var output bytes.Buffer
			if err := MaybeNotify(&output); err != nil {
				t.Fatalf("MaybeNotify: %v", err)
			}
			if got := output.String(); got != "" {
				t.Fatalf("notice = %q, want no output", got)
			}
		})
	}
}

func TestSelfUpdateRejectsDevelopmentBuild(t *testing.T) {
	_, err := SelfUpdate(context.Background(), "0.4.0-dev")
	if err == nil || !strings.Contains(err.Error(), "development builds cannot self-update") {
		t.Fatalf("SelfUpdate development error = %v", err)
	}
}
