package park


// Content artifact plumbing on the host side. The blob is opaque — its
// layout belongs entirely to the sim, which validates it at the content
// stage. The host only needs its content identity (to store, serve, and
// cross-check it), a pure function of the bytes mirroring the sim's
// derivation exactly.

// terrainID is mix64(fnv1a(blob)): the artifact's single name everywhere —
// journal rows, content_set payloads, the /terrain URL, the world hash.
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

