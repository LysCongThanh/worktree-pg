// Package registry persists the machine-wide worktree-pg state (state.json):
// clusters, templates and provisioned worktrees with their sticky ports.
//
// Every mutation runs under an exclusive flock and is written atomically
// (tmp + fsync + rename + dir fsync), invariant S10. Slow work (network, docker,
// migrations) must never run inside Update: callers record intent, release the
// lock, do the work, then commit the outcome in a second Update.
package registry

import "time"

// Version is the current state.json schema version.
const Version = 1

// Worktree statuses.
const (
	StatusProvisioning = "provisioning"
	StatusReady        = "ready"
	StatusFailed       = "failed"
	StatusTearingDown  = "tearing_down"
)

// State is the whole state.json document.
type State struct {
	Version   int                  `json:"version"`
	Clusters  map[string]*Cluster  `json:"clusters"`
	Worktrees map[string]*Worktree `json:"worktrees"`
}

// Cluster is an external server or a tool-managed docker container.
type Cluster struct {
	ID        string               `json:"id"`
	Kind      string               `json:"kind"` // external | docker
	Image     string               `json:"image,omitempty"`
	Container string               `json:"container,omitempty"`
	Host      string               `json:"host"`
	Port      int                  `json:"port"`
	PGMajor   int                  `json:"pg_major"`
	Setup     map[string]*Setup    `json:"setup,omitempty"`     // app -> cluster_setup fingerprint
	Templates map[string]*Template `json:"templates,omitempty"` // template db name -> record
}

// Setup records the last successful cluster_setup_cmd for an app.
type Setup struct {
	FP string `json:"fp"`
}

// Template records a published template database.
type Template struct {
	App        string    `json:"app"`
	FP         string    `json:"fp"`
	BuiltAt    time.Time `json:"built_at"`
	LastUsedAt time.Time `json:"last_used_at"`
}

// Worktree is one provisioned (or provisioning) worktree.
type Worktree struct {
	Key           string         `json:"key"`
	Path          string         `json:"path"`
	RepoCommonDir string         `json:"repo_common_dir"`
	App           string         `json:"app"`
	Branch        string         `json:"branch"`
	Cluster       string         `json:"cluster"`
	DB            string         `json:"db"`
	Role          string         `json:"role"`
	Ports         map[string]int `json:"ports"`
	EnvFiles      []EnvFile      `json:"env_files"`
	Template      string         `json:"template"`
	TemplateFP    string         `json:"template_fp"`
	Origin        string         `json:"origin"`         // cli | claude-hook | git-hook
	CreatedBranch string         `json:"created_branch"` // branch created by the claude hook, deleted on remove
	Status        string         `json:"status"`
	Intent        *Intent        `json:"intent,omitempty"`
	Lease         *Lease         `json:"lease,omitempty"`
	LastError     string         `json:"last_error,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// EnvFile records a managed env file and whether worktree-pg created it.
type EnvFile struct {
	Path        string `json:"path"`
	CreatedFile bool   `json:"created_file"`
}

// Intent records objects about to be created, written before CREATE so a crash
// can be rolled back (invariant S4).
type Intent struct {
	DB   string `json:"db,omitempty"`
	Role string `json:"role,omitempty"`
}

// Lease marks a worktree as being operated on by one process.
type Lease struct {
	PID      int       `json:"pid"`
	PIDStart string    `json:"pid_start"` // /proc/<pid>/stat starttime (Linux), "" elsewhere
	Host     string    `json:"host"`
	Op       string    `json:"op"`
	Since    time.Time `json:"since"`
}

// newState returns an empty v1 state.
func newState() *State {
	return &State{Version: Version, Clusters: map[string]*Cluster{}, Worktrees: map[string]*Worktree{}}
}

// TakenPorts returns every port recorded by any worktree, optionally excluding one key.
func (s *State) TakenPorts(exceptKey string) map[int]string {
	out := map[int]string{}
	for k, w := range s.Worktrees {
		if k == exceptKey {
			continue
		}
		for v, p := range w.Ports {
			out[p] = k + ":" + v
		}
	}
	for _, c := range s.Clusters {
		if c.Kind == "docker" && c.Port > 0 {
			out[c.Port] = "cluster:" + c.ID
		}
	}
	return out
}
