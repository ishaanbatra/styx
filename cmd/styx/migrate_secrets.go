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
		fmt.Println("Open a new shell so the commented-out exports take effect.")
	}
	return nil
}

func migrateOne(path string) (int, error) {
	in, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	var out strings.Builder
	moved := 0
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		m := secretShapedRE.FindStringSubmatch(line)
		if m == nil {
			out.WriteString(line)
			out.WriteByte('\n')
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
			out.WriteString("# moved to Keychain by styx migrate-secrets\n# " + line + "\n")
			moved++
		} else {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return moved, err
	}
	if moved == 0 {
		return 0, nil
	}
	return moved, os.WriteFile(path, []byte(out.String()), 0o644)
}
