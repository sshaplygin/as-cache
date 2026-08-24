package bench

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultMSRBlockSize is the granularity an MSR Cambridge trace's byte offsets
// are turned into cache keys at.
//
// 512 bytes is the sector size the volumes were traced at, and it is what the
// Caffeine simulator's reader for these traces uses, so results here are
// comparable with the ones published from it. The choice is not cosmetic: it
// decides how many distinct keys a trace has and how much reuse a request of a
// given size contributes, so two runs at different block sizes are two
// different workloads and cannot be compared.
const DefaultMSRBlockSize = 512

// maxMSRBlocksPerRecord bounds how far a single record may expand.
//
// A record carries a byte length that is multiplied out into one access per
// block, so a corrupt or misparsed length is not a skipped row: it is a row
// that can fill the entire workload on its own. At a limit of zero - documented
// as "read the whole file" - a length in the terabytes allocates until the
// process dies, and at a positive limit the first oversized record silently
// consumes the whole request budget, leaving every policy compared on one
// record's block range.
//
// 65536 blocks is 32 MiB at the default block size, far above the largest
// request these traces contain and far below the point where one row can
// dominate a replay.
const maxMSRBlocksPerRecord = 65536

// MSRFormat configures how an MSR Cambridge block I/O trace is read.
type MSRFormat struct {
	// BlockSize is the granularity, in bytes, that offsets and lengths are
	// resolved to. Zero means DefaultMSRBlockSize.
	BlockSize int

	// IncludeWrites replays write requests alongside reads.
	//
	// It is off by default, which is the convention the published cache
	// simulations of these traces follow: what is being modelled is a read
	// cache in front of the volume, and a write is a request the cache does
	// not serve. Turning it on measures something defensible but different -
	// a write-back cache - and the two numbers must not be compared.
	IncludeWrites bool
}

// msrColumns is the exact number of columns a record has:
// Timestamp,Hostname,DiskNumber,Type,Offset,Size,ResponseTime.
const msrColumns = 7

// blockRange returns the first block a byte range touches and how many blocks
// it spans.
//
// It is deliberately not ceil(size/blockSize). That counts the right number of
// blocks only when the offset is block-aligned, and silently loses the last
// block of every request that is not: at a 4096-byte block size, a 4096-byte
// read at offset 7014912 (which is 4096*1712 + 2560) runs into block 1713, and
// counting from the length alone stops at 1712. It would also miss the reuse
// between two requests that touch the same block from different offsets.
//
// At the default 512-byte block size every offset in these traces is aligned,
// so this agrees exactly with the simpler form - and with Caffeine's reader,
// which is what makes the default comparable with published results.
//
// The span is clamped to maxMSRBlocksPerRecord; see that constant.
func blockRange(offset, size, blockSize uint64) (start, blocks uint64) {
	start = offset / blockSize
	if size == 0 {
		return start, 0
	}

	// offset+size-1 is the last byte touched. Computed on the block index
	// rather than the byte total so a size near the top of the range cannot
	// wrap: the subtraction happens after the division.
	last := offset/blockSize + (size-1+offset%blockSize)/blockSize
	blocks = last - start + 1

	if blocks > maxMSRBlocksPerRecord {
		blocks = maxMSRBlocksPerRecord
	}

	return start, blocks
}

// blockSize returns the configured granularity, or the default.
func (f MSRFormat) blockSize() int {
	if f.BlockSize <= 0 {
		return DefaultMSRBlockSize
	}

	return f.BlockSize
}

// LoadMSRTrace reads a trace from the MSR Cambridge block I/O trace set: one
// comma-separated record per I/O request, laid out as
//
//	Timestamp,Hostname,DiskNumber,Type,Offset,Size,ResponseTime
//
// where Timestamp is a Windows file time, Type is Read or Write, and Offset
// and Size are in bytes from the start of the logical volume.
//
// Each record covers Size bytes and therefore stands for as many block
// accesses as fit in it, which is the same trap the ARC layout has: reading
// one record as one access would produce a workload with different length and
// different locality from the one every published result on these traces
// refers to. A 64 KiB read is 128 accesses at the default block size, not one.
//
// Keys are namespaced by host and disk. The trace set covers thirteen servers
// whose volumes are numbered from zero independently, so block 40 means a
// different thing on each of them and pooling them by block number alone would
// invent reuse that never happened.
//
// A limit of zero or less reads the whole file. Records that do not parse are
// skipped rather than failing the load, which is what makes a partially
// downloaded file usable: its last line is normally truncated.
func LoadMSRTrace(path string, format MSRFormat, limit int) (Workload, error) {
	scanner, closeTrace, err := openTraceScanner(path)
	if err != nil {
		return Workload{}, err
	}
	defer closeTrace()

	blockSize := uint64(format.blockSize()) //nolint:gosec // blockSize() returns a positive int

	intern := map[string]string{}
	keys := make([]string, 0, 1024)
	records, reads, writes := 0, 0, 0

	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ",")
		if len(fields) != msrColumns {
			// A record has exactly seven columns. Accepting fewer would let
			// the truncated last line of a partial download through whenever
			// the cut landed past the size column - replaying a phantom block
			// range, or a write that lost the "te" off its type and became a
			// read.
			continue
		}

		offset, offsetErr := strconv.ParseUint(strings.TrimSpace(fields[4]), 10, 64)
		size, sizeErr := strconv.ParseUint(strings.TrimSpace(fields[5]), 10, 64)
		if offsetErr != nil || sizeErr != nil {
			// Not a record: either the header some redistributions add, or the
			// truncated tail of a partial download.
			continue
		}

		// An allow-list rather than "anything that is not a Write is a read",
		// which is what Caffeine's reader for these traces does. The trace set
		// contains types beyond the two documented ones, and a deny-list
		// silently admits every unrecognised or garbled one into a workload
		// labelled "reads only".
		read := strings.EqualFold(strings.TrimSpace(fields[3]), "Read")
		write := strings.EqualFold(strings.TrimSpace(fields[3]), "Write")
		if !read && !write {
			continue
		}
		records++

		if write {
			writes++
		} else {
			reads++
		}
		if write && !format.IncludeWrites {
			continue
		}

		namespace := strings.TrimSpace(fields[1]) + ":" + strings.TrimSpace(fields[2]) + ":"
		start, blocks := blockRange(offset, size, blockSize)

		for i := uint64(0); i < blocks; i++ {
			key := namespace + strconv.FormatUint(start+i, 10)

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

	kind := "block accesses from reads"
	if format.IncludeWrites {
		kind = "block accesses from reads and writes"
	}

	return Workload{
		Name: strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".gz"), ".csv"),
		Keys: keys,
		Description: fmt.Sprintf(
			"MSR Cambridge trace: %d records (%d reads, %d writes) expanded to %d %s over %d distinct %d-byte blocks",
			records, reads, writes, len(keys), kind, len(intern), format.blockSize()),
	}, nil
}
