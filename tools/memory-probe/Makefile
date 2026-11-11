# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

fc-memory-probe: Makefile $(wildcard *.go) ../../go.mod ../../go.sum
	CGO_ENABLED=0 go build -mod=readonly -trimpath -buildvcs=false -o $@ .

test:
	go test -mod=readonly -count=1 ./...

clean:
	rm -f fc-memory-probe

.PHONY: test clean
