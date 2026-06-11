package kata

import (
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const runtimeSource = "kata runtime dir"

// Discover enumerates Kata's runtime state files and returns the first live
// daemon, preferring http(s) addresses over unix sockets.
func Discover() *Discovered {
	records := AliveRuntimeRecords()
	for _, r := range records {
		if u := runtimeRecordAddressURL(r); strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
			return &Discovered{URL: u, Source: runtimeSource}
		}
	}
	for _, r := range records {
		if u := runtimeRecordAddressURL(r); strings.HasPrefix(u, "unix://") {
			return &Discovered{URL: u, Source: runtimeSource}
		}
	}
	return nil
}

// RuntimeAddressURL converts a Kata runtime-record Address into a URL
// middleman can proxy to. Kata writes "unix:///path" for a Unix socket, or a
// bare "host:port" for a TCP daemon with no scheme.
func RuntimeAddressURL(addr string) string {
	if strings.HasPrefix(addr, "unix://") ||
		strings.HasPrefix(addr, "http://") ||
		strings.HasPrefix(addr, "https://") {
		return addr
	}
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return "http://" + addr
	}
	return ""
}

func runtimeRecordAddressURL(rec RuntimeRecord) string {
	if rec.Network == "unix" {
		if path.IsAbs(rec.Address) {
			return "unix://" + rec.Address
		}
	}
	return RuntimeAddressURL(rec.Address)
}

// AliveRuntimeRecords returns Kata runtime state records for daemons whose
// process is still alive. Missing or unreadable runtime state is treated as no
// daemon running.
func AliveRuntimeRecords() []RuntimeRecord {
	runtimeDir, err := RuntimeDir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		return nil
	}
	var records []RuntimeRecord
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		filenamePID, ok := runtimePIDFromName(name)
		if !ok {
			continue
		}
		body, err := os.ReadFile(filepath.Join(runtimeDir, name))
		if err != nil {
			continue
		}
		var rec RuntimeRecord
		if err := json.Unmarshal(body, &rec); err != nil {
			continue
		}
		if rec.PID != filenamePID || rec.Address == "" || !processAlive(rec.PID) {
			continue
		}
		records = append(records, rec)
	}
	sortRuntimeRecords(records)
	return records
}

func sortRuntimeRecords(records []RuntimeRecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].StartedAt.Equal(records[j].StartedAt) {
			return records[i].PID < records[j].PID
		}
		return records[i].StartedAt.Before(records[j].StartedAt)
	})
}

func runtimePIDFromName(name string) (int, bool) {
	if !strings.HasPrefix(name, "daemon.") || !strings.HasSuffix(name, ".json") {
		return 0, false
	}
	mid := strings.TrimSuffix(strings.TrimPrefix(name, "daemon."), ".json")
	pid, err := strconv.Atoi(mid)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// DiscoverLocalDaemonURL returns the address of a running machine-local Kata
// daemon for a local catalog entry: a unix socket or a loopback http(s)
// address.
func DiscoverLocalDaemonURL() string {
	for _, r := range AliveRuntimeRecords() {
		if u := runtimeRecordAddressURL(r); isLocalDaemonAddress(u) {
			return u
		}
	}
	return ""
}

func isLocalDaemonAddress(addr string) bool {
	u, err := url.Parse(addr)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "unix":
		return strings.TrimSpace(u.Path) != ""
	case "http", "https":
		if strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), "localhost") {
			return true
		}
		ip := net.ParseIP(u.Hostname())
		return ip != nil && ip.IsLoopback()
	default:
		return false
	}
}
