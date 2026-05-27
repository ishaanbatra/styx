package main

import (
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/brief"
	"github.com/ishaanbatra/styx/internal/project"
)

func cmdDeepResearch(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: styx deep-research <query>")
	}
	query := strings.Join(args, " ")
	proj, err := project.Current()
	if err != nil {
		return err
	}
	encoded := url.QueryEscape(query)
	_ = exec.Command("open", "https://gemini.google.com/app?q="+encoded).Run()
	_ = exec.Command("open", "https://chat.openai.com/?q="+encoded).Run()

	subDir := proj.ResearchDir
	if subDir == "" {
		subDir = "styx/research"
	}
	body := fmt.Sprintf(`# Deep Research Brief

**Query**: %s
**Date**:  %s
**Mode**:  human-in-the-loop (Gemini Deep Research + ChatGPT)

---

## Gemini Deep Research Findings

<!-- Paste Gemini's output here. Trim to what's actually useful. -->

---

## ChatGPT Second Opinion

<!-- Paste ChatGPT's response. Note where it disagrees with Gemini. -->

---

## Your Synthesis & Decision

<!-- What's the path forward? Where did the two sources agree? Where did they diverge? Which side did you pick, and why? -->
`, query, time.Now().Format("2006-01-02 15:04:05"))

	out, err := brief.WriteBrief(brief.WriteOpts{
		ProjectPath: proj.Path,
		SubDir:      subDir,
		Query:       query + " deep",
		Body:        body,
		Now:         time.Now(),
	})
	if err != nil {
		return err
	}
	rel, _ := filepath.Rel(proj.Path, out)
	fmt.Printf("✓ Template: %s\n", rel)
	fmt.Println("✓ Opened Gemini and ChatGPT in browser")
	fmt.Println()
	fmt.Println(`Fill in the brief, then: styx plan "<description>"`)
	return nil
}
