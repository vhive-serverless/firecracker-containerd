// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//	http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package cri

import (
	"context"
	"io"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

type runtimeForwarder struct {
	runtimeapi.UnimplementedRuntimeServiceServer
	backend runtimeapi.RuntimeServiceClient
}

type imageForwarder struct {
	runtimeapi.UnimplementedImageServiceServer
	backend runtimeapi.ImageServiceClient
}

func backendContext(ctx context.Context) context.Context {
	incoming, _ := metadata.FromIncomingContext(ctx)
	outgoing := incoming.Copy()
	for key := range outgoing {
		if strings.HasPrefix(key, ":") || strings.HasPrefix(key, "grpc-") || key == "content-type" || key == "user-agent" {
			delete(outgoing, key)
		}
	}
	return metadata.NewOutgoingContext(ctx, outgoing)
}

func forwardUnary[Request, Response any](ctx context.Context, request *Request,
	call func(context.Context, *Request, ...grpc.CallOption) (*Response, error)) (*Response, error) {
	var headers, trailers metadata.MD
	response, err := call(backendContext(ctx), request, grpc.Header(&headers), grpc.Trailer(&trailers))
	if len(headers) != 0 {
		_ = grpc.SendHeader(ctx, headers)
	}
	grpc.SetTrailer(ctx, trailers)
	return response, err
}

func forwardServerStream[Request, Response any](request *Request, output grpc.ServerStreamingServer[Response],
	call func(context.Context, *Request, ...grpc.CallOption) (grpc.ServerStreamingClient[Response], error)) error {
	input, err := call(backendContext(output.Context()), request)
	if err != nil {
		return err
	}
	defer func() { output.SetTrailer(input.Trailer()) }()
	headers, err := input.Header()
	if err != nil {
		return err
	}
	if err := output.SendHeader(headers); err != nil {
		return err
	}
	for {
		message, err := input.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := output.Send(message); err != nil {
			return err
		}
	}
}
