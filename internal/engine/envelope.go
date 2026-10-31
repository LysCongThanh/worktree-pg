package engine

import "github.com/LysCongThanh/worktree-pg/internal/errs"

// Envelope is the single JSON document printed on stdout by `--json` commands.
type Envelope struct {
	Schema   string    `json:"schema"`
	OK       bool      `json:"ok"`
	Command  string    `json:"command"`
	Result   any       `json:"result,omitempty"`
	Warnings []Warning `json:"warnings"`
	Error    *ErrorDoc `json:"error,omitempty"`
}

// ErrorDoc is the "error" member of a failed envelope.
type ErrorDoc struct {
	Code    string `json:"code"`
	Exit    int    `json:"exit"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Log     string `json:"log,omitempty"`
}

// Success wraps a result.
func Success(command string, result any, warnings []Warning) Envelope {
	if warnings == nil {
		warnings = []Warning{}
	}
	return Envelope{Schema: Schema, OK: true, Command: command, Result: result, Warnings: warnings}
}

// Failure wraps an error using its stable code and exit status.
func Failure(command string, err error, warnings []Warning) Envelope {
	e := errs.As(err)
	if warnings == nil {
		warnings = []Warning{}
	}
	return Envelope{Schema: Schema, OK: false, Command: command, Warnings: warnings, Error: &ErrorDoc{
		Code: e.Code, Exit: e.Kind.Exit(), Message: e.Error(), Hint: e.Hint, Log: e.LogPath,
	}}
}
