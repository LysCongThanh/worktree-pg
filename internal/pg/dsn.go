package pg

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/envfile"
)

// OwnerRef is the [[dsn]].role value meaning "the worktree's own role".
const OwnerRef = "@owner"

// URL renders a postgres:// connection URL. Params is appended as the raw query.
func URL(c Cred) string {
	u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), Path: "/" + c.Database, RawQuery: c.Params}
	if c.Password != "" {
		u.User = url.UserPassword(c.User, c.Password)
	} else if c.User != "" {
		u.User = url.User(c.User)
	}
	return u.String()
}

// KV renders a libpq keyword/value connection string; Params ("a=b&c=d") become
// extra keywords. Values are single-quoted when needed.
func KV(c Cred) string {
	parts := []string{"host=" + kvQuote(c.Host), "port=" + strconv.Itoa(c.Port)}
	if c.User != "" {
		parts = append(parts, "user="+kvQuote(c.User))
	}
	if c.Password != "" {
		parts = append(parts, "password="+kvQuote(c.Password))
	}
	parts = append(parts, "dbname="+kvQuote(c.Database))
	if q, err := url.ParseQuery(c.Params); err == nil {
		for _, kv := range strings.Split(c.Params, "&") {
			k, _, _ := strings.Cut(kv, "=")
			if k != "" && q.Has(k) {
				parts = append(parts, k+"="+kvQuote(q.Get(k)))
				q.Del(k)
			}
		}
	}
	return strings.Join(parts, " ")
}

// kvQuote quotes a libpq keyword value when it is empty or has spaces/quotes/backslashes.
func kvQuote(v string) string {
	if v != "" && !strings.ContainsAny(v, " '\\\t\n") {
		return v
	}
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

// PGEnv returns the libpq environment for c, used by user commands (psql, goose
// via DSN vars, ...). Secrets travel only through the environment (invariant S6).
func PGEnv(c Cred) []string {
	env := []string{"PGHOST=" + c.Host, "PGPORT=" + strconv.Itoa(c.Port), "PGUSER=" + c.User, "PGDATABASE=" + c.Database}
	if c.Password != "" {
		env = append(env, "PGPASSWORD="+c.Password)
	}
	if q, err := url.ParseQuery(c.Params); err == nil && q.Get("sslmode") != "" {
		env = append(env, "PGSSLMODE="+q.Get("sslmode"))
	}
	return env
}

// DSNVars renders every [[dsn]] entry against database base.Database on base's
// port. "@owner" entries use base.User/Password; cluster-role entries use their
// own name and the password read from password_env (none when unset). Host is
// the entry's host override or base.Host.
func DSNVars(list []config.DSN, base Cred, getenv func(string) string) []envfile.Var {
	out := make([]envfile.Var, 0, len(list))
	for _, d := range list {
		c := base
		c.Params = d.Params
		if d.Host != "" {
			c.Host = d.Host
		}
		if d.Role != OwnerRef {
			c.User, c.Password = d.Role, ""
			if d.PasswordEnv != "" {
				c.Password = getenv(d.PasswordEnv)
			}
		}
		v := URL(c)
		if d.Format == "kv" {
			v = KV(c)
		}
		out = append(out, envfile.Var{Key: d.Var, Value: v, Secret: c.Password != ""})
	}
	return out
}
