// Code generated by protoc-gen-gogo. DO NOT EDIT.
// source: fccontrol.proto

package fccontrol

import (
	context "context"
	fmt "fmt"
	github_com_containerd_ttrpc "github.com/containerd/ttrpc"
	proto1 "github.com/firecracker-microvm/firecracker-containerd/proto"
	proto "github.com/gogo/protobuf/proto"
	types "github.com/gogo/protobuf/types"
	math "math"
)

// Reference imports to suppress errors if they are not otherwise used.
var _ = proto.Marshal
var _ = fmt.Errorf
var _ = math.Inf

// This is a compile-time assertion to ensure that this generated file
// is compatible with the proto package it is being compiled against.
// A compilation error at this line likely means your copy of the
// proto package needs to be updated.
const _ = proto.GoGoProtoPackageIsVersion3 // please upgrade the proto package

func init() { proto.RegisterFile("fccontrol.proto", fileDescriptor_b99f53e2bf82c5ef) }

var fileDescriptor_b99f53e2bf82c5ef = []byte{
	// 357 bytes of a gzipped FileDescriptorProto
	0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0xff, 0x84, 0x93, 0x3f, 0x4f, 0xf2, 0x50,
	0x14, 0xc6, 0xe9, 0xf0, 0xf2, 0xc2, 0x35, 0x08, 0xdc, 0x04, 0x45, 0x4c, 0xba, 0xb8, 0x1f, 0x0c,
	0x3a, 0x33, 0x80, 0x5a, 0x1d, 0x9a, 0x18, 0x88, 0x0c, 0x6e, 0x97, 0x72, 0x4a, 0x88, 0xa5, 0xb7,
	0xb6, 0xa7, 0x83, 0x9b, 0x1f, 0x8f, 0xd1, 0xd1, 0x51, 0xba, 0xfb, 0x1d, 0x0c, 0xb4, 0x97, 0x3f,
	0x85, 0xe4, 0x6e, 0xcf, 0xf9, 0x9d, 0x9e, 0xe7, 0x3c, 0xf7, 0x24, 0x65, 0x55, 0xd7, 0x71, 0xa4,
	0x4f, 0xa1, 0xf4, 0x20, 0x08, 0x25, 0xc9, 0xd6, 0xe5, 0x54, 0xca, 0xa9, 0x87, 0xed, 0x75, 0x35,
	0x8e, 0xdd, 0x36, 0xce, 0x03, 0xfa, 0xc8, 0x9a, 0x75, 0x77, 0x16, 0xa2, 0x13, 0x0a, 0xe7, 0x0d,
	0xc3, 0x14, 0x75, 0x7e, 0xff, 0xb1, 0x93, 0x87, 0x2d, 0xe5, 0x6d, 0x56, 0xea, 0x87, 0x28, 0x08,
	0x47, 0x36, 0xaf, 0x81, 0x92, 0x03, 0x7c, 0x8f, 0x31, 0xa2, 0x56, 0x7d, 0x87, 0x44, 0x81, 0xf4,
	0x23, 0xe4, 0x1d, 0xf6, 0xff, 0x59, 0xc4, 0xd1, 0xea, 0xfb, 0x2a, 0x64, 0x4a, 0x7d, 0x7e, 0x06,
	0x69, 0x1a, 0x50, 0x69, 0xe0, 0x7e, 0x95, 0x86, 0xdf, 0xb2, 0xd2, 0x00, 0xa3, 0x78, 0x9e, 0x2e,
	0x51, 0x52, 0x37, 0x75, 0xcd, 0x8a, 0x43, 0x92, 0xc1, 0xc8, 0xe6, 0xa7, 0x90, 0x0a, 0xdd, 0x44,
	0x87, 0x95, 0x2d, 0xa4, 0x91, 0xfd, 0xe4, 0xbb, 0x92, 0xd7, 0x61, 0xa3, 0xd5, 0x1c, 0xdf, 0x45,
	0xd9, 0x7b, 0xba, 0xac, 0x32, 0x5c, 0x41, 0x1b, 0x49, 0x4c, 0x04, 0x09, 0xde, 0x80, 0xbd, 0x5a,
	0xb7, 0xf3, 0x8e, 0xd5, 0x5e, 0x82, 0xc9, 0xfa, 0x46, 0x1b, 0x8b, 0x26, 0xe4, 0x91, 0xce, 0xa5,
	0xcb, 0x2a, 0x56, 0x2e, 0x85, 0x75, 0x3c, 0x45, 0x0e, 0x67, 0xaf, 0xb0, 0x58, 0xcd, 0x42, 0xea,
	0x09, 0xcf, 0x93, 0xd2, 0xef, 0x4b, 0xdf, 0x9d, 0x4d, 0x79, 0x13, 0xf2, 0x48, 0xb9, 0x5c, 0x1c,
	0xe9, 0x6c, 0xcf, 0x91, 0x66, 0xcf, 0xda, 0xbc, 0x01, 0x7b, 0xb5, 0xfe, 0x1c, 0xd5, 0xad, 0xf7,
	0x90, 0x04, 0x45, 0xfc, 0x1c, 0x72, 0x44, 0x79, 0x34, 0x0f, 0x1b, 0x59, 0x8a, 0x47, 0xc6, 0xf7,
	0xb6, 0xa6, 0x46, 0x2d, 0x38, 0x84, 0x9a, 0x3c, 0xbd, 0xab, 0xc5, 0xd2, 0x2c, 0x7c, 0x2f, 0xcd,
	0xc2, 0x67, 0x62, 0x1a, 0x8b, 0xc4, 0x34, 0xbe, 0x12, 0xd3, 0xf8, 0x49, 0x4c, 0xe3, 0xb5, 0xbc,
	0xf9, 0x95, 0xc6, 0xc5, 0xf5, 0xd0, 0xcd, 0x5f, 0x00, 0x00, 0x00, 0xff, 0xff, 0x66, 0x43, 0x0c,
	0x39, 0x5e, 0x03, 0x00, 0x00,
}

type FirecrackerService interface {
	CreateVM(ctx context.Context, req *proto1.CreateVMRequest) (*proto1.CreateVMResponse, error)
	PauseVM(ctx context.Context, req *proto1.PauseVMRequest) (*types.Empty, error)
	ResumeVM(ctx context.Context, req *proto1.ResumeVMRequest) (*types.Empty, error)
	StopVM(ctx context.Context, req *proto1.StopVMRequest) (*types.Empty, error)
	GetVMInfo(ctx context.Context, req *proto1.GetVMInfoRequest) (*proto1.GetVMInfoResponse, error)
	SetVMMetadata(ctx context.Context, req *proto1.SetVMMetadataRequest) (*types.Empty, error)
	UpdateVMMetadata(ctx context.Context, req *proto1.UpdateVMMetadataRequest) (*types.Empty, error)
	GetVMMetadata(ctx context.Context, req *proto1.GetVMMetadataRequest) (*proto1.GetVMMetadataResponse, error)
	GetBalloonConfig(ctx context.Context, req *proto1.GetBalloonConfigRequest) (*proto1.GetBalloonConfigResponse, error)
	UpdateBalloon(ctx context.Context, req *proto1.UpdateBalloonRequest) (*types.Empty, error)
	GetBalloonStats(ctx context.Context, req *proto1.GetBalloonStatsRequest) (*proto1.GetBalloonStatsResponse, error)
	UpdateBalloonStats(ctx context.Context, req *proto1.UpdateBalloonStatsRequest) (*types.Empty, error)
}

func RegisterFirecrackerService(srv *github_com_containerd_ttrpc.Server, svc FirecrackerService) {
	srv.Register("Firecracker", map[string]github_com_containerd_ttrpc.Method{
		"CreateVM": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
			var req proto1.CreateVMRequest
			if err := unmarshal(&req); err != nil {
				return nil, err
			}
			return svc.CreateVM(ctx, &req)
		},
		"PauseVM": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
			var req proto1.PauseVMRequest
			if err := unmarshal(&req); err != nil {
				return nil, err
			}
			return svc.PauseVM(ctx, &req)
		},
		"ResumeVM": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
			var req proto1.ResumeVMRequest
			if err := unmarshal(&req); err != nil {
				return nil, err
			}
			return svc.ResumeVM(ctx, &req)
		},
		"StopVM": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
			var req proto1.StopVMRequest
			if err := unmarshal(&req); err != nil {
				return nil, err
			}
			return svc.StopVM(ctx, &req)
		},
		"GetVMInfo": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
			var req proto1.GetVMInfoRequest
			if err := unmarshal(&req); err != nil {
				return nil, err
			}
			return svc.GetVMInfo(ctx, &req)
		},
		"SetVMMetadata": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
			var req proto1.SetVMMetadataRequest
			if err := unmarshal(&req); err != nil {
				return nil, err
			}
			return svc.SetVMMetadata(ctx, &req)
		},
		"UpdateVMMetadata": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
			var req proto1.UpdateVMMetadataRequest
			if err := unmarshal(&req); err != nil {
				return nil, err
			}
			return svc.UpdateVMMetadata(ctx, &req)
		},
		"GetVMMetadata": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
			var req proto1.GetVMMetadataRequest
			if err := unmarshal(&req); err != nil {
				return nil, err
			}
			return svc.GetVMMetadata(ctx, &req)
		},
		"GetBalloonConfig": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
			var req proto1.GetBalloonConfigRequest
			if err := unmarshal(&req); err != nil {
				return nil, err
			}
			return svc.GetBalloonConfig(ctx, &req)
		},
		"UpdateBalloon": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
			var req proto1.UpdateBalloonRequest
			if err := unmarshal(&req); err != nil {
				return nil, err
			}
			return svc.UpdateBalloon(ctx, &req)
		},
		"GetBalloonStats": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
			var req proto1.GetBalloonStatsRequest
			if err := unmarshal(&req); err != nil {
				return nil, err
			}
			return svc.GetBalloonStats(ctx, &req)
		},
		"UpdateBalloonStats": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
			var req proto1.UpdateBalloonStatsRequest
			if err := unmarshal(&req); err != nil {
				return nil, err
			}
			return svc.UpdateBalloonStats(ctx, &req)
		},
	})
}

type firecrackerClient struct {
	client *github_com_containerd_ttrpc.Client
}

func NewFirecrackerClient(client *github_com_containerd_ttrpc.Client) FirecrackerService {
	return &firecrackerClient{
		client: client,
	}
}

func (c *firecrackerClient) CreateVM(ctx context.Context, req *proto1.CreateVMRequest) (*proto1.CreateVMResponse, error) {
	var resp proto1.CreateVMResponse
	if err := c.client.Call(ctx, "Firecracker", "CreateVM", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *firecrackerClient) PauseVM(ctx context.Context, req *proto1.PauseVMRequest) (*types.Empty, error) {
	var resp types.Empty
	if err := c.client.Call(ctx, "Firecracker", "PauseVM", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *firecrackerClient) ResumeVM(ctx context.Context, req *proto1.ResumeVMRequest) (*types.Empty, error) {
	var resp types.Empty
	if err := c.client.Call(ctx, "Firecracker", "ResumeVM", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *firecrackerClient) StopVM(ctx context.Context, req *proto1.StopVMRequest) (*types.Empty, error) {
	var resp types.Empty
	if err := c.client.Call(ctx, "Firecracker", "StopVM", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *firecrackerClient) GetVMInfo(ctx context.Context, req *proto1.GetVMInfoRequest) (*proto1.GetVMInfoResponse, error) {
	var resp proto1.GetVMInfoResponse
	if err := c.client.Call(ctx, "Firecracker", "GetVMInfo", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *firecrackerClient) SetVMMetadata(ctx context.Context, req *proto1.SetVMMetadataRequest) (*types.Empty, error) {
	var resp types.Empty
	if err := c.client.Call(ctx, "Firecracker", "SetVMMetadata", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *firecrackerClient) UpdateVMMetadata(ctx context.Context, req *proto1.UpdateVMMetadataRequest) (*types.Empty, error) {
	var resp types.Empty
	if err := c.client.Call(ctx, "Firecracker", "UpdateVMMetadata", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *firecrackerClient) GetVMMetadata(ctx context.Context, req *proto1.GetVMMetadataRequest) (*proto1.GetVMMetadataResponse, error) {
	var resp proto1.GetVMMetadataResponse
	if err := c.client.Call(ctx, "Firecracker", "GetVMMetadata", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *firecrackerClient) GetBalloonConfig(ctx context.Context, req *proto1.GetBalloonConfigRequest) (*proto1.GetBalloonConfigResponse, error) {
	var resp proto1.GetBalloonConfigResponse
	if err := c.client.Call(ctx, "Firecracker", "GetBalloonConfig", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *firecrackerClient) UpdateBalloon(ctx context.Context, req *proto1.UpdateBalloonRequest) (*types.Empty, error) {
	var resp types.Empty
	if err := c.client.Call(ctx, "Firecracker", "UpdateBalloon", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *firecrackerClient) GetBalloonStats(ctx context.Context, req *proto1.GetBalloonStatsRequest) (*proto1.GetBalloonStatsResponse, error) {
	var resp proto1.GetBalloonStatsResponse
	if err := c.client.Call(ctx, "Firecracker", "GetBalloonStats", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *firecrackerClient) UpdateBalloonStats(ctx context.Context, req *proto1.UpdateBalloonStatsRequest) (*types.Empty, error) {
	var resp types.Empty
	if err := c.client.Call(ctx, "Firecracker", "UpdateBalloonStats", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
