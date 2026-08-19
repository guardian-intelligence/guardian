package gateway

// The chunk directory: which (game, chunk) pairs exist and where their
// authorities listen. One live view feeds both ticket minting and session
// routing, replacing the boot-time PARK_BACKENDS env — chunk churn must
// never require restarting the process that holds every player's QUIC
// session. The file is expected to be a mounted ConfigMap; the watcher
// polls it the same way module distribution polls the behavior dir.
//
// Format, one entry per line:
//
//	<game> <chunk> <host:port>
//
// Blank lines and #-comments are allowed. Fail-closed: a missing or
// unparseable file yields an empty directory (every mint and hello is
// refused) rather than a stale or partial one.

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

type chunkDirectory struct {
	mu     sync.RWMutex
	chunks map[string]string // "game/chunk" -> backend addr
}

func newChunkDirectory() *chunkDirectory {
	return &chunkDirectory{chunks: map[string]string{}}
}

// Lookup satisfies trunk.Directory.
func (d *chunkDirectory) Lookup(game, chunk string) (string, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	addr, ok := d.chunks[game+"/"+chunk]
	return addr, ok
}

func (d *chunkDirectory) allowed(game, chunk string) bool {
	_, ok := d.Lookup(game, chunk)
	return ok
}

func (d *chunkDirectory) replace(chunks map[string]string) {
	d.mu.Lock()
	d.chunks = chunks
	d.mu.Unlock()
}

func parseChunkDirectory(r io.Reader) (map[string]string, error) {
	chunks := map[string]string{}
	scanner := bufio.NewScanner(r)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 3 {
			return nil, fmt.Errorf("line %d: want '<game> <chunk> <addr>', got %q", line, text)
		}
		chunks[fields[0]+"/"+fields[1]] = fields[2]
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return chunks, nil
}

// loadChunkDirectory reads path into d. Errors empty the directory: no
// chunk is reachable through a directory we cannot read, and an operator
// page beats silently serving yesterday's topology.
func loadChunkDirectory(path string, d *chunkDirectory) error {
	f, err := os.Open(path)
	if err != nil {
		d.replace(map[string]string{})
		return err
	}
	defer f.Close()
	chunks, err := parseChunkDirectory(f)
	if err != nil {
		d.replace(map[string]string{})
		return err
	}
	d.replace(chunks)
	return nil
}

// watchChunkDirectory polls path and applies changes without a restart.
func watchChunkDirectory(path string, d *chunkDirectory) {
	var lastErr string
	var lastMod time.Time
	var lastSize int64
	for ; ; time.Sleep(2 * time.Second) {
		info, err := os.Stat(path)
		if err == nil && info.ModTime().Equal(lastMod) && info.Size() == lastSize {
			continue
		}
		if err := loadChunkDirectory(path, d); err != nil {
			if msg := err.Error(); msg != lastErr {
				log.Printf("chunk directory %s: %v (directory emptied)", path, err)
				lastErr = msg
			}
			lastMod, lastSize = time.Time{}, 0
			continue
		}
		lastErr = ""
		if info != nil {
			lastMod, lastSize = info.ModTime(), info.Size()
		}
		d.mu.RLock()
		n := len(d.chunks)
		d.mu.RUnlock()
		log.Printf("chunk directory %s: %d chunk(s) loaded", path, n)
	}
}
