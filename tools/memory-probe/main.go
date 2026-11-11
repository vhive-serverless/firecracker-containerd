// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// fc-memory-probe observes file mappings and anonymous memory without reclaiming them.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	maxMapping    = 256 << 20
	bytesField    = "bytes"
	verifiedField = "verified"
)

type options struct {
	file      string
	statOnly  bool
	fileBytes int64
	anonMiB   int
	idle      time.Duration
}

type fileMetadata struct {
	Path           string `json:"path"`
	StatxMask      uint32 `json:"statx_mask"`
	Attributes     uint64 `json:"attributes"`
	AttributesMask uint64 `json:"attributes_mask"`
	DAXSupported   bool   `json:"statx_dax_supported"`
	DAX            bool   `json:"statx_dax"`
	Size           uint64 `json:"size_bytes"`
	Mode           uint16 `json:"mode"`
	UID            uint32 `json:"uid"`
	GID            uint32 `json:"gid"`
	Inode          uint64 `json:"inode"`
	Links          uint32 `json:"links"`
	Blocks         uint64 `json:"blocks_512_bytes"`
	BlockSize      uint32 `json:"block_size_bytes"`
	DeviceMajor    uint32 `json:"device_major"`
	DeviceMinor    uint32 `json:"device_minor"`
	MountID        uint64 `json:"mount_id"`
	MtimeSec       int64  `json:"mtime_unix_sec"`
	MtimeNsec      uint32 `json:"mtime_nsec"`
}

type usage struct {
	UserUS      int64 `json:"user_cpu_us"`
	SystemUS    int64 `json:"system_cpu_us"`
	MinorFaults int64 `json:"minor_faults"`
	MajorFaults int64 `json:"major_faults"`
}

func readUsage() (usage, error) {
	var value unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &value); err != nil {
		return usage{}, err
	}
	return usage{value.Utime.Sec*1e6 + value.Utime.Usec, value.Stime.Sec*1e6 + value.Stime.Usec, value.Minflt, value.Majflt}, nil
}

func boundedRead(path string, limit int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > limit {
		return "", fmt.Errorf("%s exceeds sample size limit", path)
	}
	return strings.TrimSpace(string(data)), nil
}

func sampleMemory() (map[string]string, map[string]string, []string) {
	meminfo, damon := map[string]string{}, map[string]string{}
	var sampleErrors []string
	data, err := boundedRead("/proc/meminfo", 64<<10)
	if err != nil {
		sampleErrors = append(sampleErrors, err.Error())
	} else {
		for line := range strings.SplitSeq(data, "\n") {
			key, value, ok := strings.Cut(line, ":")
			if ok {
				meminfo[key] = strings.TrimSpace(value)
			}
		}
	}
	for _, module := range []string{"damon_reclaim", "damon_lru_sort"} {
		root := filepath.Join("/sys/module", module, "parameters")
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			sampleErrors = append(sampleErrors, err.Error())
			continue
		}
		for i, entry := range entries {
			if i >= 128 {
				sampleErrors = append(sampleErrors, root+": parameter limit reached")
				break
			}
			value, err := boundedRead(filepath.Join(root, entry.Name()), 1024)
			if err != nil {
				sampleErrors = append(sampleErrors, err.Error())
				continue
			}
			damon[module+"/"+entry.Name()] = value
		}
	}
	return meminfo, damon, sampleErrors
}

type reporter struct {
	encoder         *json.Encoder
	start, previous time.Time
	previousUsage   usage
}

func (r *reporter) emit(phase string, fields map[string]any) error {
	now := time.Now()
	current, err := readUsage()
	if err != nil {
		return err
	}
	meminfo, damon, sampleErrors := sampleMemory()
	fields["phase"], fields["timestamp_utc"] = phase, now.UTC().Format(time.RFC3339Nano)
	fields["elapsed_ns"], fields["phase_duration_ns"] = now.Sub(r.start).Nanoseconds(), now.Sub(r.previous).Nanoseconds()
	fields["rusage_total"] = current
	fields["rusage_delta"] = usage{current.UserUS - r.previousUsage.UserUS, current.SystemUS - r.previousUsage.SystemUS, current.MinorFaults - r.previousUsage.MinorFaults, current.MajorFaults - r.previousUsage.MajorFaults}
	fields["meminfo"], fields["damon_parameters"] = meminfo, damon
	fields["damon_parameters_available"] = len(damon) != 0
	if len(sampleErrors) != 0 {
		fields["sample_errors"] = sampleErrors
	}
	r.previous, r.previousUsage = now, current
	return r.encoder.Encode(fields)
}

func parseOptions(args []string) (options, error) {
	var opts options
	flags := flag.NewFlagSet("fc-memory-probe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.file, "file", "", "existing file to inspect or map read-only")
	flags.BoolVar(&opts.statOnly, "stat-only", false, "report statx metadata without mapping")
	flags.Int64Var(&opts.fileBytes, "file-bytes", 0, "prefix length; 0 selects the whole file (maximum 256 MiB)")
	flags.IntVar(&opts.anonMiB, "anon-mib", 32, "anonymous canary size, 1..256 MiB")
	flags.DurationVar(&opts.idle, "idle", 5*time.Second, "idle duration, 0..1h")
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	if flags.NArg() != 0 || opts.file == "" {
		return opts, errors.New("-file PATH is required; positional arguments are unsupported")
	}
	if opts.fileBytes < 0 || opts.fileBytes > maxMapping {
		return opts, errors.New("-file-bytes must be 0..268435456")
	}
	if opts.anonMiB < 1 || opts.anonMiB > 256 {
		return opts, errors.New("-anon-mib must be 1..256")
	}
	if opts.idle < 0 || opts.idle > time.Hour {
		return opts, errors.New("-idle must be 0..1h")
	}
	return opts, nil
}

func inspectFile(fd int, path string) (fileMetadata, error) {
	var st unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &st); err != nil {
		return fileMetadata{}, fmt.Errorf("statx: %w", err)
	}
	return fileMetadata{Path: path, StatxMask: st.Mask, Attributes: st.Attributes, AttributesMask: st.Attributes_mask,
		DAXSupported: st.Attributes_mask&unix.STATX_ATTR_DAX != 0, DAX: st.Attributes&unix.STATX_ATTR_DAX != 0,
		Size: st.Size, Mode: st.Mode, UID: st.Uid, GID: st.Gid, Inode: st.Ino, Links: st.Nlink, Blocks: st.Blocks, BlockSize: st.Blksize,
		DeviceMajor: st.Dev_major, DeviceMinor: st.Dev_minor, MountID: st.Mnt_id, MtimeSec: st.Mtime.Sec, MtimeNsec: st.Mtime.Nsec}, nil
}

func canaryByte(offset int) byte { return byte(uint64(offset)*131 + uint64(offset>>12)*17 + 0x5a) }

func verifyCanary(data []byte) error {
	for i, value := range data {
		if value != canaryByte(i) {
			return fmt.Errorf("anonymous canary mismatch at byte %d", i)
		}
	}
	return nil
}

func execute(args []string, out io.Writer) error {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}
	// O_NONBLOCK prevents a FIFO argument from hanging before its type is checked.
	file, err := os.OpenFile(opts.file, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	metadata, err := inspectFile(int(file.Fd()), opts.file)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	if opts.statOnly {
		return encoder.Encode(metadata)
	}
	if metadata.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("mapping requires a regular file")
	}
	length := metadata.Size
	if opts.fileBytes != 0 {
		length = uint64(opts.fileBytes)
	}
	if length == 0 || length > metadata.Size || length > maxMapping {
		return errors.New("mapped file prefix must be nonempty, within file size and at most 256 MiB")
	}
	start := time.Now()
	baseline, err := readUsage()
	if err != nil {
		return err
	}
	r := reporter{encoder: encoder, start: start, previous: start, previousUsage: baseline}
	if err := r.emit("stat", map[string]any{"file": metadata, "file_bytes": length, "anon_bytes": opts.anonMiB << 20, "idle_ns": opts.idle.Nanoseconds()}); err != nil {
		return err
	}
	mapped, err := unix.Mmap(int(file.Fd()), 0, int(length), unix.PROT_READ, unix.MAP_PRIVATE)
	if err != nil {
		return fmt.Errorf("file mmap: %w", err)
	}
	defer unix.Munmap(mapped)
	before := sha256.Sum256(mapped)
	if err := r.emit("file_touch", map[string]any{"sha256": hex.EncodeToString(before[:]), bytesField: length}); err != nil {
		return err
	}
	anonymous, err := unix.Mmap(-1, 0, opts.anonMiB<<20, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANON)
	if err != nil {
		return fmt.Errorf("anonymous mmap: %w", err)
	}
	defer unix.Munmap(anonymous)
	for i := range anonymous {
		anonymous[i] = canaryByte(i)
	}
	if err := r.emit("anon_fill", map[string]any{bytesField: len(anonymous)}); err != nil {
		return err
	}
	if err := r.emit("idle_start", map[string]any{}); err != nil {
		return err
	}
	time.Sleep(opts.idle)
	if err := r.emit("idle_end", map[string]any{}); err != nil {
		return err
	}
	after := sha256.Sum256(mapped)
	if err := r.emit("file_retouch", map[string]any{"sha256": hex.EncodeToString(after[:]), bytesField: length, verifiedField: before == after}); err != nil {
		return err
	}
	if before != after {
		return errors.New("mapped file checksum changed")
	}
	if err := verifyCanary(anonymous); err != nil {
		return err
	}
	if err := r.emit("anon_verify", map[string]any{bytesField: len(anonymous), verifiedField: true}); err != nil {
		return err
	}
	return r.emit("complete", map[string]any{verifiedField: true})
}

func main() {
	if err := execute(os.Args[1:], os.Stdout); err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"phase": "error", "error": err.Error()})
		os.Exit(1)
	}
}
