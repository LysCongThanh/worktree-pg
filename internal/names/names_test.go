package names

import (
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"/home/u/shop":                    "shop",
		"/w/Feature--Login.Page":          "feature_login_page",
		"/w/__x__":                        "x",
		"/w/世界":                           "wt",
		"/w/agent-a974ca1fb8dda897b-long": "agent_a974ca1fb8dda8",
		"/w/abcdefghijklmnopqrs_tuv":      "abcdefghijklmnopqrs",
		"/":                               "wt",
		"/w/trailing/":                    "trailing",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKeyAndNames(t *testing.T) {
	k := Key("/home/u/shop/.claude/worktrees/a")
	if len(k) != 16 || k != Key("/home/u/shop/.claude/worktrees/a/") {
		t.Fatalf("key %q not stable/16 chars", k)
	}
	db := Database("shop", "a", k)
	if db != "shop_wt_a_"+k[:8] || Role("shop", "a", k) != db {
		t.Fatalf("Database = %q", db)
	}
	if OwnerRole("shop") != "shop_wt_owner" {
		t.Fatal("OwnerRole")
	}
	fp := strings.Repeat("ab", 32)
	if Template("shop", fp) != "shop_tpl_abababab" || TemplateBuild("shop", fp) != "shop_tpl_abababab_build" {
		t.Fatal("template names")
	}
	if got := Container("postgres:18-alpine"); got != "wtpg-cluster-postgres-18-alpine" {
		t.Fatalf("Container = %q", got)
	}
	long := Database(strings.Repeat("a", 20), strings.Repeat("b", 20), k)
	if len(long) > MaxIdent {
		t.Fatalf("db name too long: %d", len(long))
	}
}

func TestValidApp(t *testing.T) {
	for _, ok := range []string{"shop", "a_1", strings.Repeat("x", 20)} {
		if err := ValidApp(ok); err != nil {
			t.Errorf("ValidApp(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Shop", "a-b", strings.Repeat("x", 21), "a b"} {
		if ValidApp(bad) == nil {
			t.Errorf("ValidApp(%q) should fail", bad)
		}
	}
}

func TestMarkerRoundTrip(t *testing.T) {
	m := Marker{Kind: KindClone, Key: Key("/w/a"), Path: "/w/a \"q\"", FP: "ff"}
	s := m.String()
	if !strings.HasPrefix(s, MarkerPrefix) {
		t.Fatal(s)
	}
	got, ok := ParseMarker(s)
	if !ok || got != m {
		t.Fatalf("round trip: %+v %v", got, ok)
	}
	if !Owns(s, KindClone, m.Key) || Owns(s, KindRole, m.Key) || Owns(s, KindClone, "other") {
		t.Fatal("Owns mismatch")
	}
	for _, bad := range []string{"", "hello", MarkerPrefix + "{", MarkerPrefix + "{}", "worktree-pg:v2 {\"kind\":\"clone\"}"} {
		if _, ok := ParseMarker(bad); ok {
			t.Errorf("ParseMarker(%q) accepted", bad)
		}
	}
}

func FuzzFit63(f *testing.F) {
	for _, s := range []string{"", "a", strings.Repeat("x", 63), strings.Repeat("y", 64), strings.Repeat("é", 40)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := Fit63(s)
		if len(got) > MaxIdent {
			t.Fatalf("len %d > 63 for %q", len(got), s)
		}
		if got != Fit63(s) {
			t.Fatal("not deterministic")
		}
		if len(s) <= MaxIdent && got != s {
			t.Fatalf("short input changed: %q -> %q", s, got)
		}
		if len(s) > MaxIdent && (len(got) != MaxIdent || got[:54] != s[:54]) {
			t.Fatalf("long input shape wrong: %q", got)
		}
	})
}

func FuzzSlug(f *testing.F) {
	for _, s := range []string{"/a/b", "", "..", "/x/A-B_c", "/x/" + strings.Repeat("z", 50)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		s := Slug(p)
		if s == "" || len(s) > MaxSlug || strings.HasPrefix(s, "_") || strings.HasSuffix(s, "_") || strings.Contains(s, "__") {
			t.Fatalf("bad slug %q from %q", s, p)
		}
		for _, r := range s {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_') {
				t.Fatalf("bad rune in slug %q", s)
			}
		}
	})
}
