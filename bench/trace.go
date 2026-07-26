package bench

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// TraceDirEnv names the environment variable holding a directory of trace
// files. Traces are not committed to this repository: they are large, and
// their licences generally do not permit redistribution.
const TraceDirEnv = "AS_CACHE_TRACES"

// ErrNoTraceDir is returned when no trace directory has been configured.
var ErrNoTraceDir = errors.New("no trace directory configured")

// TraceFormat describes how to pull a cache key out of each line of a trace.
//
// Trace formats differ in trivial ways - a block number per line, a CSV with a
// key column, a space-separated offset and length - so rather than a parser
// per source there is one parser and a description of the layout.
type TraceFormat struct {
	// Delimiter splits a line into fields. Empty means split on whitespace.
	Delimiter string
	// KeyColumn is the zero-based field holding the key. Lines with fewer
	// fields are skipped.
	KeyColumn int
	// Comment, when not empty, marks lines to ignore.
	Comment string
	// SkipHeader drops the first non-comment line.
	SkipHeader bool
}

// LineFormat reads one key per line, the simplest and most common layout.
var LineFormat = TraceFormat{KeyColumn: 0}

// CSVFormat reads a comma-separated trace, taking the key from the given
// column and skipping a header row.
func CSVFormat(keyColumn int) TraceFormat {
	return TraceFormat{Delimiter: ",", KeyColumn: keyColumn, SkipHeader: true}
}

// field extracts the key column from a line, reporting whether it found one.
func (f TraceFormat) field(line string) (string, bool) {
	if f.Comment != "" && strings.HasPrefix(line, f.Comment) {
		return "", false
	}

	var fields []string
	if f.Delimiter == "" {
		fields = strings.Fields(line)
	} else {
		fields = strings.Split(line, f.Delimiter)
	}

	if f.KeyColumn >= len(fields) {
		return "", false
	}

	key := strings.TrimSpace(fields[f.KeyColumn])
	if key == "" {
		return "", false
	}

	return key, true
}

// LoadTrace reads up to limit requests from a trace file, transparently
// decompressing a .gz file. A limit of zero or less reads the whole file.
//
// Keys are interned: a real trace repeats a small key set across millions of
// requests, so sharing one string per distinct key is the difference between a
// workload that fits in memory and one that does not.
func LoadTrace(path string, format TraceFormat, limit int) (Workload, error) {
	file, err := os.Open(path)
	if err != nil {
		return Workload{}, fmt.Errorf("open trace: %w", err)
	}
	defer file.Close()

	var reader io.Reader = file
	if strings.HasSuffix(path, ".gz") {
		gz, gzErr := gzip.NewReader(file)
		if gzErr != nil {
			return Workload{}, fmt.Errorf("decompress trace: %w", gzErr)
		}
		defer gz.Close()
		reader = gz
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	intern := map[string]string{}
	keys := make([]string, 0, 1024)
	skippedHeader := false

	for scanner.Scan() {
		line := scanner.Text()

		key, ok := format.field(line)
		if !ok {
			continue
		}

		if format.SkipHeader && !skippedHeader {
			skippedHeader = true

			continue
		}

		if shared, seen := intern[key]; seen {
			key = shared
		} else {
			intern[key] = key
		}

		keys = append(keys, key)
		if limit > 0 && len(keys) >= limit {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return Workload{}, fmt.Errorf("read trace: %w", err)
	}

	if len(keys) == 0 {
		return Workload{}, fmt.Errorf("trace %s yielded no keys: wrong format?", filepath.Base(path))
	}

	return Workload{
		Name: strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".gz"), ".txt"),
		Keys: keys,
		Description: fmt.Sprintf("real trace: %d requests over %d distinct keys",
			len(keys), len(intern)),
	}, nil
}

// Known trace layouts, as verified against the published files. Each is named
// for the dataset it reads; see docs/TRACES.md for where to obtain them.
var (
	// TwitterFormat reads the Twitter Twemcache traces: seven comma-separated
	// columns, no header, key in column 1.
	TwitterFormat = TraceFormat{Delimiter: ",", KeyColumn: 1}

	// LIRSFormat reads the LIRS research traces: one page number per line.
	// Lines consisting of a single asterisk are checkpoint markers rather than
	// keys, and must not be replayed as accesses.
	LIRSFormat = TraceFormat{KeyColumn: 0, Comment: "*"}
)

// LoadARCTrace reads a trace in the ARC paper's layout: whitespace-separated
// records of "startBlock blockCount ...", where each record stands for
// blockCount consecutive block accesses.
//
// The expansion is the whole point and is easy to miss: a record is not one
// access. Reading the first column as a key would produce a workload with a
// different length and different locality from the one every published result
// refers to, so the numbers would not be comparable to the literature.
func LoadARCTrace(path string, limit int) (Workload, error) {
	file, err := os.Open(path)
	if err != nil {
		return Workload{}, fmt.Errorf("open trace: %w", err)
	}
	defer file.Close()

	var reader io.Reader = file
	if strings.HasSuffix(path, ".gz") {
		gz, gzErr := gzip.NewReader(file)
		if gzErr != nil {
			return Workload{}, fmt.Errorf("decompress trace: %w", gzErr)
		}
		defer gz.Close()
		reader = gz
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	intern := map[uint64]string{}
	keys := make([]string, 0, 1024)
	records := 0

	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}

		start, startErr := strconv.ParseUint(fields[0], 10, 64)
		count, countErr := strconv.ParseUint(fields[1], 10, 64)
		if startErr != nil || countErr != nil {
			continue
		}
		records++

		for i := uint64(0); i < count; i++ {
			block := start + i

			shared, seen := intern[block]
			if !seen {
				shared = strconv.FormatUint(block, 10)
				intern[block] = shared
			}

			keys = append(keys, shared)
			if limit > 0 && len(keys) >= limit {
				break
			}
		}

		if limit > 0 && len(keys) >= limit {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return Workload{}, fmt.Errorf("read trace: %w", err)
	}

	if len(keys) == 0 {
		return Workload{}, fmt.Errorf("trace %s yielded no keys: wrong format?", filepath.Base(path))
	}

	return Workload{
		Name: strings.TrimSuffix(filepath.Base(path), ".gz"),
		Keys: keys,
		Description: fmt.Sprintf("ARC trace: %d records expanded to %d accesses over %d distinct blocks",
			records, len(keys), len(intern)),
	}, nil
}

// TraceDir returns the configured trace directory, or ErrNoTraceDir when the
// environment variable is unset. Callers are expected to skip rather than fail
// when traces are absent, so a checkout without them still builds and tests.
func TraceDir() (string, error) {
	dir := os.Getenv(TraceDirEnv)
	if dir == "" {
		return "", ErrNoTraceDir
	}

	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", TraceDirEnv, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory: %s", TraceDirEnv, dir)
	}

	return dir, nil
}

// DiscoverTraces lists the trace files in the configured directory.
func DiscoverTraces() ([]string, error) {
	dir, err := TraceDir()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read trace dir: %w", err)
	}

	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".md") {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}

	return paths, nil
}

// DistinctKeys counts the distinct keys in a workload, which is what decides
// whether a given cache capacity is interesting: a cache larger than the key
// set makes every policy look identical.
func DistinctKeys(w Workload) int {
	seen := make(map[string]struct{}, len(w.Keys)/4+1)
	for _, key := range w.Keys {
		seen[key] = struct{}{}
	}

	return len(seen)
}
