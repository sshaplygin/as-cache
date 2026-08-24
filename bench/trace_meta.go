package bench

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// maxMetaOpCount bounds how many requests one row may expand into.
//
// The column is a small integer in the published traces, so the cap only ever
// fires on a corrupt row - the truncated last line of a partial download, for
// instance - where an unbounded expansion would allocate until the process
// died. Rows it clips are counted and reported in the workload description
// rather than silently trimmed.
const maxMetaOpCount = 1 << 16

// MetaKVFormat configures how a Meta kvcache trace is read.
type MetaKVFormat struct {
	// IncludeWrites replays SET and DELETE rows alongside the reads.
	//
	// It is off by default. A row whose op is a write is the application
	// storing a value, not a request the cache had to answer, and counting it
	// as one inflates the hit rate: in the 2022 traces almost every SET is
	// immediately followed by a GET of the same key, so replaying both would
	// hand every policy a free hit per write.
	IncludeWrites bool
}

// metaKVHeader maps the columns this loader needs onto their positions in a
// particular file's header row.
type metaKVHeader struct {
	key     int
	op      int
	opCount int
}

// parseMetaKVHeader reads the header row of a Meta kvcache trace.
//
// The layout changed between releases - the 2022 traces are
// "key,op,size,op_count,key_size" and the 2024 traces are
// "op_time,key,key_size,op,op_count,size,cache_hits,ttl,usecase,sub_usecase" -
// so the columns are located by name rather than by position. Reading the 2024
// layout at the 2022 offsets would take the key size as the operation and
// discard the whole file as unrecognised ops, which is a failure that looks
// exactly like an empty trace.
func parseMetaKVHeader(line string) (metaKVHeader, error) {
	header := metaKVHeader{key: -1, op: -1, opCount: -1}

	for i, name := range strings.Split(line, ",") {
		switch strings.TrimSpace(name) {
		case "key":
			header.key = i
		case "op":
			header.op = i
		case "op_count":
			header.opCount = i
		}
	}

	if header.key < 0 || header.op < 0 || header.opCount < 0 {
		// op_count is required, not optional. Without it every row expands to
		// exactly one request, which is the precise failure this loader exists
		// to prevent: the same keys, a fraction of the requests, most of the
		// reuse gone, and no error to show for it. Refusing the file is the
		// only honest answer, since there is nothing in the remaining columns
		// that could reconstruct the missing counts.
		return metaKVHeader{}, fmt.Errorf(
			"unrecognised header %q: a Meta kvcache trace must name key, op and op_count columns", line)
	}

	return header, nil
}

// width returns the number of columns a row must have to be readable.
func (h metaKVHeader) width() int {
	return max(h.key, max(h.op, h.opCount)) + 1
}

// LoadMetaKVTrace reads a trace from Meta's published kvcache workloads - the
// ones CacheBench replays, captured from a production key-value cache cluster.
//
// Two things about the format decide whether the resulting workload means
// anything.
//
// The op_count column is a repeat count, not a sequence number: consecutive
// identical operations were collapsed into one row when the trace was
// captured, and CacheBench replays each row that many times. A loader that
// reads one row as one request produces a workload with the same keys, far
// fewer requests, and much less reuse than the traffic it was taken from -
// which is to say, wrong hit rates that look entirely plausible.
//
// Only reads are replayed by default; see MetaKVFormat.IncludeWrites.
//
// A limit of zero or less reads the whole file, which for the published files
// means tens of gigabytes; callers normally pass one. Rows that do not parse
// are skipped rather than failing the load, so a file downloaded in part - the
// only practical way to work with these - is usable to its last complete row.
func LoadMetaKVTrace(path string, format MetaKVFormat, limit int) (Workload, error) {
	scanner, closeTrace, err := openTraceScanner(path)
	if err != nil {
		return Workload{}, err
	}
	defer closeTrace()

	if !scanner.Scan() {
		return Workload{}, fmt.Errorf("trace %s is empty", filepath.Base(path))
	}
	header, err := parseMetaKVHeader(scanner.Text())
	if err != nil {
		return Workload{}, fmt.Errorf("read trace %s: %w", filepath.Base(path), err)
	}
	width := header.width()

	intern := map[string]string{}
	keys := make([]string, 0, 1024)
	rows, reads, writes, clipped, malformed := 0, 0, 0, 0, 0

	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ",")
		if len(fields) < width {
			continue
		}

		key := strings.TrimSpace(fields[header.key])
		if key == "" {
			continue
		}
		rows++

		// GET and GET_LEASE are the read operations; SET, SET_LEASE, DELETE
		// and the rest are writes.
		read := strings.HasPrefix(strings.ToUpper(strings.TrimSpace(fields[header.op])), "GET")
		if read {
			reads++
		} else {
			writes++
		}
		if !read && !format.IncludeWrites {
			continue
		}

		// A row whose op_count does not parse, or is not positive, is corrupt
		// rather than a row worth one request: silently reading it as 1 would
		// invent a request the trace does not contain and hide the corruption
		// behind a plausible number.
		parsed, parseErr := strconv.Atoi(strings.TrimSpace(fields[header.opCount]))
		if parseErr != nil || parsed < 1 {
			malformed++

			continue
		}

		count := parsed
		if count > maxMetaOpCount {
			count = maxMetaOpCount
			clipped++
		}

		if shared, seen := intern[key]; seen {
			key = shared
		} else {
			// Cloned before it is retained. strings.Split returns substrings
			// that share the scanned line's backing array, so interning one
			// as-is keeps the whole row alive for as long as the workload
			// exists - on the wide 2024 layout that is ~70 bytes held per
			// 16-byte key.
			key = strings.Clone(key)
			intern[key] = key
		}

		for i := 0; i < count; i++ {
			keys = append(keys, key)
			if limit > 0 && len(keys) >= limit {
				break
			}
		}

		if limit > 0 && len(keys) >= limit {
			break
		}
	}

	if err := scanner.Err(); err != nil && (!truncatedTrace(err) || len(keys) == 0) {
		return Workload{}, fmt.Errorf("read trace: %w", err)
	}

	if len(keys) == 0 {
		return Workload{}, fmt.Errorf("trace %s yielded no keys: wrong format?", filepath.Base(path))
	}

	kind := "reads"
	if format.IncludeWrites {
		kind = "reads and writes"
	}
	description := fmt.Sprintf(
		"Meta kvcache trace: %d rows (%d reads, %d writes) expanded by op_count to %d %s over %d distinct keys",
		rows, reads, writes, len(keys), kind, len(intern))
	if clipped > 0 {
		description += fmt.Sprintf("; %d rows had an op_count above %d and were clipped to it",
			clipped, maxMetaOpCount)
	}
	if malformed > 0 {
		description += fmt.Sprintf("; %d rows had an unreadable op_count and were skipped", malformed)
	}

	return Workload{
		Name:        strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".gz"), ".csv"),
		Keys:        keys,
		Description: description,
	}, nil
}
