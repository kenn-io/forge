package main

import (
	"encoding/json"
	"fmt"
	"os"

	"go.kenn.io/forge/internal/server"
)

func main() {
	inventory, err := server.NewTransportInventory()
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate transport inventory: %v\n", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(inventory); err != nil {
		fmt.Fprintf(os.Stderr, "encode transport inventory: %v\n", err)
		os.Exit(1)
	}
}
