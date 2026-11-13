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

# This helper runs inside arbitrary image roots, which may use an older
# glibc or a different libc from the guest and build environments.
volume-init: Makefile ../go.mod ../go.sum $(shell find . -name '*.go')
	CGO_ENABLED=0 go build -trimpath -o $@ ./cmd/volume-init

test:
	go test ./...

clean:
	rm -f volume-init

install:

.PHONY: test clean install
