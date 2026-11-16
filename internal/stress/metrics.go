package stress

import "strings"

// MetricTitle names a metric for people: "cpu_temp" → "Hottest CPU °C".
func MetricTitle(name string) string {
	switch name {
	case "cpu_load":
		return "CPU load %"
	case "cpu_temp":
		return "Hottest CPU °C"
	case "cpu_freq":
		return "Frequency MHz (avg)"
	case "mem_used":
		return "Memory used %"
	case "disk_read":
		return "Disk read"
	case "disk_write":
		return "Disk write"
	case "net_rx":
		return "NIC rx"
	case "net_tx":
		return "NIC tx"
	case "throttle":
		return "Throttle events"
	case "verified_kernels_per_s":
		return "Verified kernels/s"
	case "verified_bytes_per_s":
		return "Memory verified/s"
	case "net_test_bits_per_s":
		return "Test stream"
	case "file_write_bytes_per_s":
		return "File write"
	case "file_read_bytes_per_s":
		return "File read"
	case "tcp_retransmits":
		return "TCP retransmits"
	case "net_link_ratio":
		return "Share of link speed %"
	case "memtester_loops":
		return "memtester loops"
	case "mprime_self_tests_passed":
		return "mprime self-tests"
	}
	for _, p := range []struct{ pre, title string }{{"read_bytes_per_s_", "Read "}, {"read_max_ms_", "Slowest read ms "},
		{"slow_reads_", "Slow reads "}, {"bogo_ops_per_s_", "Bogo ops/s "}, {"gpu_load_", "GPU load % "}} {
		if strings.HasPrefix(name, p.pre) {
			return p.title + strings.TrimPrefix(name, p.pre)
		}
	}
	return strings.ReplaceAll(name, "_", " ")
}
