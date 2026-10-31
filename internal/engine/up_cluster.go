package engine

import (
	"context"

	"github.com/LysCongThanh/worktree-pg/internal/cluster"
	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
	"github.com/LysCongThanh/worktree-pg/internal/ports"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
)

// clusterPortVar is the allocator variable name used for a managed cluster's port.
const clusterPortVar = "PGPORT"

// clusterID is the registry id of the cluster cfg points at (no I/O for docker).
func (e *Engine) clusterID(cfg *config.Config) (string, error) {
	if cfg.Cluster.Source == cluster.KindDocker {
		return names.Container(cfg.Cluster.Image), nil
	}
	c, err := cluster.External(cfg, e.Opts.Getenv)
	if err != nil {
		return "", err
	}
	return c.ID, nil
}

// openCluster resolves and connects the cluster for cfg. For a managed docker
// cluster it allocates (once, sticky in the registry) its loopback port and, when
// start is set, creates or starts the container (actions reported).
func (e *Engine) openCluster(ctx context.Context, cfg *config.Config, start bool) (*cluster.Cluster, *pg.Admin, []string, error) {
	if cfg.Cluster.Source != cluster.KindDocker {
		c, err := cluster.External(cfg, e.Opts.Getenv)
		if err != nil {
			return nil, nil, nil, err
		}
		a, err := c.Connect(ctx)
		return c, a, nil, err
	}
	id := names.Container(cfg.Cluster.Image)
	port, err := e.managedPort(ctx, cfg, id)
	if err != nil {
		return nil, nil, nil, err
	}
	m, err := cluster.NewManaged(cfg.Cluster.Image, cfg.Database.Host, port, e.Secret.Password("cluster/"+id))
	if err != nil {
		return nil, nil, nil, err
	}
	var acts []string
	if start {
		if acts, err = m.Ensure(ctx); err != nil {
			return nil, nil, acts, err
		}
	}
	c := m.Cluster()
	a, err := c.Connect(ctx)
	return c, a, acts, err
}

// managedPort returns the managed cluster's recorded port, allocating one from
// [ports].ranges under the registry lock on first use (invariant S8).
func (e *Engine) managedPort(ctx context.Context, cfg *config.Config, id string) (int, error) {
	port := 0
	_ = e.Store.View(func(st *registry.State) error {
		if c := st.Clusters[id]; c != nil {
			port = c.Port
		}
		return nil
	})
	if port > 0 {
		return port, nil
	}
	bound, _ := ports.DockerBound(ctx) // best effort; never under the lock (S10)
	err := e.Store.Update(ctx, func(st *registry.State) error {
		if c := st.Clusters[id]; c != nil && c.Port > 0 {
			port = c.Port
			return nil
		}
		a, err := allocator(cfg, st, id, bound, nil)
		if err != nil {
			return err
		}
		got, _, err := a.Allocate(id, []string{clusterPortVar}, nil)
		if err != nil {
			return err
		}
		port = got[clusterPortVar]
		host, _ := cluster.LoopbackIP(cfg.Database.Host)
		st.Clusters[id] = &registry.Cluster{ID: id, Kind: cluster.KindDocker, Image: cfg.Cluster.Image, Container: id, Host: host, Port: port}
		return nil
	})
	return port, err
}

// allocator builds the port allocator for one key: configured ranges, minus
// ports taken by other worktrees/clusters, reserved ports and docker-bound ports
// (except the key's own sticky ports, which its own containers may be using).
func allocator(cfg *config.Config, st *registry.State, key string, bound ports.PortSet, own map[string]int) (*ports.Allocator, error) {
	def, err := ports.ParseRanges(cfg.Ports.Ranges)
	if err != nil {
		return nil, err
	}
	rs := map[string][]ports.Range{ports.DefaultKey: def}
	for v, r := range cfg.Ports.Vars {
		if r != "" {
			if rs[v], err = ports.ParseRanges([]string{r}); err != nil {
				return nil, err
			}
		}
	}
	ex := ports.PortSet{}
	for p := range st.TakenPorts(key) {
		ex.Add(p)
	}
	ex.Add(cfg.Ports.Reserved...)
	mine := map[int]bool{}
	for _, p := range own {
		mine[p] = true
	}
	for p := range bound {
		if !mine[p] {
			ex.Add(p)
		}
	}
	return &ports.Allocator{Ranges: rs, Exclude: ex, Ephemeral: ports.Ephemeral(), Probe: ports.ListenProbe(cfg.Database.Host)}, nil
}

// connectRecord connects to a cluster recorded in the registry (down, status,
// gc). A managed container is started only when start is set; an external
// cluster must be the one the current admin DSN points at.
func (e *Engine) connectRecord(ctx context.Context, cfg *config.Config, c *registry.Cluster, start bool) (*cluster.Cluster, *pg.Admin, error) {
	if c.Kind == cluster.KindDocker {
		m := &cluster.Managed{Image: c.Image, Container: c.Container, Volume: c.Container + "-data", BindIP: c.Host,
			Port: c.Port, Password: e.Secret.Password("cluster/" + c.Container)}
		if start {
			if _, err := m.Ensure(ctx); err != nil {
				return nil, nil, err
			}
		}
		cl := m.Cluster()
		a, err := cl.Connect(ctx)
		return cl, a, err
	}
	cl, err := cluster.External(cfg, e.Opts.Getenv)
	if err != nil {
		return nil, nil, err
	}
	if c.ID != "" && cl.ID != c.ID {
		return nil, nil, errs.New(errs.Prereq, errs.CodePGUnreachable, "the admin DSN points at %s:%d, not the cluster this worktree was provisioned on", cl.Host, cl.Port).
			WithHint("export the admin DSN of the original cluster")
	}
	a, err := cl.Connect(ctx)
	return cl, a, err
}
