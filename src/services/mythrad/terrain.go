package main

import (
	"encoding/binary"
	"fmt"
)

// Terrain artifact plumbing on the host side. The blob's semantics belong
// to the sim; the host only needs its content identity (to store, serve,
// and cross-check it) and its header dims (for the welcome line). Both are
// pure functions of the bytes, mirroring the sim's derivation exactly.

const terrainHeader = 16

// terrainID is mix64(fnv1a(blob)): the artifact's single name everywhere —
// journal rows, terrain_set payloads, the /terrain URL, the world hash.
func terrainID(blob []byte) uint64 {
	h := uint64(0xCBF29CE484222325)
	for _, b := range blob {
		h ^= uint64(b)
		h *= 0x00000100000001B3
	}
	z := h
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func terrainHeaderFields(blob []byte) (schema uint32, w, h uint16, err error) {
	if len(blob) < terrainHeader || string(blob[:4]) != "MYT1" {
		return 0, 0, 0, fmt.Errorf("not a terrain blob (%d bytes)", len(blob))
	}
	return binary.LittleEndian.Uint32(blob[4:8]),
		binary.LittleEndian.Uint16(blob[8:10]),
		binary.LittleEndian.Uint16(blob[10:12]), nil
}
