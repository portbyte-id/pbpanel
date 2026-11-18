package docker

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// This file holds the read side and the lifecycle controls behind the Docker
// page: listings, one-shot resource samples and process lists. Nothing here
// creates a container; creation stays in Spec/Ensure, which owns the panel's
// own containers.

// EngineInfo is what the Docker page shows about the Engine itself.
type EngineInfo struct {
	ServerVersion   string `json:"serverVersion"`
	OperatingSystem string `json:"operatingSystem"`
	KernelVersion   string `json:"kernelVersion"`
	Driver          string `json:"storageDriver"`
	RootDir         string `json:"rootDir"`
	CPUs            int    `json:"cpus"`
	MemTotal        int64  `json:"memTotal"`
	Containers      int    `json:"containers"`
	Running         int    `json:"running"`
	Paused          int    `json:"paused"`
	Stopped         int    `json:"stopped"`
	Images          int    `json:"images"`
}

// Info reads the Engine's /info.
func (c *Client) Info(ctx context.Context) (EngineInfo, error) {
	var raw struct {
		ServerVersion     string
		OperatingSystem   string
		KernelVersion     string
		Driver            string
		DockerRootDir     string
		NCPU              int
		MemTotal          int64
		Containers        int
		ContainersRunning int
		ContainersPaused  int
		ContainersStopped int
		Images            int
	}
	if err := c.call(ctx, request{op: "info", method: http.MethodGet, path: "/info"}, &raw); err != nil {
		return EngineInfo{}, err
	}
	return EngineInfo{
		ServerVersion: raw.ServerVersion, OperatingSystem: raw.OperatingSystem, KernelVersion: raw.KernelVersion,
		Driver: raw.Driver, RootDir: raw.DockerRootDir, CPUs: raw.NCPU, MemTotal: raw.MemTotal,
		Containers: raw.Containers, Running: raw.ContainersRunning, Paused: raw.ContainersPaused,
		Stopped: raw.ContainersStopped, Images: raw.Images,
	}, nil
}

// Port is one published or exposed container port.
type Port struct {
	IP          string `json:"ip,omitempty"`
	PrivatePort int    `json:"privatePort"`
	PublicPort  int    `json:"publicPort,omitempty"`
	Type        string `json:"type"`
}

// ContainerSummary is one row of the container list.
type ContainerSummary struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	ImageID string            `json:"imageId"`
	Command string            `json:"command"`
	Created time.Time         `json:"created"`
	State   string            `json:"state"`
	Status  string            `json:"status"`
	Ports   []Port            `json:"ports"`
	Labels  map[string]string `json:"labels"`
	// Volumes are the named volumes the container mounts.
	Volumes []string `json:"volumes"`
	// Managed is true for the panel's own containers (nginx, PHP-FPM).
	Managed bool `json:"managed"`
}

// ContainerList lists containers, running ones only unless all is set.
func (c *Client) ContainerList(ctx context.Context, all bool) ([]ContainerSummary, error) {
	q := url.Values{}
	if all {
		q.Set("all", "1")
	}
	var raw []struct {
		ID      string `json:"Id"`
		Names   []string
		Image   string
		ImageID string
		Command string
		Created int64
		State   string
		Status  string
		Ports   []struct {
			IP          string
			PrivatePort int
			PublicPort  int
			Type        string
		}
		Labels map[string]string
		Mounts []struct {
			Type string
			Name string
		}
	}
	if err := c.call(ctx, request{op: "list containers", method: http.MethodGet, path: "/containers/json", query: q}, &raw); err != nil {
		return nil, err
	}
	out := make([]ContainerSummary, 0, len(raw))
	for _, r := range raw {
		name := ""
		if len(r.Names) > 0 {
			name = trimSlash(r.Names[0])
		}
		ports := make([]Port, 0, len(r.Ports))
		for _, p := range r.Ports {
			ports = append(ports, Port{IP: p.IP, PrivatePort: p.PrivatePort, PublicPort: p.PublicPort, Type: p.Type})
		}
		vols := []string{}
		for _, m := range r.Mounts {
			if m.Type == "volume" && m.Name != "" {
				vols = append(vols, m.Name)
			}
		}
		if r.Labels == nil {
			r.Labels = map[string]string{}
		}
		out = append(out, ContainerSummary{
			ID: r.ID, Name: name, Image: r.Image, ImageID: r.ImageID, Command: r.Command,
			Created: time.Unix(r.Created, 0).UTC(), State: r.State, Status: r.Status, Ports: ports,
			Labels: r.Labels, Volumes: vols, Managed: r.Labels[LabelManaged] == "true",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ContainerRestart stops a container (killing it after timeout) and starts it again.
func (c *Client) ContainerRestart(ctx context.Context, id string, timeout time.Duration) error {
	if err := checkName("container", id); err != nil {
		return err
	}
	q := url.Values{"t": {strconv.Itoa(int(timeout / time.Second))}}
	return c.call(ctx, request{op: "restart container " + id, method: http.MethodPost, path: "/containers/" + id + "/restart", query: q}, nil)
}

// ContainerStats is one resource sample of a running container.
type ContainerStats struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	CPUPercent float64 `json:"cpuPercent"`
	MemUsage   int64   `json:"memUsage"`
	MemLimit   int64   `json:"memLimit"`
	MemPercent float64 `json:"memPercent"`
	NetRx      int64   `json:"netRx"`
	NetTx      int64   `json:"netTx"`
	BlockRead  int64   `json:"blockRead"`
	BlockWrite int64   `json:"blockWrite"`
	Pids       int     `json:"pids"`
}

type statsWire struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	CPUStats struct {
		CPUUsage struct {
			TotalUsage int64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemUsage int64 `json:"system_cpu_usage"`
		OnlineCPUs  int   `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage int64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemUsage int64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage int64            `json:"usage"`
		Limit int64            `json:"limit"`
		Stats map[string]int64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes int64 `json:"rx_bytes"`
		TxBytes int64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IOServiceBytes []struct {
			Op    string `json:"op"`
			Value int64  `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
	PidsStats struct {
		Current int `json:"current"`
	} `json:"pids_stats"`
}

// ContainerStats samples a running container once. The Engine takes about a
// second to answer: CPU is a rate, so it needs two readings to subtract.
func (c *Client) ContainerStats(ctx context.Context, id string) (ContainerStats, error) {
	if err := checkName("container", id); err != nil {
		return ContainerStats{}, err
	}
	var w statsWire
	q := url.Values{"stream": {"false"}}
	if err := c.call(ctx, request{op: "stats of container " + id, method: http.MethodGet, path: "/containers/" + id + "/stats", query: q}, &w); err != nil {
		return ContainerStats{}, err
	}
	return w.summary(), nil
}

func (w statsWire) summary() ContainerStats {
	out := ContainerStats{ID: w.ID, Name: trimSlash(w.Name), Pids: w.PidsStats.Current, MemLimit: w.MemoryStats.Limit}

	cpuDelta := float64(w.CPUStats.CPUUsage.TotalUsage - w.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(w.CPUStats.SystemUsage - w.PreCPUStats.SystemUsage)
	// No previous reading (precpu all zero) would make the delta the whole
	// lifetime of the container, which is not a rate.
	if w.PreCPUStats.SystemUsage > 0 && cpuDelta > 0 && sysDelta > 0 {
		cpus := w.CPUStats.OnlineCPUs
		if cpus == 0 {
			cpus = 1
		}
		out.CPUPercent = cpuDelta / sysDelta * float64(cpus) * 100
	}

	// Cache is not memory the container is holding on to: `docker stats`
	// subtracts it, so the page agrees with what the operator sees there.
	usage := w.MemoryStats.Usage
	if v, ok := w.MemoryStats.Stats["inactive_file"]; ok && v < usage { // cgroup v2
		usage -= v
	} else if v, ok := w.MemoryStats.Stats["total_inactive_file"]; ok && v < usage { // cgroup v1
		usage -= v
	}
	out.MemUsage = usage
	if out.MemLimit > 0 {
		out.MemPercent = float64(usage) / float64(out.MemLimit) * 100
	}

	for _, n := range w.Networks {
		out.NetRx += n.RxBytes
		out.NetTx += n.TxBytes
	}
	for _, b := range w.BlkioStats.IOServiceBytes {
		switch strings.ToLower(b.Op) {
		case "read":
			out.BlockRead += b.Value
		case "write":
			out.BlockWrite += b.Value
		}
	}
	return out
}

// maxStatsParallel bounds how many one-second samples run at once.
const maxStatsParallel = 8

// StatsAll samples the given running containers concurrently. A container that
// stops between the list and its sample is left out rather than failing the lot.
func (c *Client) StatsAll(ctx context.Context, ids []string) []ContainerStats {
	out := make([]ContainerStats, len(ids))
	ok := make([]bool, len(ids))
	sem := make(chan struct{}, maxStatsParallel)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if s, err := c.ContainerStats(ctx, id); err == nil {
				out[i], ok[i] = s, true
			}
		}()
	}
	wg.Wait()
	res := make([]ContainerStats, 0, len(ids))
	for i := range out {
		if ok[i] {
			res = append(res, out[i])
		}
	}
	return res
}

// ProcessList is a container's process table as `ps` columns.
type ProcessList struct {
	Titles    []string   `json:"titles"`
	Processes [][]string `json:"processes"`
}

// ContainerTop lists the processes inside a running container.
func (c *Client) ContainerTop(ctx context.Context, id string) (ProcessList, error) {
	if err := checkName("container", id); err != nil {
		return ProcessList{}, err
	}
	var raw struct {
		Titles    []string
		Processes [][]string
	}
	if err := c.call(ctx, request{op: "top of container " + id, method: http.MethodGet, path: "/containers/" + id + "/top"}, &raw); err != nil {
		return ProcessList{}, err
	}
	if raw.Titles == nil {
		raw.Titles = []string{}
	}
	if raw.Processes == nil {
		raw.Processes = [][]string{}
	}
	return ProcessList{Titles: raw.Titles, Processes: raw.Processes}, nil
}

// ImageSummary is one row of the image list.
type ImageSummary struct {
	ID       string    `json:"id"`
	Tags     []string  `json:"tags"`
	Digests  []string  `json:"digests"`
	Size     int64     `json:"size"`
	Created  time.Time `json:"created"`
	Dangling bool      `json:"dangling"`
}

// ImageList lists the images on the host, newest first.
func (c *Client) ImageList(ctx context.Context) ([]ImageSummary, error) {
	var raw []struct {
		ID          string `json:"Id"`
		RepoTags    []string
		RepoDigests []string
		Size        int64
		Created     int64
	}
	if err := c.call(ctx, request{op: "list images", method: http.MethodGet, path: "/images/json"}, &raw); err != nil {
		return nil, err
	}
	out := make([]ImageSummary, 0, len(raw))
	for _, r := range raw {
		tags := make([]string, 0, len(r.RepoTags))
		for _, t := range r.RepoTags {
			if t != "<none>:<none>" {
				tags = append(tags, t)
			}
		}
		digests := r.RepoDigests
		if digests == nil {
			digests = []string{}
		}
		out = append(out, ImageSummary{
			ID: r.ID, Tags: tags, Digests: digests, Size: r.Size,
			Created: time.Unix(r.Created, 0).UTC(), Dangling: len(tags) == 0,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// Volume is one named volume.
type Volume struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Mountpoint string            `json:"mountpoint"`
	Scope      string            `json:"scope"`
	CreatedAt  string            `json:"createdAt"`
	Labels     map[string]string `json:"labels"`
}

// VolumeList lists named volumes.
func (c *Client) VolumeList(ctx context.Context) ([]Volume, error) {
	var raw struct {
		Volumes []struct {
			Name       string
			Driver     string
			Mountpoint string
			Scope      string
			CreatedAt  string
			Labels     map[string]string
		}
	}
	if err := c.call(ctx, request{op: "list volumes", method: http.MethodGet, path: "/volumes"}, &raw); err != nil {
		return nil, err
	}
	out := make([]Volume, 0, len(raw.Volumes))
	for _, v := range raw.Volumes {
		if v.Labels == nil {
			v.Labels = map[string]string{}
		}
		out = append(out, Volume{
			Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, Scope: v.Scope,
			CreatedAt: v.CreatedAt, Labels: v.Labels,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// VolumeRemove deletes a volume. The Engine refuses while a container uses it
// (409); that refusal is returned as is, never forced.
func (c *Client) VolumeRemove(ctx context.Context, name string) error {
	if err := checkName("volume", name); err != nil {
		return err
	}
	return c.call(ctx, request{op: "remove volume " + name, method: http.MethodDelete, path: "/volumes/" + name}, nil)
}
