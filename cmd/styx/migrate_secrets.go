package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ishaanbatra/styx/internal/config"
)

var secretShapedRE = regexp.MustCompile(`^export\s+([A-Z][A-Z0-9_]*(?:_API_KEY|_TOKEN|_SECRET))="?([^"]+)"?\s*$`)

func cmdMigrateSecrets() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	files := []string{".zshrc", ".bashrc", ".bash_profile", ".zprofile"}
	moved := 0
	for _, f := range files {
		path := filepath.Join(home, f)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		n, err := migrateOne(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", path, err)
			continue
		}
		moved += n
	}
	fmt.Printf("Migrated %d secret(s) to the macOS Keychain (service=styx).\n", moved)
	if moved > 0 {
		fmt.Println("Note: the old values may survive in shell history and Time Machine; consider rotating the migrated keys.")
	}
	return nil
}

func migrateOne(path string) (int, error) {
	in, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	var toRemove []string
	moved := 0
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		m := secretShapedRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := strings.ToLower(m[1])
		value := m[2]
		fmt.Printf("%s -> %s — move to Keychain? [Y/n] ", path, name)
		reader := bufio.NewReader(os.Stdin)
		ans, _ := reader.ReadString('\n')
		ans = strings.TrimSpace(strings.ToLower(ans))
		if ans == "" || ans == "y" || ans == "yes" {
			if err := config.SetSecret(name, value); err != nil {
				return moved, err
			}
			toRemove = append(toRemove, line)
			moved++
		}
	}
	if err := scanner.Err(); err != nil {
		return moved, err
	}
	if moved == 0 {
		return 0, nil
	}
	return moved, rewriteRC(path, toRemove)
}

// rewriteRC removes the given exact lines from the rc file, writing a
// one-time 0600 backup first and tightening the rc to 0600. The secret must
// not survive in the live file — a commented copy is still a plaintext leak.
func rewriteRC(path string, remove []string) error {
	orig, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	bak := path + ".styx-bak"
	if _, err := os.Stat(bak); os.IsNotExist(err) {
		if err := os.WriteFile(bak, orig, 0o600); err != nil {
			return fmt.Errorf("write backup: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("stat backup %s: %w", bak, err)
	}
	// drop is a multiset: remove only as many occurrences of each line as the
	// user confirmed, so a declined duplicate of the same line survives.
	drop := make(map[string]int, len(remove))
	for _, l := range remove {
		drop[strings.TrimSpace(l)]++
	}
	var out []string
	for _, line := range strings.Split(string(orig), "\n") {
		if key := strings.TrimSpace(line); drop[key] > 0 {
			drop[key]--
			continue
		}
		out = append(out, line)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(out, "\n")), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return os.Rename(tmp, path)
}
