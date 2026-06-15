package main

func cmdDeepResearch(args []string) error {
	logStatus("'deep-research' is now 'research --deep' in v0.2 — forwarding")
	a, err := loadApp()
	if err != nil {
		return err
	}
	defer a.tracker.Close()
	forwarded := append([]string{"--deep"}, args...)
	return cmdResearch(a, forwarded)
}
