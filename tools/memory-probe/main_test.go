// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mapped")
	contents := bytes.Repeat([]byte("guest-memory-probe\n"), 8192)
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := execute([]string{"-file", path, "-file-bytes", "257", "-anon-mib", "1", "-idle", "1ms"}, &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	wantPhases := []string{"stat", "file_touch", "anon_fill", "idle_start", "idle_end", "file_retouch", "anon_verify", "complete"}
	wantHash := sha256.Sum256(contents[:257])
	var priorElapsed float64
	for _, phase := range wantPhases {
		var event map[string]any
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event["phase"] != phase {
			t.Fatalf("phase = %v, want %s", event["phase"], phase)
		}
		elapsed, ok := event["elapsed_ns"].(float64)
		if !ok || elapsed < priorElapsed {
			t.Fatalf("invalid phase timing: %v", event)
		}
		priorElapsed = elapsed
		for _, key := range []string{"rusage_total", "rusage_delta", "meminfo", "damon_parameters"} {
			if _, ok := event[key].(map[string]any); !ok {
				t.Fatalf("missing sample %s: %v", key, event)
			}
		}
		if phase == "file_touch" || phase == "file_retouch" {
			if event["sha256"] != hex.EncodeToString(wantHash[:]) {
				t.Fatalf("wrong mapped prefix hash: %v", event)
			}
		}
		if phase == "complete" && event["verified"] != true {
			t.Fatalf("verification missing: %v", event)
		}
	}
	if decoder.More() {
		t.Fatal("unexpected trailing event")
	}
}

func TestStatOnlyEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := execute([]string{"-file", path, "-stat-only"}, &output); err != nil {
		t.Fatal(err)
	}
	var metadata fileMetadata
	if err := json.Unmarshal(output.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Size != 0 || metadata.Path != path || metadata.Mode&unix.S_IFMT != unix.S_IFREG {
		t.Fatalf("bad stat metadata: %+v", metadata)
	}
}

func TestMalformedArguments(t *testing.T) {
	cases := [][]string{
		{}, {"-file", "/unused", "extra"}, {"-file", "/unused", "-unknown"},
		{"-file", "/unused", "-file-bytes", "-1"}, {"-file", "/unused", "-file-bytes", "268435457"},
		{"-file", "/unused", "-anon-mib", "0"}, {"-file", "/unused", "-anon-mib", "257"},
		{"-file", "/unused", "-idle", "-1s"}, {"-file", "/unused", "-idle", "1h1s"},
		{"-file", "/unused", "-idle", "invalid"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, err := parseOptions(args); err == nil {
				t.Fatalf("accepted invalid args: %v", args)
			}
		})
	}
}

func TestRejectInvalidMapping(t *testing.T) {
	root := t.TempDir()
	empty, fifo := filepath.Join(root, "empty"), filepath.Join(root, "fifo")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{empty, fifo, root} {
		if err := execute([]string{"-file", path}, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted invalid mapping: %s", path)
		}
	}
}

func TestCanaryDetectsCorruption(t *testing.T) {
	data := make([]byte, 8193)
	for i := range data {
		data[i] = canaryByte(i)
	}
	if err := verifyCanary(data); err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{0, 4096, len(data) - 1} {
		data[index] ^= 1
		if err := verifyCanary(data); err == nil {
			t.Fatalf("missed corruption at %d", index)
		}
		data[index] ^= 1
	}
}
