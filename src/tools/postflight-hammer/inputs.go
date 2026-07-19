package main

import (
	"fmt"
	"sort"
	"strings"
)

// workflowInputs implements flag.Value for repeatable workflow_dispatch
// inputs. Duplicate keys are rejected so the state artifact has one
// unambiguous value for every input.
type workflowInputs map[string]string

func (i workflowInputs) Set(raw string) error {
	key, value, ok := strings.Cut(raw, "=")
	key = strings.TrimSpace(key)
	if !ok || key == "" {
		return fmt.Errorf("workflow input %q must be key=value", raw)
	}
	if _, exists := i[key]; exists {
		return fmt.Errorf("workflow input %q was provided more than once", key)
	}
	i[key] = value
	return nil
}

func (i workflowInputs) String() string {
	keys := make([]string, 0, len(i))
	for key := range i {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key+"="+i[key])
	}
	return strings.Join(values, ",")
}

func cloneInputs(inputs workflowInputs) map[string]string {
	if len(inputs) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(inputs))
	for key, value := range inputs {
		cloned[key] = value
	}
	return cloned
}
