package main

import "testing"

func TestTurboStorageRejectsPlaintextDescendantsAndMissingKeys(t *testing.T) {
	root := "tank/postflight"
	valid := root + "\tencryption\taes-256-gcm\n" + root + "\tkeystatus\tavailable\n"
	if err := validateTurboStorage(root, valid); err != nil {
		t.Fatal(err)
	}
	for name, output := range map[string]string{
		"empty":           "",
		"missing key":     root + "\tencryption\taes-256-gcm\n",
		"unloaded key":    root + "\tencryption\taes-256-gcm\n" + root + "\tkeystatus\tunavailable\n",
		"plaintext root":  root + "\tencryption\toff\n" + root + "\tkeystatus\t-\n",
		"plaintext child": valid + root + "/gen/old\tencryption\toff\n" + root + "/gen/old\tkeystatus\t-\n",
		"outside root":    valid + "tank/other\tencryption\taes-256-gcm\ntank/other\tkeystatus\tavailable\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateTurboStorage(root, output); err == nil {
				t.Fatal("unsafe Turbo storage accepted")
			}
		})
	}
}
