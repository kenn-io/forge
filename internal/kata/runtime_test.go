package kata

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeRuntimeFile(t *testing.T, home string, rec RuntimeRecord) {
	t.Helper()

	db := filepath.Join(home, "kata.db")
	abs, err := filepath.Abs(db)
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(abs))
	dbhash := hex.EncodeToString(sum[:])[:12]
	dir := filepath.Join(home, "runtime", dbhash)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	body, err := json.Marshal(rec)
	require.NoError(t, err)
	path := filepath.Join(dir, fmt.Sprintf("daemon.%d.json", rec.PID))
	require.NoError(t, os.WriteFile(path, body, 0o644))
}

func TestDiscoverAlivenessFromRuntimeFile(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", "")
	writeRuntimeFile(t, home, RuntimeRecord{
		PID:     os.Getpid(),
		Address: "127.0.0.1:9876",
	})

	got := Discover()

	require.NotNil(got)
	assert.Equal("http://127.0.0.1:9876", got.URL)
	assert.NotEmpty(got.Source)
}

func TestDiscoverNoRuntimeDir(t *testing.T) {
	assert := Assert.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", "")

	assert.Nil(Discover())
}

func TestDiscoverSkipsDeadProcess(t *testing.T) {
	assert := Assert.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", "")
	writeRuntimeFile(t, home, RuntimeRecord{
		PID:     2_147_483_647,
		Address: "127.0.0.1:9",
	})

	assert.Nil(Discover())
}

func TestDiscoverPrefersHTTPOverUnix(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", "")
	writeRuntimeFile(t, home, RuntimeRecord{
		PID:     os.Getpid(),
		Address: "unix:///tmp/k.sock",
	})
	writeRuntimeFile(t, home, RuntimeRecord{
		PID:     os.Getppid(),
		Address: "127.0.0.1:9",
	})

	got := Discover()

	require.NotNil(got)
	assert.Equal("http://127.0.0.1:9", got.URL)
}

func TestDiscoverSkipsUnparseableJSON(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", "")
	db := filepath.Join(home, "kata.db")
	abs, err := filepath.Abs(db)
	require.NoError(err)
	sum := sha256.Sum256([]byte(abs))
	dbhash := hex.EncodeToString(sum[:])[:12]
	dir := filepath.Join(home, "runtime", dbhash)
	require.NoError(os.MkdirAll(dir, 0o700))
	require.NoError(os.WriteFile(filepath.Join(dir, "daemon.123.json"), []byte("not json"), 0o644))

	assert.Nil(Discover())
}

func TestDiscoverLocalDaemonURLRunningDaemon(t *testing.T) {
	assert := Assert.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", "")
	writeRuntimeFile(t, home, RuntimeRecord{
		PID:     os.Getpid(),
		Address: "127.0.0.1:9876",
	})

	assert.Equal("http://127.0.0.1:9876", DiscoverLocalDaemonURL())
}

func TestDiscoverLocalDaemonURLLocalhostRuntimeRecord(t *testing.T) {
	assert := Assert.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", "")
	writeRuntimeFile(t, home, RuntimeRecord{
		PID:     os.Getpid(),
		Address: "localhost:9876",
	})

	assert.Equal("http://localhost:9876", DiscoverLocalDaemonURL())
}

func TestDiscoverLocalDaemonURLNoneRunning(t *testing.T) {
	assert := Assert.New(t)

	t.Setenv("KATA_HOME", t.TempDir())
	t.Setenv("KATA_DB", "")

	assert.Empty(DiscoverLocalDaemonURL())
}

func TestDiscoverLocalDaemonURLRejectsNonLoopbackHTTP(t *testing.T) {
	assert := Assert.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", "")
	writeRuntimeFile(t, home, RuntimeRecord{
		PID:     os.Getpid(),
		Address: "203.0.113.5:7777",
	})

	assert.Empty(DiscoverLocalDaemonURL())
}

func TestDiscoverLocalDaemonURLUnixSocket(t *testing.T) {
	assert := Assert.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", "")
	writeRuntimeFile(t, home, RuntimeRecord{
		PID:     os.Getpid(),
		Address: "unix:///tmp/kata.sock",
	})

	assert.Equal("unix:///tmp/kata.sock", DiscoverLocalDaemonURL())
}

func TestDiscoverLocalDaemonURLUnixSocketRuntimeRecord(t *testing.T) {
	assert := Assert.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", "")
	writeRuntimeFile(t, home, RuntimeRecord{
		PID:     os.Getpid(),
		Network: "unix",
		Address: "/tmp/kata.sock",
	})

	assert.Equal("unix:///tmp/kata.sock", DiscoverLocalDaemonURL())
}

func TestDiscoverLocalDaemonURLPrefersLocalOverNonLoopbackHTTP(t *testing.T) {
	assert := Assert.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", "")
	writeRuntimeFile(t, home, RuntimeRecord{
		PID:     os.Getpid(),
		Address: "203.0.113.5:7777",
	})
	writeRuntimeFile(t, home, RuntimeRecord{
		PID:     os.Getppid(),
		Address: "unix:///tmp/kata.sock",
	})

	assert.Equal("unix:///tmp/kata.sock", DiscoverLocalDaemonURL())
}

func TestAliveRuntimeRecordsRejectsMismatchedFilenamePID(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", "")
	dir := runtimeDirForTest(t, home)
	body, err := json.Marshal(RuntimeRecord{
		PID:     os.Getpid(),
		Address: "127.0.0.1:9876",
	})
	require.NoError(err)
	require.NoError(os.WriteFile(filepath.Join(dir, fmt.Sprintf("daemon.%d.json", os.Getpid()+1)), body, 0o644))

	assert.Empty(AliveRuntimeRecords())
}

func TestAliveRuntimeRecordsRejectsEmptyAddress(t *testing.T) {
	assert := Assert.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", "")
	writeRuntimeFile(t, home, RuntimeRecord{PID: os.Getpid()})

	assert.Empty(AliveRuntimeRecords())
}

func TestAliveRuntimeRecordsSortsByStartedAtThenPID(t *testing.T) {
	assert := Assert.New(t)

	base := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	records := []RuntimeRecord{
		{PID: 12, Address: "127.0.0.1:1002", StartedAt: base.Add(time.Minute)},
		{PID: 10, Address: "127.0.0.1:1000", StartedAt: base},
		{PID: 11, Address: "127.0.0.1:1001", StartedAt: base},
	}

	sortRuntimeRecords(records)

	assert.Equal([]int{10, 11, 12}, []int{records[0].PID, records[1].PID, records[2].PID})
}

func TestRuntimeAddressURL(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{"127.0.0.1:7474", "http://127.0.0.1:7474"},
		{"localhost:7474", "http://localhost:7474"},
		{"[::1]:7474", "http://[::1]:7474"},
		{"203.0.113.5:7474", "http://203.0.113.5:7474"},
		{"unix:///tmp/kata.sock", "unix:///tmp/kata.sock"},
		{"http://127.0.0.1:7474", "http://127.0.0.1:7474"},
		{"https://kata.example.com", "https://kata.example.com"},
		{"", ""},
		{"garbage", ""},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			Assert.Equal(t, tt.want, RuntimeAddressURL(tt.addr))
		})
	}
}

func TestRuntimeRecordAddressURL(t *testing.T) {
	tests := []struct {
		name string
		rec  RuntimeRecord
		want string
	}{
		{"current unix shape", RuntimeRecord{Network: "unix", Address: "/tmp/kata.sock"}, "unix:///tmp/kata.sock"},
		{"legacy unix url", RuntimeRecord{Address: "unix:///tmp/kata.sock"}, "unix:///tmp/kata.sock"},
		{"tcp bare address", RuntimeRecord{Network: "tcp", Address: "127.0.0.1:7474"}, "http://127.0.0.1:7474"},
		{"unknown invalid", RuntimeRecord{Network: "unix", Address: "relative.sock"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Assert.Equal(t, tt.want, runtimeRecordAddressURL(tt.rec))
		})
	}
}

func TestIsLocalDaemonAddress(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"unix:///tmp/kata.sock", true},
		{"http://127.0.0.1:7777", true},
		{"https://127.0.0.1:7777", true},
		{"http://[::1]:7777", true},
		{"http://192.168.1.10:7777", false},
		{"http://203.0.113.5:7777", false},
		{"http://0.0.0.0:7777", false},
		{"http://localhost:7777", true},
		{"http://localhost.:7777", true},
		{"unix://", false},
		{"https://kata.example.com", false},
		{"", false},
		{"ftp://127.0.0.1", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			Assert.Equal(t, tt.want, isLocalDaemonAddress(tt.addr))
		})
	}
}

func runtimeDirForTest(t *testing.T, home string) string {
	t.Helper()

	db := filepath.Join(home, "kata.db")
	abs, err := filepath.Abs(db)
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(abs))
	dbhash := hex.EncodeToString(sum[:])[:12]
	dir := filepath.Join(home, "runtime", dbhash)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	return dir
}
