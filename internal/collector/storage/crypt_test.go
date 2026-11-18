package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/sysfs"
)

// cryptHost builds a small sysfs: / on LUKS2 over md0 (sda1), the EFI
// partition sda2, an unencrypted data disk sdb with a swap file, and sdc1,
// a LUKS partition that is not opened.
func cryptHost(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	put := func(p, s string) {
		t.Helper()
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(p, target string) {
		t.Helper()
		p = filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	blk := "sys/devices/pci0000:00/0000:00:17.0/block/"
	virt := "sys/devices/virtual/block/"
	put(blk+"sda/dev", "8:0")
	put(blk+"sda/sda1/partition", "1")
	put(blk+"sda/sda1/dev", "8:1")
	put(blk+"sda/sda1/holders/md0", "")
	put(blk+"sda/sda2/partition", "2")
	put(blk+"sda/sda2/dev", "8:2")
	put(blk+"sdb/dev", "8:16")
	put(blk+"sdc/dev", "8:32")
	put(blk+"sdc/sdc1/partition", "1")
	put(blk+"sdc/sdc1/dev", "8:33")
	put(virt+"md0/dev", "9:0")
	put(virt+"md0/slaves/sda1", "")
	put(virt+"md0/holders/dm-0", "")
	put(virt+"dm-0/dev", "253:0")
	put(virt+"dm-0/dm/name", "cryptroot")
	put(virt+"dm-0/dm/uuid", "CRYPT-LUKS2-0b1c2d3eaaaabbbbcccc0123456789ab-cryptroot")
	put(virt+"dm-0/slaves/md0", "")
	for n, dir := range map[string]string{"sda": blk + "sda", "sda1": blk + "sda/sda1", "sda2": blk + "sda/sda2", "sdb": blk + "sdb",
		"sdc": blk + "sdc", "sdc1": blk + "sdc/sdc1", "md0": virt + "md0", "dm-0": virt + "dm-0"} {
		link("sys/class/block/"+n, "../../../"+dir)
		put("dev/"+n, "")
	}
	link("sys/block/dm-0", "../devices/virtual/block/dm-0")
	link("dev/mapper/cryptroot", "../dm-0")
	put("run/udev/data/b8:2", "E:ID_PART_ENTRY_TYPE=C12A7328-F81F-11D2-BA4B-00A0C93EC93B\nE:ID_FS_TYPE=vfat\n")
	put("run/udev/data/b8:16", "E:ID_FS_TYPE=ext4\n")
	put("run/udev/data/b8:33", "E:ID_FS_TYPE=crypto_LUKS\n")
	put("proc/swaps", "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n/data/swapfile                          file\t\t4194300\t\t0\t\t-2\n")
	put("sys/fs/ext4/sdb/errors_count", "3")
	put("sys/fs/ext4/sdb/last_error_time", "1795003065")
	put("sys/fs/ext4/dm-0/errors_count", "0")
	return root
}

func TestEncryption(t *testing.T) {
	opts := collector.DefaultOptions()
	opts.RequireEncryption = true
	env := &collector.Env{FS: hostfs.New(cryptHost(t)), Now: time.Now, Opts: opts}
	mounts := []sysfs.Mount{
		{Mountpoint: "/", Source: "/dev/mapper/cryptroot", FSType: "ext4", MajMin: "253:0", Root: "/"},
		{Mountpoint: "/boot/efi", Source: "/dev/sda2", FSType: "vfat", MajMin: "8:2", Root: "/"},
		{Mountpoint: "/data", Source: "/dev/sdb", FSType: "ext4", MajMin: "8:16", Root: "/"},
	}
	out := &model.Storage{
		Disks: []model.Disk{{Name: "sda", Model: "A", Mounted: []string{"/boot/efi"}}, {Name: "sdb", Model: "B", Mounted: []string{"/data"}},
			{Name: "sdc", Model: "C", SizeBytes: 4e12}},
		Filesystems: []model.Filesystem{{Mountpoint: "/", Source: "/dev/mapper/cryptroot", FSType: "ext4"},
			{Mountpoint: "/boot/efi", Source: "/dev/sda2", FSType: "vfat"}, {Mountpoint: "/data", Source: "/dev/sdb", FSType: "ext4"}},
	}
	h := &out.Header
	c := New()
	c.encryption(context.Background(), env, out, h, mounts, nil)
	fsHealth(env.FS, out, h, mounts)

	if len(out.Crypt) != 1 || out.Crypt[0].Name != "cryptroot" || out.Crypt[0].Type != "LUKS2" || out.Crypt[0].Backing[0] != "md0" ||
		len(out.Crypt[0].UsedBy) != 1 || out.Crypt[0].UsedBy[0] != "/" {
		t.Errorf("crypt %+v", out.Crypt)
	}
	fs := map[string]*model.Filesystem{}
	for i := range out.Filesystems {
		fs[out.Filesystems[i].Mountpoint] = &out.Filesystems[i]
	}
	if e := fs["/"].Encryption; e == nil || !e.Protected || e.Method != "LUKS2" || e.Via != "dm-0 (cryptroot)" {
		t.Errorf("/ %+v", e)
	}
	if e := fs["/data"].Encryption; e == nil || e.Protected {
		t.Errorf("/data %+v", e)
	}
	dk := map[string]*model.Disk{}
	for i := range out.Disks {
		dk[out.Disks[i].Name] = &out.Disks[i]
	}
	if e := dk["sda"].Encryption; e == nil || !e.Protected || e.Via != "sda1 → dm-0" {
		t.Errorf("sda (EFI partition left out, sda1 via md0) %+v", e)
	}
	if e := dk["sdb"].Encryption; e == nil || e.Protected {
		t.Errorf("sdb %+v", e)
	}
	if e := dk["sdc"].Encryption; e == nil || !e.Protected || e.Method != "crypto_LUKS" || e.Via != "sdc1 (not opened)" {
		t.Errorf("sdc (closed LUKS) %+v", e)
	}
	if len(out.Swap) != 1 || out.Swap[0].Type != "file" || out.Swap[0].Encryption == nil || out.Swap[0].Encryption.Protected {
		t.Errorf("swap %+v", out.Swap)
	}
	got := map[string]string{}
	for _, f := range out.Findings {
		got[f.Code+" "+f.Subject] = f.Message
	}
	for _, want := range []string{"not_encrypted /data", "swap_not_encrypted /data/swapfile", "fs_errors /data"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing finding %s in %v", want, got)
		}
	}
	for k := range got {
		if strings.Contains(k, "/boot/efi") || k == "not_encrypted /" || strings.HasPrefix(k, "not_encrypted sd") {
			t.Errorf("unexpected finding %s", k)
		}
	}
	if fs["/data"].Health == nil || *fs["/data"].Health.Errors != 3 || fs["/"].Health == nil || *fs["/"].Health.Errors != 0 {
		t.Errorf("health / %+v, /data %+v", fs["/"].Health, fs["/data"].Health)
	}

	// without the requirement: shown, no finding
	out2 := &model.Storage{Filesystems: []model.Filesystem{{Mountpoint: "/data", Source: "/dev/sdb", FSType: "ext4"}}}
	env.Opts.RequireEncryption = false
	c.encryption(context.Background(), env, out2, &out2.Header, mounts, nil)
	if len(out2.Findings) != 0 || out2.Filesystems[0].Encryption == nil {
		t.Errorf("not required: %+v %+v", out2.Findings, out2.Filesystems[0].Encryption)
	}
}
