package quality

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/filewrite"
)

var supportedGoTestName = regexp.MustCompile(`^(Test|Example|Fuzz)[A-Za-z0-9_]*$`)

// planGoTestShards assigns every discovered top-level test, example, and fuzz
// seed to exactly one deterministic process shard. Sorting before round-robin
// assignment makes a fresh CI worker independent of go test discovery order
// while spreading adjacent feature families across workers.
func planGoTestShards(tests []string, shardCount int) ([][]string, error) {
	if shardCount < 1 {
		return nil, fmt.Errorf("test shard count must be at least 1")
	}
	if len(tests) == 0 {
		return nil, fmt.Errorf("no tests, examples, or fuzz targets were discovered")
	}
	names := append([]string(nil), tests...)
	sort.Strings(names)
	for index, name := range names {
		if !supportedGoTestName.MatchString(name) {
			return nil, fmt.Errorf("unsupported Go test name %q", name)
		}
		if index > 0 && names[index-1] == name {
			return nil, fmt.Errorf("duplicate Go test name %q", name)
		}
	}
	if shardCount > len(names) {
		shardCount = len(names)
	}
	shards := make([][]string, shardCount)
	for index, name := range names {
		shards[index%shardCount] = append(shards[index%shardCount], name)
	}
	return shards, nil
}

type coverageBlock struct {
	location   string
	statements int
	count      int64
}

// mergeCoverageProfiles combines process-isolated shards without weakening
// coverage semantics. A statement block has one stable source identity; a
// changed statement count at the same location is ambiguous and fails closed.
func mergeCoverageProfiles(paths []string, output string) error {
	if len(paths) == 0 {
		return fmt.Errorf("at least one coverage profile is required")
	}
	mode := ""
	blocks := map[string]coverageBlock{}
	for _, path := range paths {
		profileMode, profileBlocks, err := readCoverageProfile(path)
		if err != nil {
			return err
		}
		if mode == "" {
			mode = profileMode
		} else if profileMode != mode {
			return fmt.Errorf("coverage mode mismatch: %s uses %q, want %q", path, profileMode, mode)
		}
		for _, incoming := range profileBlocks {
			current, exists := blocks[incoming.location]
			if exists && current.statements != incoming.statements {
				return fmt.Errorf("coverage block %s has statement count %d, want %d", incoming.location, incoming.statements, current.statements)
			}
			if !exists {
				blocks[incoming.location] = incoming
				continue
			}
			switch mode {
			case "set":
				if incoming.count > current.count {
					current.count = incoming.count
				}
			case "count", "atomic":
				current.count += incoming.count
			default:
				return fmt.Errorf("unsupported coverage mode %q", mode)
			}
			blocks[incoming.location] = current
		}
	}
	return writeCoverageProfileAtomically(output, mode, blocks)
}

func readCoverageProfile(path string) (string, []coverageBlock, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", nil, fmt.Errorf("open coverage profile %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", nil, fmt.Errorf("read coverage profile %s: %w", path, err)
		}
		return "", nil, fmt.Errorf("coverage profile %s is empty", path)
	}
	header := strings.Fields(scanner.Text())
	if len(header) != 2 || header[0] != "mode:" {
		return "", nil, fmt.Errorf("coverage profile %s has invalid mode header", path)
	}
	mode := header[1]
	if mode != "set" && mode != "count" && mode != "atomic" {
		return "", nil, fmt.Errorf("unsupported coverage mode %q in %s", mode, path)
	}
	var blocks []coverageBlock
	seen := map[string]bool{}
	for line := 2; scanner.Scan(); line++ {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 {
			return "", nil, fmt.Errorf("invalid coverage profile %s at line %d", path, line)
		}
		statements, statementErr := strconv.Atoi(fields[1])
		count, countErr := strconv.ParseInt(fields[2], 10, 64)
		if statementErr != nil || statements < 0 || countErr != nil || count < 0 {
			return "", nil, fmt.Errorf("invalid coverage profile %s at line %d", path, line)
		}
		if seen[fields[0]] {
			return "", nil, fmt.Errorf("duplicate coverage block %s in %s", fields[0], path)
		}
		seen[fields[0]] = true
		blocks = append(blocks, coverageBlock{location: fields[0], statements: statements, count: count})
	}
	if err := scanner.Err(); err != nil {
		return "", nil, fmt.Errorf("read coverage profile %s: %w", path, err)
	}
	return mode, blocks, nil
}

func writeCoverageProfileAtomically(output, mode string, blocks map[string]coverageBlock) error {
	return writeCoverageProfileAtomicallyInjected(output, mode, blocks, nil)
}

// writeCoverageProfileAtomicallyInjected is writeCoverageProfileAtomically's
// test seam (task-9 PR-6): every production call site reaches it only
// through writeCoverageProfileAtomically, which always passes a nil
// *filewrite.Injector, so production behaviour is unchanged. A test passes
// its own Injector to reach a chmod/write/sync/close/rename failure branch
// deterministically. The buffered fmt.Fprintf writes go through
// bufio.NewWriter(filewrite.Writer(...)) (review-t9-pr6 B1): the whole
// payload (mode plus the block map) is already in memory here, unlike
// extractWorktreeMergeArchive's arbitrary-size tar.Reader stream, but the
// mode/block Fprintf calls and the final Flush are still real writes to
// our own temp file that a test needs to fault-inject, so
// filewrite.Writer -- an io.Writer adapter, not a []byte-at-a-time
// primitive -- makes them injectable without changing what bufio writes
// or how its errors are wrapped.
func writeCoverageProfileAtomicallyInjected(output, mode string, blocks map[string]coverageBlock, inj *filewrite.Injector) (err error) {
	directory := filepath.Dir(output)
	temporary, err := filewrite.CreateTemp(directory, ".wb-coverage-merge-*.tmp", inj)
	if err != nil {
		return fmt.Errorf("create merged coverage profile beside %s: %w", output, err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if temporary != nil {
			err = errors.Join(err, filewrite.Close(temporary, temporaryPath, inj))
		}
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := filewrite.ChmodFile(temporary, 0o644, temporaryPath, inj); err != nil {
		return fmt.Errorf("set merged coverage profile mode: %w", err)
	}
	writer := bufio.NewWriter(filewrite.Writer(temporary, temporaryPath, inj))
	if _, err := fmt.Fprintf(writer, "mode: %s\n", mode); err != nil {
		return err
	}
	locations := make([]string, 0, len(blocks))
	for location := range blocks {
		locations = append(locations, location)
	}
	sort.Strings(locations)
	for _, location := range locations {
		block := blocks[location]
		if _, err := fmt.Fprintf(writer, "%s %d %d\n", block.location, block.statements, block.count); err != nil {
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush merged coverage profile: %w", err)
	}
	if err := filewrite.Sync(temporary, temporaryPath, inj); err != nil {
		return fmt.Errorf("sync merged coverage profile: %w", err)
	}
	if err := filewrite.Close(temporary, temporaryPath, inj); err != nil {
		return fmt.Errorf("close merged coverage profile: %w", err)
	}
	temporary = nil
	if err := filewrite.Rename(temporaryPath, output, inj); err != nil {
		return fmt.Errorf("publish merged coverage profile %s: %w", output, err)
	}
	return nil
}
