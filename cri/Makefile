# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License"). You may
# not use this file except in compliance with the License. A copy of the
# License is located at
#
# 	http://aws.amazon.com/apache2.0/
#
# or in the "license" file accompanying this file. This file is distributed
# on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
# express or implied. See the License for the specific language governing
# permissions and limitations under the License.

ifeq ($(origin REVISION),undefined)
REVISION := $(shell git rev-parse HEAD)
endif
SOURCES := $(shell find . ../config ../internal ../proto -name '*.go')

all: firecracker-cri

firecracker-cri: $(SOURCES) go.mod go.sum
	GOWORK=off go build -mod=readonly -trimpath -ldflags '-X main.revision=$(REVISION)' -o $@ ./cmd/firecracker-cri

generate:
	python3 ../tools/critest/generate-forwarders.py --proto "$$(go list -m -f '{{.Dir}}' k8s.io/cri-api)/pkg/apis/runtime/v1/api.proto" --output generated_forward.go
	gofmt -w generated_forward.go

install: firecracker-cri
	install -D -o root -g root -m755 -t $(INSTALLROOT)/bin firecracker-cri

test:
	GOWORK=off go test -mod=readonly -count=1 ./... $(EXTRAGOARGS)

integ-test:
	cd .. && tools/critest/test-cri.sh

clean:
	rm -f firecracker-cri

distclean: clean

.PHONY: all generate install test integ-test clean distclean
