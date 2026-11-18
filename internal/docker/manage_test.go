package docker

import (
	"context"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestContainerListParsesRowsAndMarksPanelOwned(t *testing.T) {
	c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v"+apiVersion+"/containers/json" || r.URL.Query().Get("all") != "1" {
			t.Errorf("unexpected request %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`[
		 {"Id":"bbb","Names":["/redis"],"Image":"redis:7","ImageID":"sha256:r","Command":"redis-server","Created":1700000000,
		  "State":"running","Status":"Up 2 hours","Ports":[{"IP":"0.0.0.0","PrivatePort":6379,"PublicPort":6379,"Type":"tcp"}],
		  "Labels":null,"Mounts":[{"Type":"volume","Name":"redis-data"},{"Type":"bind","Name":""}]},
		 {"Id":"aaa","Names":["/nginx"],"Image":"panel/nginx:1","ImageID":"sha256:n","Command":"nginx","Created":1700000100,
		  "State":"running","Status":"Up","Ports":[],"Labels":{"panel.managed":"true"},"Mounts":[]}]`))
	}))
	got, err := c.ContainerList(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "nginx" || got[1].Name != "redis" {
		t.Fatalf("want rows sorted by name, got %+v", got)
	}
	if !got[0].Managed || got[1].Managed {
		t.Errorf("managed flags wrong: %+v", got)
	}
	if got[1].Labels == nil || len(got[1].Volumes) != 1 || got[1].Volumes[0] != "redis-data" {
		t.Errorf("redis volumes/labels: %+v", got[1])
	}
	if got[1].Ports[0].PublicPort != 6379 {
		t.Errorf("ports: %+v", got[1].Ports)
	}
	if !got[1].Created.Equal(time.Unix(1700000000, 0)) {
		t.Errorf("created: %v", got[1].Created)
	}
}

func TestContainerStatsComputesRatesLikeDockerStats(t *testing.T) {
	c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("stream") != "false" {
			t.Errorf("stats must not stream: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"id":"abc","name":"/web",
		 "cpu_stats":{"cpu_usage":{"total_usage":300},"system_cpu_usage":2000,"online_cpus":4},
		 "precpu_stats":{"cpu_usage":{"total_usage":100},"system_cpu_usage":1000},
		 "memory_stats":{"usage":1000,"limit":4000,"stats":{"inactive_file":200}},
		 "networks":{"eth0":{"rx_bytes":10,"tx_bytes":20},"eth1":{"rx_bytes":1,"tx_bytes":2}},
		 "blkio_stats":{"io_service_bytes_recursive":[{"op":"Read","value":5},{"op":"Write","value":7},{"op":"Total","value":12}]},
		 "pids_stats":{"current":9}}`))
	}))
	s, err := c.ContainerStats(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	// (300-100)/(2000-1000) * 4 cpus * 100 = 80
	if math.Abs(s.CPUPercent-80) > 1e-9 {
		t.Errorf("cpu = %v, want 80", s.CPUPercent)
	}
	if s.MemUsage != 800 || math.Abs(s.MemPercent-20) > 1e-9 {
		t.Errorf("memory %d (%v%%), want 800 (20%%): cache must be subtracted", s.MemUsage, s.MemPercent)
	}
	if s.NetRx != 11 || s.NetTx != 22 || s.BlockRead != 5 || s.BlockWrite != 7 || s.Pids != 9 || s.Name != "web" {
		t.Errorf("counters wrong: %+v", s)
	}
}

func TestContainerStatsWithoutPreviousSampleIsZeroNotNaN(t *testing.T) {
	c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"abc","name":"/web","cpu_stats":{"cpu_usage":{"total_usage":300},"system_cpu_usage":2000},
		 "precpu_stats":{},"memory_stats":{"usage":10,"limit":0}}`))
	}))
	s, err := c.ContainerStats(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if s.CPUPercent != 0 || s.MemPercent != 0 || math.IsNaN(s.CPUPercent) {
		t.Errorf("want zeros, got %+v", s)
	}
}

func TestStatsAllSkipsContainersThatVanish(t *testing.T) {
	c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/gone/") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"No such container: gone"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"x","name":"/x"}`))
	}))
	got := c.StatsAll(context.Background(), []string{"a", "gone", "b"})
	if len(got) != 2 {
		t.Fatalf("want 2 samples, got %+v", got)
	}
}

func TestImageListHidesNoneTagsAndMarksDangling(t *testing.T) {
	c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
		 {"Id":"sha256:old","RepoTags":["<none>:<none>"],"RepoDigests":null,"Size":5,"Created":100},
		 {"Id":"sha256:new","RepoTags":["redis:7","redis:latest"],"RepoDigests":["redis@sha256:z"],"Size":9,"Created":200}]`))
	}))
	got, err := c.ImageList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "sha256:new" {
		t.Fatalf("want newest first, got %+v", got)
	}
	if got[0].Dangling || len(got[0].Tags) != 2 {
		t.Errorf("tagged image: %+v", got[0])
	}
	if !got[1].Dangling || len(got[1].Tags) != 0 || got[1].Digests == nil {
		t.Errorf("dangling image: %+v", got[1])
	}
}

func TestVolumeListAndRemove(t *testing.T) {
	var deleted string
	c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"Volumes":[{"Name":"b","Driver":"local","Mountpoint":"/v/b","Scope":"local","CreatedAt":"2026-01-01T00:00:00Z"},
		 {"Name":"a","Driver":"local","Mountpoint":"/v/a","Scope":"local","Labels":{"k":"v"}}],"Warnings":null}`))
	}))
	got, err := c.VolumeList(context.Background())
	if err != nil || len(got) != 2 || got[0].Name != "a" || got[1].Labels == nil {
		t.Fatalf("list: %v %+v", err, got)
	}
	if err := c.VolumeRemove(context.Background(), "a"); err != nil || deleted != "/v"+apiVersion+"/volumes/a" {
		t.Fatalf("remove: %v path=%q", err, deleted)
	}
	if err := c.VolumeRemove(context.Background(), "../etc"); err == nil {
		t.Fatal("a volume name that is a path must be refused before any request")
	}
}

func TestContainerTopAndRestart(t *testing.T) {
	var restartQuery string
	c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/top"):
			_, _ = w.Write([]byte(`{"Titles":["PID","CMD"],"Processes":[["1","nginx"],["7","worker"]]}`))
		case strings.HasSuffix(r.URL.Path, "/restart"):
			restartQuery = r.URL.RawQuery
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	top, err := c.ContainerTop(context.Background(), "web")
	if err != nil || len(top.Titles) != 2 || len(top.Processes) != 2 {
		t.Fatalf("top: %v %+v", err, top)
	}
	if err := c.ContainerRestart(context.Background(), "web", 10*time.Second); err != nil || restartQuery != "t=10" {
		t.Fatalf("restart: %v query=%q", err, restartQuery)
	}
}

func TestInfo(t *testing.T) {
	c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ServerVersion":"29.9.0","OperatingSystem":"Ubuntu","Driver":"overlay2","DockerRootDir":"/var/lib/docker",
		 "NCPU":4,"MemTotal":8000,"Containers":3,"ContainersRunning":2,"ContainersPaused":0,"ContainersStopped":1,"Images":5}`))
	}))
	i, err := c.Info(context.Background())
	if err != nil || i.ServerVersion != "29.9.0" || i.Running != 2 || i.Stopped != 1 || i.Images != 5 || i.CPUs != 4 {
		t.Fatalf("%v %+v", err, i)
	}
}
