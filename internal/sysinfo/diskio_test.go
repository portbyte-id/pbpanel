package sysinfo

import (
	"context"
	"fmt"
	"testing"
	"time"
)

const diskstatsFixture = `   8       0 sda 1000 10 80000 500 2000 20 160000 900 0 800 1400
   8       1 sda1 900 10 72000 450 1900 20 152000 850 0 700 1300
 259       0 nvme0n1 50 0 4000 20 60 0 4800 30 0 40 50
 259       1 nvme0n1p1 40 0 3200 10 50 0 4000 20 0 30 30
 253       0 dm-0 700 0 56000 300 1500 0 120000 700 0 600 1000
   7       0 loop0 5 0 40 1 0 0 0 0 0 1 1
   8      16 sdb short
`

func TestParseDiskstats(t *testing.T) {
	got, err := ParseDiskstats([]byte(diskstatsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("parsed %d devices, want 6 (short line skipped): %v", len(got), got)
	}
	if d := got["sda1"]; d.SectorsRead != 72000 || d.SectorsWrite != 152000 {
		t.Errorf("sda1 = %+v", d)
	}
	if d := got["nvme0n1p1"]; d.SectorsRead != 3200 || d.SectorsWrite != 4000 {
		t.Errorf("nvme0n1p1 = %+v", d)
	}
	if _, err := ParseDiskstats([]byte("8 0 sda 1 2 notanumber 4 5 6 7 8\n")); err == nil {
		t.Error("garbage sector count accepted")
	}
}

func TestDiskstatsName(t *testing.T) {
	known, _ := ParseDiskstats([]byte(diskstatsFixture))
	dm := map[string]string{"vg0-root": "dm-0", "vg0-gone": "dm-9"}
	cases := []struct {
		dev  string
		want string
		ok   bool
	}{
		{"/dev/sda1", "sda1", true},
		{"/dev/nvme0n1p1", "nvme0n1p1", true},
		{"/dev/mapper/vg0-root", "dm-0", true},
		{"/dev/mapper/vg0-gone", "", false},   // mapped but absent from diskstats
		{"/dev/mapper/unknown", "", false},    // not in the dm map
		{"/dev/disk/by-uuid/abcd", "", false}, // a link we cannot resolve from the mount
		{"/dev/root", "", false},
		{"tank/data", "", false}, // ZFS dataset
		{"/dev/vda1", "", false}, // not in diskstats
	}
	for _, c := range cases {
		got, ok := DiskstatsName(c.dev, dm, known)
		if got != c.want || ok != c.ok {
			t.Errorf("DiskstatsName(%q) = %q,%v; want %q,%v", c.dev, got, ok, c.want, c.ok)
		}
	}
}

type sysFake struct {
	*fakeSource
	sys map[string]string
}

func (f sysFake) ReadSys(name string) ([]byte, error) {
	if s, ok := f.sys[name]; ok {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("no such file %s", name)
}

func TestSamplerDiskIORates(t *testing.T) {
	src := newFakeSource(t)
	src.set("diskstats", diskstatsFixture)
	src.set("mounts", "/dev/sda1 / ext4 rw 0 0\n")
	s, clk := newSampler(t, src, 10)
	ctx := context.Background()

	first, err := s.Sample(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Disks) != 1 || !first.Disks[0].IO {
		t.Fatalf("disks = %+v, want sda1 with IO", first.Disks)
	}
	if first.Disks[0].ReadBytesPerSec != 0 {
		t.Errorf("first sample has a rate: %+v", first.Disks[0])
	}

	// 10 s later sda1 read 1000 more sectors and wrote 2000 more.
	clk.advance(10 * time.Second)
	src.set("diskstats", "   8       1 sda1 900 10 73000 450 1900 20 154000 850 0 700 1300\n")
	second, err := s.Sample(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := second.Disks[0]
	if want := 1000.0 * SectorBytes / 10; d.ReadBytesPerSec != want {
		t.Errorf("read rate = %v, want %v", d.ReadBytesPerSec, want)
	}
	if want := 2000.0 * SectorBytes / 10; d.WriteBytesPerSec != want {
		t.Errorf("write rate = %v, want %v", d.WriteBytesPerSec, want)
	}

	// A counter that went backwards (device reset) is 0, not a huge number.
	clk.advance(10 * time.Second)
	src.set("diskstats", "   8       1 sda1 1 0 5 0 1 0 5 0 0 0 0\n")
	third, _ := s.Sample(ctx)
	if d := third.Disks[0]; d.ReadBytesPerSec != 0 || d.WriteBytesPerSec != 0 {
		t.Errorf("counter wrap gave %+v", d)
	}
}

func TestSamplerWithoutDiskstatsStillSamples(t *testing.T) {
	src := newFakeSource(t) // serves no diskstats
	s, _ := newSampler(t, src, 10)
	snap, err := s.Sample(context.Background())
	if err != nil {
		t.Fatalf("sample failed without diskstats: %v", err)
	}
	for _, d := range snap.Disks {
		if d.IO {
			t.Errorf("%s claims IO without diskstats", d.Mount)
		}
	}
}

func TestSamplerResolvesDeviceMapper(t *testing.T) {
	base := newFakeSource(t)
	base.set("diskstats", diskstatsFixture)
	base.set("mounts", "/dev/mapper/vg0-root / ext4 rw 0 0\n")
	src := sysFake{fakeSource: base, sys: map[string]string{"block/dm-0/dm/name": "vg0-root\n"}}
	s, _ := newSampler(t, src, 10)
	snap, err := s.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Disks) != 1 || !snap.Disks[0].IO {
		t.Fatalf("lvm root not mapped: %+v", snap.Disks)
	}
}
