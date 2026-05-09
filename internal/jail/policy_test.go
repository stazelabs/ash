package jail

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPolicy_Disabled(t *testing.T) {
	p := &Policy{Enabled: false}
	if err := p.Check("/etc/hosts"); err != nil {
		t.Errorf("disabled policy must allow everything: %v", err)
	}
}

func TestPolicy_Nil(t *testing.T) {
	var p *Policy
	if err := p.Check("/etc/hosts"); err != nil {
		t.Errorf("nil policy must allow everything: %v", err)
	}
}

func TestPolicy_InRoot(t *testing.T) {
	root := t.TempDir()
	p := FromConfig(true, root, nil, nil)
	mustExist(t, filepath.Join(root, "child.txt"))
	if err := p.Check(filepath.Join(root, "child.txt")); err != nil {
		t.Errorf("in-root path should be allowed: %v", err)
	}
}

func TestPolicy_OutsideRoot(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	p := FromConfig(true, root, nil, nil)
	if err := p.Check(filepath.Join(other, "x.txt")); err == nil {
		t.Errorf("outside path should be denied")
	}
}

func TestPolicy_AllowPaths(t *testing.T) {
	root := t.TempDir()
	allow := t.TempDir()
	p := FromConfig(true, root, []string{allow}, nil)
	if err := p.Check(filepath.Join(allow, "x.txt")); err != nil {
		t.Errorf("allow-listed path should be allowed: %v", err)
	}
}

func TestPolicy_DenyPaths(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "secrets")
	if err := os.MkdirAll(secret, 0o755); err != nil {
		t.Fatal(err)
	}
	p := FromConfig(true, root, nil, []string{secret})
	if err := p.Check(filepath.Join(secret, "key.txt")); err == nil {
		t.Errorf("deny-listed path should be denied even inside root")
	}
	// Sibling path under root still allowed.
	if err := p.Check(filepath.Join(root, "ok.txt")); err != nil {
		t.Errorf("non-deny in-root path should be allowed: %v", err)
	}
}

func TestPolicy_SymlinkEscape(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir() // outside root
	link := filepath.Join(root, "escape")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	p := FromConfig(true, root, nil, nil)
	if err := p.Check(link); err == nil {
		t.Errorf("symlink escape should be denied")
	}
}

func TestPolicy_NewFileUnderRoot(t *testing.T) {
	// `ash write --path new.go` creates a file that doesn't yet exist.
	// Canonicalization must still resolve the parent and accept it.
	root := t.TempDir()
	subdir := filepath.Join(root, "pkg")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := FromConfig(true, root, nil, nil)
	if err := p.Check(filepath.Join(subdir, "new.go")); err != nil {
		t.Errorf("not-yet-created path under root must be allowed: %v", err)
	}
}

func TestPolicy_NewFileViaEscape(t *testing.T) {
	// Adversarial: new file under a symlink that escapes the root.
	// EvalSymlinks-of-prefix must catch this.
	root := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(root, "dropbox")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	p := FromConfig(true, root, nil, nil)
	if err := p.Check(filepath.Join(link, "new.go")); err == nil {
		t.Errorf("new file under escape symlink should be denied")
	}
}

func TestCheckPaths_NoActivePolicy(t *testing.T) {
	SetPolicy(nil)
	if perr := CheckPaths(map[string]string{"path": "/etc/hosts"}); perr != nil {
		t.Errorf("no-policy CheckPaths must allow: %v", perr)
	}
}

func TestCheckPaths_ActivePolicy(t *testing.T) {
	root := t.TempDir()
	SetPolicy(FromConfig(true, root, nil, nil))
	defer SetPolicy(nil)
	perr := CheckPaths(map[string]string{
		"path":  filepath.Join(root, "ok.txt"),
		"other": "/etc/hosts",
	})
	if perr == nil {
		t.Fatal("expected denial for /etc/hosts")
	}
	if perr.Code != "path_denied" {
		t.Errorf("error code: want path_denied, got %q", perr.Code)
	}
	// The denied key should be named in the message so the agent knows
	// which arg failed.
	if !contains(perr.Msg, "other=") {
		t.Errorf("message should name the denied key: %q", perr.Msg)
	}
}

func TestCheckPaths_EmptyValuesSkipped(t *testing.T) {
	root := t.TempDir()
	SetPolicy(FromConfig(true, root, nil, nil))
	defer SetPolicy(nil)
	if perr := CheckPaths(map[string]string{"other": ""}); perr != nil {
		t.Errorf("empty values should be skipped: %v", perr)
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}
