package kata

import "time"

// Daemon describes one external Kata daemon that middleman can talk to.
type Daemon struct {
	ID            string
	URL           string
	Token         string
	TokenEnv      string
	Default       bool
	Local         bool
	AllowInsecure bool
}

// Catalog is the daemon catalog read from Kata's config file.
type Catalog struct {
	Daemons []Daemon
	Source  string
}

// Discovered names a runtime-discovered local Kata daemon and where it came
// from.
type Discovered struct {
	URL    string
	Token  string
	Source string
}

// RuntimeRecord mirrors Kata's daemon runtime state file.
type RuntimeRecord struct {
	PID       int       `json:"pid"`
	Network   string    `json:"network,omitempty"`
	Address   string    `json:"address"`
	DBPath    string    `json:"db_path"`
	Version   string    `json:"version,omitempty"`
	StartedAt time.Time `json:"started_at"`
}
