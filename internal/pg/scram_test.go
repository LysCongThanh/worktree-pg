package pg

import (
	"strings"
	"testing"
)

// Golden vector computed independently with Python hashlib/hmac (RFC 7677 steps).
func TestScramVerifierGolden(t *testing.T) {
	got, err := ScramVerifier("shop_wt_feat_1a2b3c4d", "s3cr3t-Pass")
	if err != nil {
		t.Fatal(err)
	}
	want := "SCRAM-SHA-256$4096:B6swUfgOsseq9hJDXkHxSg==$sxU15KduNcdYCdvCicZZAe+XJzuUU4LDwMcjY4qAImU=:RMJS7xuaQ2A9kPhlW6NJ9vnzlkoWefAyBfYWSpQzRzk="
	if got != want {
		t.Fatalf("verifier\n got %s\nwant %s", got, want)
	}
	other, _ := ScramVerifier("shop_wt_other_0000", "s3cr3t-Pass")
	if other == got {
		t.Fatal("same password on two roles must not share a verifier (salt includes the role)")
	}
}

// TestInvariant_S6_RoleSQLNeverHoldsPlaintext: the CREATE/ALTER ROLE text, which the
// server logs on any error, carries only the verifier.
func TestInvariant_S6_RoleSQLNeverHoldsPlaintext(t *testing.T) {
	const pw = "plaintext-must-not-appear"
	create, err := createRoleSQL("shop_wt_x", pw, "worktree-pg:v1 {}")
	if err != nil {
		t.Fatal(err)
	}
	alter, err := setPasswordSQL("shop_wt_x", pw)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{create, alter} {
		if strings.Contains(sql, pw) || !strings.Contains(sql, "'SCRAM-SHA-256$4096:") {
			t.Fatalf("role statement leaks or lacks verifier: %s", sql)
		}
	}
}
