package signals

import (
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/ishaanbatra/styx/internal/config"
)

func TestExtract(t *testing.T) {
	cases := []struct {
		name string
		verb string
		args []string
		proj config.Project
		want []string
	}{
		{
			name: "grunt-trivial",
			verb: "grunt",
			args: []string{"format this json"},
			proj: config.Project{Language: "python"},
			want: []string{"lang:python", "trivial"},
		},
		{
			name: "grunt-not-trivial",
			verb: "grunt",
			args: []string{strings.Repeat("a", 200)},
			proj: config.Project{Language: "go"},
			want: []string{"lang:go"},
		},
		{
			name: "plan-complex-keyword",
			verb: "plan",
			args: []string{"refactor the auth middleware"},
			proj: config.Project{Language: "python"},
			want: []string{"complex", "lang:python"},
		},
		{
			name: "build-interactive",
			verb: "build",
			args: nil,
			proj: config.Project{Language: "typescript"},
			want: []string{"interactive", "lang:typescript"},
		},
		{
			name: "think-deep",
			verb: "think",
			args: []string{"deep: should we adopt event sourcing"},
			proj: config.Project{Language: "go"},
			want: []string{"deep", "lang:go"},
		},
		{
			name: "debug-panic",
			verb: "debug",
			args: []string{"panic with a nil pointer in the failing test"},
			proj: config.Project{Language: "go"},
			want: []string{"debug", "lang:go"},
		},
		{
			name: "debug-benign",
			verb: "debug",
			args: []string{"unexpected behavior in the cache"},
			proj: config.Project{Language: "go"},
			want: []string{"lang:go"},
		},
		{
			name: "complex-pr-drafting-raises-floor",
			verb: "pr.body",
			args: []string{"refactor the auth middleware"},
			proj: config.Project{Language: "go"},
			want: []string{"clerical", "complex", "lang:go"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Extract(c.verb, c.args, c.proj)
			sort.Strings(got)
			sort.Strings(c.want)
			if diff := cmp.Diff(c.want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
