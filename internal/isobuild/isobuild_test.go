package isobuild

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSys builds /sys/block and mountinfo: sdb a USB stick (mounted on the
// desktop), sdc a USB disk the system runs from, sda an internal SATA disk.
func fakeSys(t *testing.T) string {
	root := t.TempDir()
	dev := func(name, bus string, size string, devno string) {
		real := filepath.Join(root, "sys", "devices", "pci0000:00", bus, "host0", "target0:0:0", "0:0:0:0", "block", name)
		os.MkdirAll(filepath.Join(real, "device"), 0o755)
		os.WriteFile(filepath.Join(real, "size"), []byte(size+"\n"), 0o644)
		os.WriteFile(filepath.Join(real, "dev"), []byte(devno+"\n"), 0o644)
		os.WriteFile(filepath.Join(real, "device", "vendor"), []byte("SanDisk \n"), 0o644)
		os.WriteFile(filepath.Join(real, "device", "model"), []byte("Ultra           \n"), 0o644)
		os.MkdirAll(filepath.Join(real, name+"1"), 0o755)
		maj, min, _ := strings.Cut(devno, ":")
		os.WriteFile(filepath.Join(real, name+"1", "dev"), []byte(maj+":"+min+"1\n"), 0o644)
		os.MkdirAll(filepath.Join(root, "sys", "block"), 0o755)
		os.Symlink(real, filepath.Join(root, "sys", "block", name))
	}
	dev("sda", "0000:00:17.0/ata1", "1000215216", "8:0")
	dev("sdb", "0000:00:14.0/usb1/1-2/1-2:1.0", "30031872", "8:16")
	dev("sdc", "0000:00:14.0/usb1/1-3/1-3:1.0", "62521344", "8:32")
	os.WriteFile(filepath.Join(root, "sys", "devices", "pci0000:00", "0000:00:14.0", "usb1", "1-2", "serial"), []byte("4C530001\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "proc", "self"), 0o755)
	os.WriteFile(filepath.Join(root, "proc", "self", "mountinfo"), []byte(
		"22 1 8:321 / / rw,relatime - ext4 /dev/sdc1 rw\n"+
			"40 22 8:161 / /run/media/me/TYR\\040LIVE ro - iso9660 /dev/sdb1 ro\n"), 0o644)
	return root
}

func TestUSBDisks(t *testing.T) {
	ds, err := USBDisks(fakeSys(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 2 {
		t.Fatalf("%d disks (the SATA disk must not be listed): %+v", len(ds), ds)
	}
	b, c := ds[0], ds[1]
	if b.Name != "sdb" || b.Label() != "SanDisk Ultra · 15.4 GB" || b.Serial != "4C530001" || b.Why != "" ||
		len(b.Mounts) != 1 || b.Mounts[0] != "/run/media/me/TYR LIVE" {
		t.Errorf("sdb %+v", b)
	}
	if c.Name != "sdc" || !c.System || c.Why == "" {
		t.Errorf("sdc (system) %+v", c)
	}
	if err := Write(context.Background(), "/dev/null", c, func(string, int64, int64) {}); err == nil {
		t.Error("wrote to the system disk")
	}
}

func TestWriteVerify(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "tyr.iso")
	data := make([]byte, 9<<20+123) // not a multiple of the buffer
	rand.Read(data)
	os.WriteFile(img, data, 0o644)
	target := filepath.Join(dir, "stick")
	os.WriteFile(target, make([]byte, 16<<20), 0o644)
	var stages []string
	last := map[string]int64{}
	err := Write(context.Background(), img, Disk{Dev: target, SizeBytes: 16 << 20}, func(stage string, done, total int64) {
		if len(stages) == 0 || stages[len(stages)-1] != stage {
			stages = append(stages, stage)
		}
		last[stage] = done
		if total != int64(len(data)) {
			t.Errorf("total %d", total)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got[:len(data)], data) || strings.Join(stages, ",") != "write,verify" || last["verify"] != int64(len(data)) {
		t.Errorf("stages %v last %v", stages, last)
	}
	// too small
	if err := Write(context.Background(), img, Disk{Dev: target, SizeBytes: 1 << 20}, func(string, int64, int64) {}); err == nil {
		t.Error("an image larger than the stick was written")
	}
	// cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Write(ctx, img, Disk{Dev: target, SizeBytes: 16 << 20}, func(string, int64, int64) {}); err == nil {
		t.Error("cancelled write succeeded")
	}
}

func TestSettingsAndPhases(t *testing.T) {
	dir, err := WriteSettings(Settings{NetBox: map[string]string{"url": "https://nb", "token": "secret"}, Webhooks: map[string]any{"webhooks": []any{}}})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", fi.Mode())
	}
	for _, f := range []string{"netbox.json", "webhooks.json"} {
		if fi, err := os.Stat(filepath.Join(dir, f)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", f, fi, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "tags.json")); err == nil {
		t.Error("tags.json written without tags")
	}
	for line, want := range map[string]int{"== tyr binary (linux/amd64)": 0, "== root filesystem (Debian trixie, amd64)": 3,
		"== boot loader": 5, "I: running --customize-hook": -1} {
		if got := Phase(line); got != want {
			t.Errorf("Phase(%q) = %d, want %d", line, got, want)
		}
	}
	if m := doneRE.FindStringSubmatch("== /src/dist/tyr-live-e9fe8f3-amd64.iso (417M)"); m == nil || m[2] != "dist/tyr-live-e9fe8f3-amd64.iso" {
		t.Errorf("done line: %v", m)
	}
	t.Setenv("SUDO_USER", "")
	cmd := BuildCommand("/src/tyr", "amd64", "/usr/local/go/bin/go", []string{"TYR_ISO_CONFIG=/tmp/x y"})
	if s := strings.Join(cmd.Args, " "); !strings.Contains(s, `export PATH='/usr/local/go/bin':"$PATH"; cd '/src/tyr' && exec env 'TYR_ISO_CONFIG=/tmp/x y' iso/build.sh 'amd64'`) {
		t.Errorf("command %s", s)
	}
	if FindGo() == "" {
		t.Error("no Go found (the tests run with it)")
	}
	if !IsSource("../..") || IsSource(t.TempDir()) {
		t.Error("IsSource")
	}
}
