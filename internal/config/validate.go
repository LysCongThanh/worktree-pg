package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/ports"
)

var (
	envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	roleRe    = regexp.MustCompile(`^[a-z_][a-z0-9_$]{0,62}$`)
)

// TemplateFuncs are placeholders for the funcs available in [env.vars]; the engine
// supplies real implementations at render time. Used here to syntax-check templates.
var TemplateFuncs = template.FuncMap{
	"Port": func(string) int { return 0 },
	"DSN":  func(string) string { return "" },
}

// Validate checks the whole config and returns one CONFIG_INVALID error listing every
// problem. The ephemeral range comes from the running kernel.
func (c *Config) Validate() error { return c.ValidateWith(ports.Ephemeral()) }

// ValidateWith is Validate with an explicit ephemeral range (deterministic tests).
func (c *Config) ValidateWith(eph ports.Range) error {
	var p []string
	bad := func(f string, a ...any) { p = append(p, fmt.Sprintf(f, a...)) }
	if c.Version != 1 {
		bad("version must be 1 (got %d)", c.Version)
	}
	if err := names.ValidApp(c.Name); err != nil {
		bad("%v", err)
	}
	switch c.Cluster.Source {
	case "external":
		if c.Cluster.AdminDSNEnv == "" && c.Cluster.AdminDSNFile == "" {
			bad("cluster: external source needs admin_dsn_env or admin_dsn_file")
		}
	case "docker":
		if c.Cluster.Image == "" {
			bad("cluster: docker source needs image")
		}
	default:
		bad("cluster.source must be external or docker (got %q)", c.Cluster.Source)
	}
	if c.Cluster.MaintenanceDB == "" {
		bad("cluster.maintenance_db must not be empty")
	}
	oneOf(bad, "database.roles", c.Database.Roles, "per_worktree", "shared")
	oneOf(bad, "database.strategy", c.Database.Strategy, "", "wal_log", "file_copy")
	if c.Database.Host == "" {
		bad("database.host must not be empty")
	}
	if strings.TrimSpace(c.Template.MigrateCmd) == "" {
		bad("template.migrate_cmd is required")
	}
	if c.Template.Keep < 1 {
		bad("template.keep must be >= 1")
	}
	if c.Template.Timeout.Duration <= 0 {
		bad("template.timeout must be > 0")
	}
	for _, e := range c.Template.Extensions {
		if !roleRe.MatchString(strings.ReplaceAll(e, "-", "_")) {
			bad("template.extensions: invalid name %q", e)
		}
	}
	for _, in := range append(append([]string{}, c.Template.Inputs...), c.Template.ClusterSetupInputs...) {
		checkRel(bad, "template input", in)
	}
	c.validateDSN(bad)
	c.validatePorts(bad, eph)
	c.validateEnv(bad)
	oneOf(bad, "hooks.claude.on_up_failure", c.Hooks.Claude.OnUpFailure, "fail", "keep")
	if c.Hooks.Claude.Base == "" || c.Hooks.Claude.WorktreeDir == "" || c.Hooks.Claude.Branch == "" {
		bad("hooks.claude: worktree_dir, branch and base must not be empty")
	}
	if len(p) > 0 {
		return errs.New(errs.Config, errs.CodeConfigInvalid, "%s: %s", c.displayPath(), strings.Join(p, "; "))
	}
	return nil
}

func (c *Config) validateDSN(bad func(string, ...any)) {
	seen := map[string]bool{}
	for i, d := range c.DSN {
		if !envNameRe.MatchString(d.Var) || seen[d.Var] {
			bad("dsn[%d].var %q must be a unique env var name", i, d.Var)
		}
		seen[d.Var] = true
		if d.Role != "@owner" && !roleRe.MatchString(d.Role) {
			bad("dsn[%d].role must be \"@owner\" or a role name (got %q)", i, d.Role)
		}
		if d.Role != "@owner" && d.PasswordEnv != "" && !envNameRe.MatchString(d.PasswordEnv) {
			bad("dsn[%d].password_env %q is not an env var name", i, d.PasswordEnv)
		}
		oneOf(bad, fmt.Sprintf("dsn[%d].format", i), d.Format, "url", "kv")
	}
}

func (c *Config) validatePorts(bad func(string, ...any), eph ports.Range) {
	check := func(field string, list []string) {
		rs, err := ports.ParseRanges(list)
		if err != nil {
			bad("%s: %v", field, err)
		}
		for _, r := range rs {
			if r.Overlaps(eph) {
				bad("%s: range %s overlaps the ephemeral range %s", field, r, eph)
			}
		}
	}
	check("ports.ranges", c.Ports.Ranges)
	if len(c.Ports.Vars) > 0 && len(c.Ports.Ranges) == 0 {
		bad("ports.ranges must not be empty when ports.vars is set")
	}
	for v, r := range c.Ports.Vars {
		if !envNameRe.MatchString(v) {
			bad("ports.vars: %q is not an env var name", v)
		}
		if r != "" {
			check("ports.vars."+v, []string{r})
		}
	}
	for _, r := range c.Ports.Reserved {
		if r < 1 || r > 65535 {
			bad("ports.reserved: %d out of range", r)
		}
	}
}

func (c *Config) validateEnv(bad func(string, ...any)) {
	oneOf(bad, "env.on_duplicate", c.Env.OnDuplicate, "warn", "error")
	for _, f := range c.Env.Files {
		checkRel(bad, "env.files", f)
	}
	for k, v := range c.Env.Vars {
		if !envNameRe.MatchString(k) {
			bad("env.vars: %q is not an env var name", k)
		}
		if _, err := template.New(k).Funcs(TemplateFuncs).Option("missingkey=error").Parse(v); err != nil {
			bad("env.vars.%s: %v", k, err)
		}
	}
}

// checkRel requires a clean relative path that stays inside the root.
func checkRel(bad func(string, ...any), field, p string) {
	if p == "" || filepath.IsAbs(p) || p == ".." || strings.HasPrefix(filepath.Clean(p), ".."+string(filepath.Separator)) {
		bad("%s: %q must be a relative path inside the repository", field, p)
	}
}

func oneOf(bad func(string, ...any), field, v string, allowed ...string) {
	for _, a := range allowed {
		if v == a {
			return
		}
	}
	bad("%s must be one of %q (got %q)", field, allowed, v)
}

func (c *Config) displayPath() string {
	if c.Path == "" {
		return FileName
	}
	return c.Path
}

// readSecretFile reads a DSN file and refuses group/other-accessible permissions.
func readSecretFile(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", errs.Wrap(errs.Prereq, errs.CodeAdminDSNMissing, err, "admin_dsn_file")
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", errs.New(errs.Prereq, errs.CodeAdminDSNMissing, "admin_dsn_file %s must have mode 0600 (has %o)", path, st.Mode().Perm()).
			WithHint("chmod 600 %s", path)
	}
	b, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return "", errs.New(errs.Prereq, errs.CodeAdminDSNMissing, "admin_dsn_file %s is empty or unreadable", path)
	}
	return strings.TrimSpace(string(b)), nil
}

func errAdminMissing(env string) error {
	return errs.New(errs.Prereq, errs.CodeAdminDSNMissing, "admin DSN not set").
		WithHint("export %s=postgres://admin@127.0.0.1:5432/postgres (never pass it as a flag)", env)
}
