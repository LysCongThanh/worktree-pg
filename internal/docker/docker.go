// Package docker drives the docker CLI through os/exec (no SDK) for the
// tool-managed Postgres cluster: inspect, image volumes, run, start, remove.
// Secrets are passed to `docker run` by variable name only (`-e NAME`), with the
// value in the child environment, never in argv (spike S6 / D6).
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// Container is the subset of `docker inspect` used by worktree-pg.
type Container struct {
	Name    string
	Image   string
	Running bool
	Labels  map[string]string
	Port    int // host port bound to the container's 5432/tcp (0 = none)
	HostIP  string
}

// RunSpec describes a detached `docker run`.
type RunSpec struct {
	Name       string
	Image      string
	Labels     map[string]string
	BindIP     string // loopback address to publish on (invariant S12)
	HostPort   int
	Volume     string // named volume
	VolumePath string // mount point inside the container (image Config.Volumes, spike D5)
	SecretEnv  map[string]string
}

// cli runs `docker args...` with extra env and returns stdout.
func cli(ctx context.Context, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, errs.Wrap(errs.Prereq, errs.CodeDockerUnavailable, err, "docker not found on PATH")
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errs.Wrap(errs.Internal, errs.CodeInternal, err, "docker %s: %s", args[0], strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Available checks that the docker daemon answers.
func Available(ctx context.Context) error {
	if _, err := cli(ctx, nil, "version", "--format", "{{.Server.Version}}"); err != nil {
		return errs.Wrap(errs.Prereq, errs.CodeDockerUnavailable, err, "docker daemon is not reachable").
			WithHint("start docker, or use [cluster] source = \"external\"")
	}
	return nil
}

// inspectDoc mirrors the fields of `docker inspect <container>` we read.
type inspectDoc struct {
	Name   string
	Config struct {
		Image  string
		Labels map[string]string
	}
	State      struct{ Running bool }
	HostConfig struct {
		PortBindings map[string][]struct{ HostIp, HostPort string }
	}
}

// Inspect returns the container, or nil when no container has that name.
func Inspect(ctx context.Context, name string) (*Container, error) {
	out, err := cli(ctx, nil, "container", "inspect", name)
	if err != nil {
		if strings.Contains(err.Error(), "No such") || strings.Contains(err.Error(), "no such") {
			return nil, nil
		}
		return nil, err
	}
	var docs []inspectDoc
	if err := json.Unmarshal(out, &docs); err != nil || len(docs) != 1 {
		return nil, errs.New(errs.Internal, errs.CodeInternal, "unexpected docker inspect output for %s", name)
	}
	d := docs[0]
	c := &Container{Name: strings.TrimPrefix(d.Name, "/"), Image: d.Config.Image, Running: d.State.Running, Labels: d.Config.Labels}
	for _, b := range d.HostConfig.PortBindings["5432/tcp"] {
		if p, err := strconv.Atoi(b.HostPort); err == nil && p > 0 {
			c.Port, c.HostIP = p, b.HostIp
			break
		}
	}
	return c, nil
}

// ImageVolumes returns the image's declared VOLUME paths (Config.Volumes), pulling
// the image first when it is not present locally.
func ImageVolumes(ctx context.Context, image string) ([]string, error) {
	out, err := cli(ctx, nil, "image", "inspect", "--format", "{{json .Config.Volumes}}", image)
	if err != nil {
		if _, perr := cli(ctx, nil, "pull", "-q", image); perr != nil {
			return nil, errs.Wrap(errs.Prereq, errs.CodeDockerUnavailable, perr, "pull %s", image)
		}
		if out, err = cli(ctx, nil, "image", "inspect", "--format", "{{json .Config.Volumes}}", image); err != nil {
			return nil, err
		}
	}
	var vols map[string]struct{}
	if err := json.Unmarshal(bytes.TrimSpace(out), &vols); err != nil {
		return nil, errs.Wrap(errs.Internal, errs.CodeInternal, err, "parse volumes of %s", image)
	}
	paths := make([]string, 0, len(vols))
	for p := range vols {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}

// Run starts a detached container per spec.
func Run(ctx context.Context, s RunSpec) error {
	args := []string{"run", "-d", "--name", s.Name, "--restart", "unless-stopped",
		"-p", s.BindIP + ":" + strconv.Itoa(s.HostPort) + ":5432"}
	var labels []string
	for k, v := range s.Labels {
		labels = append(labels, k+"="+v)
	}
	sort.Strings(labels)
	for _, l := range labels {
		args = append(args, "--label", l)
	}
	if s.Volume != "" {
		args = append(args, "-v", s.Volume+":"+s.VolumePath)
	}
	var env []string
	for k, v := range s.SecretEnv {
		args = append(args, "-e", k) // name only: value comes from the child env
		env = append(env, k+"="+v)
	}
	_, err := cli(ctx, env, append(args, s.Image)...)
	return err
}

// Start starts a stopped container.
func Start(ctx context.Context, name string) error {
	_, err := cli(ctx, nil, "start", name)
	return err
}

// Remove force-removes a container and, when volume is set, its named volume.
// Only called for containers proven ours by label (tests and explicit teardown).
func Remove(ctx context.Context, name, volume string) error {
	if _, err := cli(ctx, nil, "rm", "-f", name); err != nil {
		return err
	}
	if volume != "" {
		_, err := cli(ctx, nil, "volume", "rm", "-f", volume)
		return err
	}
	return nil
}
