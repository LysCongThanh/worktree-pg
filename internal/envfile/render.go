package envfile

import (
	"regexp"
	"strings"
)

// bareRe matches values that need no quoting in dotenv files or POSIX shells.
var bareRe = regexp.MustCompile(`^[A-Za-z0-9_./:@%+,=-]*$`)

// RenderDotenv renders KEY=value lines in input order.
func RenderDotenv(vars []Var) []string {
	out := make([]string, len(vars))
	for i, v := range vars {
		out[i] = v.Key + "=" + QuoteDotenv(v.Value)
	}
	return out
}

// RenderShell renders `export KEY='value'` lines for eval in POSIX shells.
func RenderShell(vars []Var) []string {
	out := make([]string, len(vars))
	for i, v := range vars {
		out[i] = "export " + v.Key + "=" + QuoteShell(v.Value)
	}
	return out
}

// QuoteDotenv quotes a value so docker compose, godotenv, dotenv (node) and
// python-dotenv read it back unchanged:
//   - safe charset: bare
//   - no single quote and no newline: single quotes (literal, no interpolation)
//   - otherwise: double quotes escaping \ " $ ` and newlines as \n
func QuoteDotenv(v string) string {
	if bareRe.MatchString(v) {
		return v
	}
	if !strings.ContainsAny(v, "'\n\r") {
		return "'" + v + "'"
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`", "\n", `\n`, "\r", `\r`)
	return `"` + r.Replace(v) + `"`
}

// QuoteShell single-quotes a value for POSIX sh; an embedded quote becomes quote-backslash-quote-quote.
func QuoteShell(v string) string {
	if v != "" && bareRe.MatchString(v) {
		return v
	}
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}
