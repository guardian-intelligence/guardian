package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const hostUsage = `usage:
  guardian host list                 list checked-in hosts
  guardian host inspect [host.yaml]  validate and print the host assignment
  guardian host use <host.yaml>      set the default host used by up/down`

func runHostCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("host: %w: missing subcommand\n%s", errUsage, hostUsage)
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return fmt.Errorf("host list: %w: expected no arguments", errUsage)
		}
		return runHostList()
	case "inspect":
		if len(args) > 2 {
			return fmt.Errorf("host inspect: %w: expected at most one host.yaml path", errUsage)
		}
		return runHostInspect(args[1:])
	case "use":
		if len(args) != 2 {
			return fmt.Errorf("host use: %w: expected a host.yaml path", errUsage)
		}
		return runConfigCmd([]string{"host", args[1]})
	default:
		return fmt.Errorf("host: %w: unknown subcommand %q\n%s", errUsage, args[0], hostUsage)
	}
}

func runHostList() error {
	paths, err := checkedInHostPaths()
	if err != nil {
		return err
	}
	fmt.Println("HOST\tENVIRONMENT\tCLUSTER\tHOSTNAME\tADDRESS\tPROVIDER\tSERVER")
	for _, path := range paths {
		host, _, err := loadHostConfig(path)
		if err != nil {
			return err
		}
		fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			host.Host,
			host.Environment,
			host.Cluster.Name,
			host.Node.Hostname,
			host.Node.Address,
			host.Provider.Name,
			host.Provider.ServerID,
		)
	}
	return nil
}

func runHostInspect(args []string) error {
	host, path, err := resolveHost(args)
	if err != nil {
		return err
	}
	fmt.Printf("path\t%s\n", path)
	fmt.Printf("host\t%s\n", host.Host)
	fmt.Printf("environment\t%s\n", host.Environment)
	fmt.Printf("cluster\t%s\n", host.Cluster.Name)
	fmt.Printf("endpoint\t%s\n", host.Cluster.Endpoint)
	fmt.Printf("hostname\t%s\n", host.Node.Hostname)
	fmt.Printf("address\t%s/%d\n", host.Node.Address, host.Node.PrefixLength)
	fmt.Printf("gateway\t%s\n", host.Node.Gateway)
	fmt.Printf("provider\t%s\n", host.Provider.Name)
	fmt.Printf("serverId\t%s\n", host.Provider.ServerID)
	fmt.Printf("environmentBundle\t%s\n", host.EnvironmentBundle.Path)
	return nil
}

func checkedInHostPaths() ([]string, error) {
	roots := []string{resolvePath(filepath.Join("src", "hosts"))}
	if runfilesRoot, err := toolPath("_main/src/hosts"); err == nil {
		roots = append(roots, runfilesRoot)
	}
	seen := map[string]bool{}
	var paths []string
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(root, entry.Name(), "host.yaml")
			if _, err := os.Stat(path); err != nil {
				continue
			}
			if seen[path] {
				continue
			}
			seen[path] = true
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("host list: no checked-in hosts found under src/hosts")
	}
	sort.Strings(paths)
	return paths, nil
}
