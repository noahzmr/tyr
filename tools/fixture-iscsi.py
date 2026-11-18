#!/usr/bin/env python3
"""Adds iSCSI details to server-degraded and builds the k8s-longhorn fixture
from server-healthy. Run from the repository root: python3 tools/fixture-iscsi.py"""
import json, os, shutil, datetime as dt

FX = "testdata/fixtures"
NOW = dt.datetime(2026, 11, 16, 9, 0, tzinfo=dt.timezone.utc)
BOOT = NOW - dt.timedelta(days=9)

JOURNAL_ARGV = ["journalctl", "-o", "json", "--no-pager", "-q", "--since=-24h", "-n", "5000", "-g",
                "iscsi|longhorn|multipath|mountvolume|mountdevice|attachvolume|unmountvolume|unmountdevice|mapvolume|connection[0-9]+:[0-9]+|nvme|already mounted|mount point busy|error|fail",
                "SYSLOG_IDENTIFIER=iscsid", "+", "SYSLOG_IDENTIFIER=multipathd", "+", "SYSLOG_IDENTIFIER=kubelet", "+",
                "SYSLOG_IDENTIFIER=k3s", "+", "SYSLOG_IDENTIFIER=rke2"]


class Fx:
    def __init__(self, name):
        self.dir = os.path.join(FX, name)
        self.man = json.load(open(os.path.join(self.dir, "manifest.json")))

    def p(self, path):
        return os.path.join(self.dir, "root", path.lstrip("/"))

    def write(self, path, content, append=False):
        full = self.p(path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, "a" if append else "w") as f:
            f.write(content)

    def symlink(self, path, target):
        full = self.p(path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        if os.path.lexists(full):
            os.remove(full)
        os.symlink(target, full)

    def cmd(self, argv, out, name=None):
        name = name or ("_".join(argv).replace("/", "_").replace(" ", "_").replace("|", "_") + ".txt")
        rel = os.path.join("cmd", name)
        with open(os.path.join(self.dir, rel), "w") as f:
            f.write(out)
        self.man["commands"] = [c for c in self.man["commands"] if c["argv"] != argv]
        self.man["commands"].append({"argv": argv, "stdout": rel, "exit": 0})

    def kmsg(self, lines):
        """lines: (datetime, priority, message), appended after the existing records."""
        path = self.p("/dev/kmsg")
        have = {l.split(";", 1)[1].rstrip("\n") for l in open(path) if ";" in l}
        seq = sum(1 for _ in open(path)) + 1000
        with open(path, "a") as f:
            for t, pri, msg in lines:
                if msg in have:  # added by an earlier run
                    continue
                us = int((t - BOOT).total_seconds() * 1e6)
                f.write(f"{pri},{seq},{us},-;{msg}\n")
                seq += 1

    def proc(self, pid, *argv):
        self.write(f"/proc/{pid}/cmdline", "\0".join(argv) + "\0")

    def save(self):
        with open(os.path.join(self.dir, "manifest.json"), "w") as f:
            json.dump(self.man, f, indent=2, sort_keys=False)
            f.write("\n")


def journal(entries):
    out = []
    for t, ident, pri, msg in sorted(entries):
        out.append(json.dumps({"__REALTIME_TIMESTAMP": str(int(t.timestamp() * 1e6)), "SYSLOG_IDENTIFIER": ident,
                               "PRIORITY": str(pri), "MESSAGE": msg, "_SYSTEMD_UNIT": ident + ".service"}))
    return "\n".join(out) + "\n"


def lun(fx, host, sess, disk, size_gb, iqn, portal, state="LOGGED_IN", conn="up", local="", ro_disk=False):
    """An iSCSI session with one LUN (disk="" : a session without disk)."""
    sdir = f"/sys/devices/platform/host{host}/session{sess}"
    fx.write(f"{sdir}/iscsi_session/session{sess}/targetname", iqn + "\n")
    fx.write(f"{sdir}/iscsi_session/session{sess}/state", state + "\n")
    fx.write(f"{sdir}/iscsi_session/session{sess}/recovery_tmo", "120\n")
    fx.symlink(f"{sdir}/iscsi_session/session{sess}/device", "../../../session%d" % sess)
    fx.symlink(f"/sys/class/iscsi_session/session{sess}", f"../../devices/platform/host{host}/session{sess}/iscsi_session/session{sess}")
    cdir = f"{sdir}/connection{sess}:0/iscsi_connection/connection{sess}:0"
    addr, port = portal.rsplit(":", 1)
    for k, v in {"persistent_address": addr, "persistent_port": port, "address": addr, "port": port, "state": conn,
                 "header_digest": "None", "data_digest": "None"}.items():
        fx.write(f"{cdir}/{k}", v + "\n")
    fx.symlink(f"/sys/class/iscsi_connection/connection{sess}:0", f"../../devices/platform/host{host}/session{sess}/connection{sess}:0/iscsi_connection/connection{sess}:0")
    fx.write(f"/sys/devices/platform/host{host}/iscsi_host/host{host}/ipaddress", (local or "<NULL>") + "\n")
    fx.write(f"/sys/devices/platform/host{host}/iscsi_host/host{host}/netdev", "<NULL>\n")
    fx.symlink(f"/sys/class/iscsi_host/host{host}", f"../../devices/platform/host{host}/iscsi_host/host{host}")
    if not disk:
        return
    scsi = f"{sdir}/target{host}:0:0/{host}:0:0:1"
    for k, v in {"vendor": "IET     ", "model": "VIRTUAL-DISK    ", "rev": "0001", "ioerr_cnt": "0x0",
                 "wwid": f"t10.IET     VIRTUAL-DISK    000{host}0001"}.items():
        fx.write(f"{scsi}/{k}", v + "\n")
    blk = f"{scsi}/block/{disk}"
    fx.write(f"{blk}/size", "%d\n" % (size_gb * 1000**3 // 512))
    fx.write(f"{blk}/ro", "0\n")
    fx.write(f"{blk}/removable", "0\n")
    fx.write(f"{blk}/queue/rotational", "0\n")
    fx.symlink(f"{blk}/device", "../../../%d:0:0:1" % host)
    fx.symlink(f"/sys/block/{disk}", f"../devices/platform/host{host}/session{sess}/target{host}:0:0/{host}:0:0:1/block/{disk}")


def degraded():
    fx = Fx("server-degraded")
    # session1 already exists as plain directories: replace them by the kernel's layout
    for d in ["/sys/class/iscsi_session/session1", "/sys/class/iscsi_connection/connection1:0"]:
        shutil.rmtree(fx.p(d), ignore_errors=True)
    sdir = "/sys/devices/platform/host9/session1"
    fx.write(f"{sdir}/iscsi_session/session1/targetname", "iqn.2024-01.net.example:backup.vol2\n")
    fx.write(f"{sdir}/iscsi_session/session1/state", "FAILED\n")
    fx.write(f"{sdir}/iscsi_session/session1/recovery_tmo", "120\n")
    fx.symlink(f"{sdir}/iscsi_session/session1/device", "../../../session1")
    fx.symlink("/sys/class/iscsi_session/session1", "../../devices/platform/host9/session1/iscsi_session/session1")
    cdir = f"{sdir}/connection1:0/iscsi_connection/connection1:0"
    for k, v in {"persistent_address": "10.16.40.20", "persistent_port": "3260", "state": "failed", "header_digest": "None", "data_digest": "None"}.items():
        fx.write(f"{cdir}/{k}", v + "\n")
    fx.symlink("/sys/class/iscsi_connection/connection1:0", "../../devices/platform/host9/session1/connection1:0/iscsi_connection/connection1:0")
    fx.write("/sys/devices/platform/host9/iscsi_host/host9/ipaddress", "10.20.0.21\n")
    fx.write("/sys/devices/platform/host9/iscsi_host/host9/netdev", "<NULL>\n")
    fx.symlink("/sys/class/iscsi_host/host9", "../../devices/platform/host9/iscsi_host/host9")
    fx.write("/sys/module/iscsi_tcp/refcnt", "2\n")
    fx.write("/etc/iscsi/initiatorname.iscsi", "## DO NOT EDIT OR REMOVE THIS FILE!\nInitiatorName=iqn.1993-08.org.debian:01:5a3c9e1f2b7d\n")
    fx.proc(812, "/usr/sbin/iscsid")
    fx.man["tools"]["iscsiadm"] = "/usr/sbin/iscsiadm"
    t0 = BOOT + dt.timedelta(days=8, hours=4, minutes=30)
    fx.kmsg([
        (BOOT + dt.timedelta(seconds=41), 6, "scsi host9: iSCSI Initiator over TCP/IP"),
        (BOOT + dt.timedelta(seconds=42), 5, "sd 9:0:0:1: [sdf] Attached SCSI disk"),
        (t0 - dt.timedelta(seconds=6), 3, " connection1:0: ping timeout of 5 secs expired, recv timeout 5, last rx 4339172480, last ping 4339173760, now 4339175040"),
        (t0 + dt.timedelta(seconds=120), 3, " session1: session recovery timed out after 120 secs"),
        (t0 + dt.timedelta(seconds=121), 3, "sd 9:0:0:1: rejecting I/O to offline device"),
    ])
    fx.cmd(JOURNAL_ARGV, journal([
        (t0, "iscsid", 3, "Kernel reported iSCSI connection 1:0 error (1020 - ISCSI_ERR_TCP_CONN_CLOSE: TCP connection closed) state (3)"),
        (t0 + dt.timedelta(seconds=30), "iscsid", 3, "connect to 10.16.40.20:3260 failed (No route to host)"),
        (t0 + dt.timedelta(hours=3), "iscsid", 3, "connect to 10.16.40.20:3260 failed (No route to host)"),
        (NOW - dt.timedelta(minutes=5), "iscsid", 3, "connect to 10.16.40.20:3260 failed (No route to host)"),
    ]), name="journalctl_node.txt")
    fx.cmd(["ip", "-j", "route", "get", "10.16.40.20"],
           '[{"dst":"10.16.40.20","gateway":"10.20.0.1","dev":"bond0.100","prefsrc":"10.20.0.21","flags":[],"uid":0,"cache":[]}]\n')
    fx.save()


PVC = {
    "a": "pvc-3f8e2a10-6c1d-4b8e-9f3a-1c2d3e4f5a6b",  # flapping session
    "b": "pvc-7b2c9d4e-1a3f-4c5e-8d7b-2e3f4a5b6c7d",  # timed out, filesystem read-only
    "c": "pvc-c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f",  # orphaned session, kubelet cannot mount
    "d": "pvc-d4e5f6a7-b8c9-4d0e-9f1a-2b3c4d5e6f70",  # replica in error
    "e": "pvc-e1e2e3e4-f5f6-4a7b-8c9d-0e1f2a3b4c5d",  # detached earlier: a gone session
}


def longhorn():
    dst = os.path.join(FX, "k8s-longhorn")
    shutil.rmtree(dst, ignore_errors=True)
    shutil.copytree(os.path.join(FX, "server-healthy"), dst, symlinks=True)
    fx = Fx("k8s-longhorn")
    fx.man["description"] = ("synthetic: server-healthy as a k3s node with Longhorn (v1 engine): a flapping volume session, a volume whose "
                             "filesystem went read-only after a recovery timeout, an orphaned session, multipathd without a blacklist, "
                             "a failed replica and the kubelet failing to mount a volume")
    fx.man["hostname"] = "k8s-node-03"
    fx.write("/proc/sys/kernel/hostname", "k8s-node-03\n")
    fx.man["tools"].update({"iscsiadm": "/usr/sbin/iscsiadm", "multipath": "/usr/sbin/multipath"})
    # sessions: a (sdg), b (sdh), c (orphan, FAILED)
    lun(fx, 10, 2, "sdg", 21, "iqn.2019-10.io.longhorn:" + PVC["a"], "10.42.3.17:3260", local="10.42.3.1")
    lun(fx, 11, 3, "sdh", 53, "iqn.2019-10.io.longhorn:" + PVC["b"], "10.42.3.17:3260", local="10.42.3.1")
    lun(fx, 12, 4, "", 0, "iqn.2019-10.io.longhorn:" + PVC["c"], "10.42.3.21:3260", state="FAILED", conn="failed", local="10.42.3.1")
    for m in ["iscsi_tcp", "dm_multipath", "dm_crypt"]:
        fx.write(f"/sys/module/{m}/refcnt", "1\n")
    fx.write("/sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages", "0\n")
    fx.write("/etc/iscsi/initiatorname.iscsi", "InitiatorName=iqn.2004-10.com.ubuntu:01:8e1f4c2d9a7b\n")
    fx.write("/etc/multipath.conf", "defaults {\n    user_friendly_names yes\n}\n")
    fx.write("/usr/sbin/mount.nfs4", "")
    fx.write("/var/lib/rancher/k3s/agent/.keep", "")
    for v in ["a", "b"]:
        fx.write(f"/dev/longhorn/{PVC[v]}", "")
    # data path and replicas
    fx.write("/var/lib/longhorn/longhorn-disk.cfg", '{"diskName":"default-disk-5c0a","diskUUID":"6e0c2a4f-93b1-4d2e-8f7a-0b1c2d3e4f50","diskDriver":""}\n')
    meta = lambda size, rebuilding=False, err="": json.dumps({"Size": size, "Head": "volume-head-003.img", "Dirty": True, "Rebuilding": rebuilding,
                                                             "Error": err, "Parent": "volume-snap-c0ffee.img", "SectorSize": 512, "BackingFilePath": ""})
    fx.write(f"/var/lib/longhorn/replicas/{PVC['a']}-1a2b3c4d/volume.meta", meta(21474836480))
    fx.write(f"/var/lib/longhorn/replicas/{PVC['b']}-5e6f7a8b/volume.meta", meta(53687091200, rebuilding=True))
    fx.write(f"/var/lib/longhorn/replicas/{PVC['d']}-9c0d1e2f/volume.meta", meta(10737418240, err="failed to write volume-head-003.img: input/output error"))
    # processes
    fx.proc(811, "/usr/sbin/iscsid")
    fx.proc(822, "/sbin/multipathd", "-d", "-s")
    fx.proc(1510, "/usr/local/bin/k3s", "agent")
    fx.proc(2301, "longhorn-manager", "-d", "daemon", "--engine-image", "longhornio/longhorn-engine:v1.10.1")
    fx.proc(2402, "longhorn-instance-manager", "--debug", "daemon", "--listen", "0.0.0.0:8500")
    fx.proc(2510, "longhorn", "replica", f"/host/var/lib/longhorn/replicas/{PVC['a']}-1a2b3c4d", "--size", "21474836480")
    fx.proc(2511, "longhorn", "controller", PVC["a"], "--frontend", "tgt-blockdev")
    # mounts: the kubelet's global and pod mounts of a and b; b went read-only
    mi = open(fx.p("/proc/self/mountinfo")).read()
    pod = "/var/lib/kubelet/pods/0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d/volumes/kubernetes.io~csi/{}/mount"
    glob = "/var/lib/kubelet/plugins/kubernetes.io/csi/driver.longhorn.io/{}/globalmount"
    mi += (f"60 22 8:96 / {glob.format('5d1e0c2b3a49f8e7d6c5b4a3928170615f4e3d2c1b0a99887766554433221100')} rw,relatime shared:40 - ext4 /dev/longhorn/{PVC['a']} rw\n"
           f"61 22 8:96 / {pod.format(PVC['a'])} rw,relatime shared:40 - ext4 /dev/longhorn/{PVC['a']} rw\n"
           f"62 22 8:112 / {glob.format('8f7e6d5c4b3a29180716253443526170819fa0b1c2d3e4f5061728394a5b6c7d')} rw,relatime shared:41 - ext4 /dev/longhorn/{PVC['b']} ro,relatime\n"
           f"63 22 8:112 / {pod.format(PVC['b'])} rw,relatime shared:41 - ext4 /dev/longhorn/{PVC['b']} ro,relatime\n")
    fx.write("/proc/self/mountinfo", mi)
    # kernel log
    h = lambda hours: NOW - dt.timedelta(hours=hours)
    lines = [
        (BOOT + dt.timedelta(minutes=10), 6, "scsi host9: iSCSI Initiator over TCP/IP"),
        (BOOT + dt.timedelta(minutes=10, seconds=1), 5, "sd 9:0:0:1: [sdf] Attached SCSI disk"),
        (BOOT + dt.timedelta(days=1), 5, "sd 9:0:0:1: [sdf] Synchronizing SCSI cache"),
        (h(30), 6, "scsi host10: iSCSI Initiator over TCP/IP"),
        (h(30) + dt.timedelta(seconds=1), 5, "sd 10:0:0:1: [sdg] Attached SCSI disk"),
        (h(30) + dt.timedelta(minutes=1), 6, "scsi host11: iSCSI Initiator over TCP/IP"),
        (h(30) + dt.timedelta(minutes=1, seconds=1), 5, "sd 11:0:0:1: [sdh] Attached SCSI disk"),
        (h(29), 6, "scsi host12: iSCSI Initiator over TCP/IP"),
    ]
    for hrs in (20, 11, 2):
        lines += [(h(hrs), 3, " connection2:0: ping timeout of 5 secs expired, recv timeout 5, last rx 4339172480, last ping 4339173760, now 4339175040"),
                  (h(hrs) + dt.timedelta(milliseconds=2), 3, " connection2:0: detected conn error (1022)")]
    t = h(6)
    lines += [(t, 3, " connection3:0: detected conn error (1020)"),
              (t + dt.timedelta(seconds=120), 3, " session3: session recovery timed out after 120 secs"),
              (t + dt.timedelta(seconds=121), 3, "sd 11:0:0:1: rejecting I/O to offline device"),
              (t + dt.timedelta(seconds=121, milliseconds=5), 3, "I/O error, dev sdh, sector 2048 op 0x1:(WRITE) flags 0x800 phys_seg 1 prio class 0"),
              (t + dt.timedelta(seconds=122), 2, "EXT4-fs error (device sdh): ext4_journal_check_start:83: comm kworker/u64:2: Detected aborted journal"),
              (t + dt.timedelta(seconds=122, milliseconds=1), 2, "EXT4-fs (sdh): Remounting filesystem read-only"),
              (h(3), 3, " connection4:0: detected conn error (1020)")]
    fx.kmsg(lines)
    # journal: iscsid, multipathd, k3s's kubelet
    j = [(BOOT + dt.timedelta(minutes=10), "iscsid", 6, f"Connection1:0 to [target: iqn.2019-10.io.longhorn:{PVC['e']}, portal: 10.42.3.9,3260] through [iface: default] is operational now"),
         (h(30) + dt.timedelta(seconds=1), "iscsid", 6, f"Connection2:0 to [target: iqn.2019-10.io.longhorn:{PVC['a']}, portal: 10.42.3.17,3260] through [iface: default] is operational now"),
         (h(30) + dt.timedelta(minutes=1, seconds=1), "iscsid", 6, f"Connection3:0 to [target: iqn.2019-10.io.longhorn:{PVC['b']}, portal: 10.42.3.17,3260] through [iface: default] is operational now"),
         (h(29) + dt.timedelta(seconds=1), "iscsid", 6, f"Connection4:0 to [target: iqn.2019-10.io.longhorn:{PVC['c']}, portal: 10.42.3.21,3260] through [iface: default] is operational now")]
    for hrs in (20, 11, 2):
        j += [(h(hrs) + dt.timedelta(seconds=1), "iscsid", 3, "Kernel reported iSCSI connection 2:0 error (1022 - ISCSI_ERR_NOP_TIMEDOUT: A NOP has timed out) state (3)"),
              (h(hrs) + dt.timedelta(seconds=9), "iscsid", 5, "connection2:0 is operational after recovery (1 attempts)")]
    j += [(t + dt.timedelta(seconds=1), "iscsid", 3, "Kernel reported iSCSI connection 3:0 error (1020 - ISCSI_ERR_TCP_CONN_CLOSE: TCP connection closed) state (3)"),
          (t + dt.timedelta(minutes=3), "iscsid", 5, "connection3:0 is operational after recovery (14 attempts)")]
    for mins in range(170, 0, -20):
        j.append((NOW - dt.timedelta(minutes=mins), "iscsid", 3, "connect to 10.42.3.21:3260 failed (Connection refused)"))
    for mins in range(150, 0, -30):
        ts = (NOW - dt.timedelta(minutes=mins))
        j.append((ts, "k3s", 3, ts.strftime("E%m%d %H:%M:%S.123456") + f'    1510 nestedpendingoperations.go:348] Operation for "{{volumeName:kubernetes.io/csi/driver.longhorn.io^{PVC["c"]} podName: nodeName:}}" failed. No retries permitted until '
                  + (ts + dt.timedelta(minutes=2)).strftime("%Y-%m-%d %H:%M:%S") + f' +0000 UTC m=+780000.1 (durationBeforeRetry 2m2s). Error: MountVolume.MountDevice failed for volume "{PVC["c"]}" (UniqueName: "kubernetes.io/csi/driver.longhorn.io^{PVC["c"]}") pod "postgres-0" (UID: "5b6c7d8e-9f0a-4b1c-8d2e-3f4a5b6c7d8e") : rpc error: code = Internal desc = mount failed: exit status 32\nmount: /var/lib/kubelet/plugins/kubernetes.io/csi/driver.longhorn.io/aa11/globalmount: /dev/longhorn/{PVC["c"]} already mounted or mount point busy.'))
    j.append((h(1), "multipathd", 6, "sdh: add path (uevent)"))
    fx.cmd(JOURNAL_ARGV, journal(j), name="journalctl_node.txt")
    for addr in ["10.42.3.17", "10.42.3.21"]:
        fx.cmd(["ip", "-j", "route", "get", addr], f'[{{"dst":"{addr}","dev":"cni0","prefsrc":"10.42.3.1","flags":[],"uid":0,"cache":[]}}]\n')
    # Longhorn pods' logs (CRI format)
    def cri(ts, msg):
        return ts.strftime("%Y-%m-%dT%H:%M:%S.") + "123456789Z stderr F " + msg + "\n"
    im = "/var/log/pods/longhorn-system_instance-manager-5f7c1d2e3a4b_9a8b7c6d-5e4f-4a3b-2c1d-0e9f8a7b6c5d/instance-manager/0.log"
    s = ""
    for k in range(4):
        ts = t + dt.timedelta(seconds=20 * k)
        s += cri(ts, f'time="{ts:%Y-%m-%dT%H:%M:%SZ}" level=error msg="Failed to connect to replica" func="controller.(*Controller).monitoring" file="control.go:917" error="dial tcp 10.42.1.12:10000: connect: connection refused" volume={PVC["b"]}')
    s += cri(t + dt.timedelta(minutes=5), f'time="{t + dt.timedelta(minutes=5):%Y-%m-%dT%H:%M:%SZ}" level=info msg="Replica rebuild started" volume={PVC["b"]}')
    old = NOW - dt.timedelta(days=3)
    s = cri(old, f'time="{old:%Y-%m-%dT%H:%M:%SZ}" level=error msg="an error three days old, outside the 24 h window"') + s
    fx.write(im, s)
    mgr = "/var/log/pods/longhorn-system_longhorn-manager-7k2xq_1c2d3e4f-5a6b-4c7d-8e9f-0a1b2c3d4e5f/longhorn-manager/0.log"
    ts = h(4)
    fx.write(mgr, cri(ts, f'time="{ts:%Y-%m-%dT%H:%M:%SZ}" level=warning msg="Volume is degraded" volume={PVC["b"]}')
             + cri(ts + dt.timedelta(minutes=1), f'time="{ts:%Y-%m-%dT%H:%M:%SZ}" level=error msg="Failed to sync Longhorn volume longhorn-system/{PVC["d"]}" controller=longhorn-volume error="replica {PVC["d"]}-r-0c1d2e3f is in error state"'))
    fx.save()


if __name__ == "__main__":
    degraded()
    longhorn()
    print("ok")
