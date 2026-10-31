package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/LysCongThanh/worktree-pg/internal/engine"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// finish prints the outcome of a command and returns its exit code.
//
// --json: exactly one envelope on stdout (success or failure), nothing else on
// stdout. Otherwise: warnings and errors on stderr, human output (unless -q) on
// stdout via human.
func (a *app) finish(cmd string, result any, warns []engine.Warning, err error, human func(io.Writer)) int {
	if a.g.JSON {
		env := engine.Success(cmd, result, warns)
		if err != nil {
			env = engine.Failure(cmd, err, warns)
		}
		a.writeEnvelope(env)
		return errs.ExitCode(err)
	}
	printWarnings(a.stderr, warns)
	if err != nil {
		printError(a.stderr, err)
		return errs.ExitCode(err)
	}
	if !a.g.Quiet && human != nil {
		human(a.stdout)
	}
	return exitOK
}

// finishKeep is finish for commands whose result stays meaningful on failure
// (doctor report, hook snippet): with --json the failure envelope keeps .result;
// otherwise human output is printed before the error.
func (a *app) finishKeep(cmd string, result any, err error, human func(io.Writer)) int {
	if a.g.JSON {
		env := engine.Success(cmd, result, nil)
		if err != nil {
			env = engine.Failure(cmd, err, nil)
			env.Result = result
		}
		a.writeEnvelope(env)
		return errs.ExitCode(err)
	}
	if human != nil && (!a.g.Quiet || err != nil) {
		human(a.stdout)
	}
	if err != nil {
		printError(a.stderr, err)
	}
	return errs.ExitCode(err)
}

// writeEnvelope encodes one JSON document (indented, HTML not escaped) on stdout.
func (a *app) writeEnvelope(env engine.Envelope) {
	enc := json.NewEncoder(a.stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(env); err != nil {
		fmt.Fprintln(a.stderr, "worktree-pg: encode json:", err)
	}
}

// printWarnings writes "warning: CODE: message" lines.
func printWarnings(w io.Writer, warns []engine.Warning) {
	for _, x := range warns {
		fmt.Fprintf(w, "worktree-pg: warning: %s: %s\n", x.Code, x.Message)
	}
}

// printError writes the error with its stable code, hint and log path.
func printError(w io.Writer, err error) {
	e := errs.As(err)
	fmt.Fprintf(w, "worktree-pg: error [%s]: %s\n", e.Code, e.Error())
	if e.Hint != "" {
		fmt.Fprintf(w, "  hint: %s\n", e.Hint)
	}
	if e.LogPath != "" {
		fmt.Fprintf(w, "  log:  %s\n", e.LogPath)
	}
}
