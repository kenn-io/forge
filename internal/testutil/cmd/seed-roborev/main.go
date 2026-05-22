package main

import (
	"flag"
	"fmt"
	"os"

	"go.kenn.io/middleman/internal/testutil"
)

func main() {
	out := flag.String("out", "", "output SQLite database path")
	flag.Parse()

	if *out == "" {
		fmt.Fprintln(os.Stderr, "error: -out flag is required")
		flag.Usage()
		os.Exit(1)
	}

	// Remove existing DB so reseeding works cleanly.
	_ = os.Remove(*out)
	_ = os.Remove(*out + "-wal")
	_ = os.Remove(*out + "-shm")

	if err := testutil.SeedRoborevDB(*out); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("seeded roborev database: %s\n", *out)
}
