package git

import "testing"

// TestSetBackend_Mapping pins the config-string → backendKind mapping,
// including the empty-string default that a missing [git].backend
// resolves to (ASH-203).
func TestSetBackend_Mapping(t *testing.T) {
	prev := currentBackend()
	t.Cleanup(func() { active.Store(int32(prev)) })

	cases := []struct {
		name string
		want backendKind
	}{
		{"", backendDefault},
		{"go-git", backendGogit},
		{"gogit", backendGogit},
		{"shellout", backendShellout},
	}
	for _, tc := range cases {
		if err := SetBackend(tc.name); err != nil {
			t.Fatalf("SetBackend(%q): %v", tc.name, err)
		}
		if got := currentBackend(); got != tc.want {
			t.Errorf("SetBackend(%q) → %d, want %d", tc.name, got, tc.want)
		}
	}
	if err := SetBackend("bogus"); err == nil {
		t.Error("SetBackend(\"bogus\") should return an error")
	}
}

// TestStatusDiffShellout_Routing pins the ASH-203 routing matrix: which
// backend status and diff resolve to for each config × git-on-PATH
// combination. Explicit go-git is honored even when git is available;
// the default config delegates to git only when it is on PATH.
func TestStatusDiffShellout_Routing(t *testing.T) {
	prev := currentBackend()
	prevGit := gitOnPath
	t.Cleanup(func() {
		active.Store(int32(prev))
		gitOnPath = prevGit
	})

	cases := []struct {
		name    string
		backend backendKind
		gitPath bool
		want    bool // true → shellout, false → go-git
	}{
		{"default config, git available", backendDefault, true, true},
		{"default config, no git (fallback)", backendDefault, false, false},
		{"explicit go-git, git available", backendGogit, true, false},
		{"explicit go-git, no git", backendGogit, false, false},
		{"explicit shellout, git available", backendShellout, true, true},
		{"explicit shellout, no git", backendShellout, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			active.Store(int32(tc.backend))
			gitOnPath = tc.gitPath
			if got := statusDiffShellout(); got != tc.want {
				t.Errorf("statusDiffShellout() = %v, want %v", got, tc.want)
			}
		})
	}
}
