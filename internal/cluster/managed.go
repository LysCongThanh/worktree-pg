package cluster

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/docker"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
)

// Managed-cluster actions reported by Ensure.
const (
	ActCreateCluster = "create_cluster"
	ActStartCluster  = "start_cluster"
)

// defaultDataPath is used when an image declares no VOLUME.
const defaultDataPath = "/var/lib/postgresql/data"

// Managed is one tool-managed postgres container per image, labeled with a
// cluster marker, published on a loopback address only (invariant S12).
type Managed struct {
	Image        string
	Container    string
	Volume       string
	BindIP       string
	Port         int    // host port; replaced by the existing container's binding
	Password     string // superuser password (secret.Password("cluster/<container>"))
	ReadyTimeout time.Duration
}

// NewManaged describes the managed cluster for image. host is [database].host
// and must be a loopback name or address.
func NewManaged(image, host string, port int, password string) (*Managed, error) {
	ip, err := LoopbackIP(host)
	if err != nil {
		return nil, err
	}
	c := names.Container(image)
	return &Managed{Image: image, Container: c, Volume: c + "-data", BindIP: ip, Port: port, Password: password}, nil
}

// LoopbackIP maps host to the loopback address the container is published on.
func LoopbackIP(host string) (string, error) {
	if strings.EqualFold(host, "localhost") {
		return "127.0.0.1", nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return ip.String(), nil
	}
	return "", errs.New(errs.Config, errs.CodeConfigInvalid, "managed docker clusters bind loopback only; [database].host %q is not loopback", host).
		WithHint("use host = \"127.0.0.1\" or \"localhost\"")
}

// Marker is the label value proving a container belongs to worktree-pg.
func (m *Managed) Marker() string {
	return names.Marker{Kind: names.KindCluster, Key: m.Container}.String()
}

// Ensure creates or starts the container and waits for host TCP readiness.
// An existing container without our label is refused (invariant S1).
func (m *Managed) Ensure(ctx context.Context) (actions []string, err error) {
	if err := docker.Available(ctx); err != nil {
		return nil, err
	}
	c, err := docker.Inspect(ctx, m.Container)
	if err != nil {
		return nil, err
	}
	if c == nil {
		if actions, err = m.create(ctx); err != nil {
			return actions, err
		}
		if c, err = docker.Inspect(ctx, m.Container); err != nil || c == nil {
			return actions, errs.New(errs.Prereq, errs.CodeDockerUnavailable, "container %s vanished after docker run", m.Container)
		}
	}
	if mk, ok := names.ParseMarker(c.Labels[names.LabelKey]); !ok || mk.Kind != names.KindCluster {
		return actions, errs.New(errs.Conflict, errs.CodeForeignResource, "container %s exists but was not created by worktree-pg", m.Container).
			WithHint("remove or rename it yourself")
	}
	if c.Port > 0 {
		m.Port = c.Port
	}
	if !c.Running {
		if err := docker.Start(ctx, m.Container); err != nil {
			return actions, err
		}
		actions = append(actions, ActStartCluster)
	}
	return actions, docker.WaitReady(ctx, m.ReadyTimeout, func(ctx context.Context) error {
		a, err := pg.Connect(ctx, m.DSN())
		a.Close(ctx)
		return err
	})
}

// create runs the container with its named volume mounted at the image's
// declared VOLUME path (spike D5: PG18 uses /var/lib/postgresql).
func (m *Managed) create(ctx context.Context) ([]string, error) {
	vols, err := docker.ImageVolumes(ctx, m.Image)
	if err != nil {
		return nil, err
	}
	path := defaultDataPath
	if len(vols) > 0 {
		path = vols[0]
	}
	err = docker.Run(ctx, docker.RunSpec{
		Name: m.Container, Image: m.Image, Labels: map[string]string{names.LabelKey: m.Marker()},
		BindIP: m.BindIP, HostPort: m.Port, Volume: m.Volume, VolumePath: path,
		SecretEnv: map[string]string{"POSTGRES_PASSWORD": m.Password},
	})
	if err != nil && strings.Contains(err.Error(), "already in use") { // parallel up created it
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []string{ActCreateCluster}, nil
}

// DSN is the superuser admin DSN of the managed cluster (secret).
func (m *Managed) DSN() string {
	return pg.URL(pg.Cred{Host: m.BindIP, Port: m.Port, User: "postgres", Password: m.Password, Database: "postgres", Params: "sslmode=disable"})
}

// Cluster returns the resolved cluster for connecting.
func (m *Managed) Cluster() *Cluster {
	return &Cluster{ID: m.Container, Kind: KindDocker, Image: m.Image, Container: m.Container, Host: m.BindIP, Port: m.Port, dsn: m.DSN()}
}
