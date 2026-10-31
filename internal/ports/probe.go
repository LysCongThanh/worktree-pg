package ports

import (
	"context"
	"encoding/json"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

// ListenProbe returns a Probe that treats a port as free only if a TCP listener
// can be opened both on the wildcard address (":p", dual-stack, catches v4 and v6
// binds — spike S7) and on the configured host ("127.0.0.1:p"/"localhost:p").
// It never touches the process holding a port (invariant S8: never kill).
func ListenProbe(host string) func(int) bool {
	return func(p int) bool {
		addrs := []string{":" + strconv.Itoa(p)}
		if host != "" {
			addrs = append(addrs, net.JoinHostPort(host, strconv.Itoa(p)))
		}
		for _, addr := range addrs {
			l, err := net.Listen("tcp", addr)
			if err != nil {
				return false
			}
			_ = l.Close()
		}
		return true
	}
}

// inspectDoc is the subset of `docker inspect` output we read.
type inspectDoc struct {
	HostConfig struct {
		PortBindings map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"PortBindings"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
}

// ParseDockerBindings extracts host ports from `docker inspect` JSON (an array).
// It reads HostConfig.PortBindings, which is kept for stopped containers (spike S7:
// NetworkSettings.Ports is empty when stopped), plus runtime Ports for random binds.
func ParseDockerBindings(raw []byte) (PortSet, error) {
	var docs []inspectDoc
	if err := json.Unmarshal(raw, &docs); err != nil {
		return nil, err
	}
	set := PortSet{}
	add := func(hp string) {
		if p, err := strconv.Atoi(hp); err == nil && p > 0 {
			set.Add(p)
		}
	}
	for _, d := range docs {
		for _, bs := range d.HostConfig.PortBindings {
			for _, b := range bs {
				add(b.HostPort)
			}
		}
		for _, bs := range d.NetworkSettings.Ports {
			for _, b := range bs {
				add(b.HostPort)
			}
		}
	}
	return set, nil
}

// DockerBound returns host ports bound by any docker container, running or stopped.
// Best effort: when docker is absent or fails, it returns an empty set and the error.
func DockerBound(ctx context.Context) (PortSet, error) {
	ids, err := exec.CommandContext(ctx, "docker", "ps", "-aq").Output()
	if err != nil {
		return PortSet{}, err
	}
	list := strings.Fields(string(ids))
	if len(list) == 0 {
		return PortSet{}, nil
	}
	out, err := exec.CommandContext(ctx, "docker", append([]string{"inspect"}, list...)...).Output()
	return boundFromInspect(out, err)
}

// boundFromInspect keeps what `docker inspect` printed even when it exited
// non-zero: a container removed between `ps` and `inspect` fails the whole command
// while the JSON for every other container is still complete (S8: one vanished
// container must not hide the reserved ports of all the stopped ones).
func boundFromInspect(out []byte, runErr error) (PortSet, error) {
	set, perr := ParseDockerBindings(out)
	if perr != nil {
		if runErr != nil {
			return PortSet{}, runErr
		}
		return PortSet{}, perr
	}
	return set, runErr
}
