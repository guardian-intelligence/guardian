package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

type inventory struct {
	Nodes []inventoryNode `json:"nodes"`
}

type inventoryNode struct {
	Name     string `json:"name"`
	ServerID string `json:"server_id"`
}

type options struct {
	format    string
	inventory string
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "management-inventory: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	opts, command, err := parseOptions(args)
	if err != nil {
		return err
	}

	inv, err := readInventory(opts.inventory)
	if err != nil {
		return err
	}

	switch command {
	case "nodes":
		return writeNodes(stdout, inv.Nodes, opts.format)
	default:
		return fmt.Errorf("unsupported command %q", command)
	}
}

func parseOptions(args []string) (options, string, error) {
	fs := flag.NewFlagSet("management-inventory", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	opts := options{}
	fs.StringVar(&opts.format, "format", "lines", "output format: lines or csv")
	fs.StringVar(&opts.inventory, "inventory", "src/infrastructure/inventory/guardian-mgmt.json", "management inventory JSON")

	if err := fs.Parse(args); err != nil {
		return opts, "", err
	}
	if fs.NArg() != 1 {
		return opts, "", errors.New("pass exactly one command: nodes")
	}
	if opts.format != "lines" && opts.format != "csv" {
		return opts, "", fmt.Errorf("unsupported --format %q", opts.format)
	}
	return opts, fs.Arg(0), nil
}

func readInventory(path string) (inventory, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return inventory{}, err
	}
	var inv inventory
	if err := json.Unmarshal(raw, &inv); err != nil {
		return inventory{}, err
	}
	return inv, nil
}

func writeNodes(w io.Writer, nodes []inventoryNode, format string) error {
	names := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if node.Name != "" {
			names = append(names, node.Name)
		}
	}
	switch format {
	case "lines":
		for _, name := range names {
			if _, err := fmt.Fprintln(w, name); err != nil {
				return err
			}
		}
	case "csv":
		if _, err := fmt.Fprintln(w, strings.Join(names, ",")); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
	return nil
}
