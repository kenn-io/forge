package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
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
	encoder := jsontext.NewEncoder(os.Stdout, jsontext.WithIndentPrefix(""), jsontext.WithIndent("  "))
	if err := json.MarshalEncode(encoder, inventory); err != nil {
		fmt.Fprintf(os.Stderr, "encode transport inventory: %v\n", err)
		os.Exit(1)
	}
}
