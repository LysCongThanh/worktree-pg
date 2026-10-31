package secret

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

func TestLoadOrCreateStableAndPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	var wg sync.WaitGroup
	got := make([]string, 8)
	for i := range got { // concurrent first use must converge on one key
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := LoadOrCreate(dir)
			if err != nil {
				t.Error(err)
				return
			}
			got[i] = s.Password("role/c1/shop_wt_a")
		}(i)
	}
	wg.Wait()
	for _, g := range got[1:] {
		if g != got[0] {
			t.Fatal("concurrent LoadOrCreate produced different keys")
		}
	}
	st, _ := os.Stat(filepath.Join(dir, FileName))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("key perm %o", st.Mode().Perm())
	}
	s, _ := LoadOrCreate(dir)
	p := s.Password("role/c1/shop_wt_a")
	if p != got[0] || len(p) != 32 || p == s.Password("role/c1/shop_wt_b") {
		t.Fatalf("password not stable/unique: %q", p)
	}
}

func TestUnsafeOrMalformedKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	_ = os.WriteFile(path, []byte("zz"), 0o600)
	if _, err := LoadOrCreate(dir); errs.CodeOf(err) != errs.CodeSecretKeyUnsafe {
		t.Fatalf("malformed key accepted: %v", err)
	}
	_ = os.Chmod(path, 0o644)
	if _, err := LoadOrCreate(dir); errs.CodeOf(err) != errs.CodeSecretKeyUnsafe || errs.ExitCode(err) != 4 {
		t.Fatalf("0644 key accepted: %v", err)
	}
}
