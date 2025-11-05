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

package proto

// PrepareShimRequest specifies parameters for preparing a shim
type PrepareShimRequest struct {
	// VM identifier for the shim
	VMID                 string   `protobuf:"bytes,1,opt,name=VMID,proto3" json:"VMID,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *PrepareShimRequest) Reset()         { *m = PrepareShimRequest{} }
func (m *PrepareShimRequest) String() string { return "PrepareShimRequest" }
func (*PrepareShimRequest) ProtoMessage()    {}

func (m *PrepareShimRequest) GetVMID() string {
	if m != nil {
		return m.VMID
	}
	return ""
}

// PrepareShimResponse returns information about the prepared shim
type PrepareShimResponse struct {
	// VM identifier
	VMID string `protobuf:"bytes,1,opt,name=VMID,proto3" json:"VMID,omitempty"`
	// Namespace where the shim was created
	Namespace string `protobuf:"bytes,2,opt,name=Namespace,proto3" json:"Namespace,omitempty"`
	// Socket address for the shim
	ShimSocketAddress string `protobuf:"bytes,3,opt,name=ShimSocketAddress,proto3" json:"ShimSocketAddress,omitempty"`
	// Socket address for fccontrol
	FCSocketAddress      string   `protobuf:"bytes,4,opt,name=FCSocketAddress,proto3" json:"FCSocketAddress,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *PrepareShimResponse) Reset()         { *m = PrepareShimResponse{} }
func (m *PrepareShimResponse) String() string { return "PrepareShimResponse" }
func (*PrepareShimResponse) ProtoMessage()    {}

func (m *PrepareShimResponse) GetVMID() string {
	if m != nil {
		return m.VMID
	}
	return ""
}

func (m *PrepareShimResponse) GetNamespace() string {
	if m != nil {
		return m.Namespace
	}
	return ""
}

func (m *PrepareShimResponse) GetShimSocketAddress() string {
	if m != nil {
		return m.ShimSocketAddress
	}
	return ""
}

func (m *PrepareShimResponse) GetFCSocketAddress() string {
	if m != nil {
		return m.FCSocketAddress
	}
	return ""
}

// RemoveShimRequest specifies parameters for removing a prepared shim
type RemoveShimRequest struct {
	// VM identifier for the shim to remove
	VMID                 string   `protobuf:"bytes,1,opt,name=VMID,proto3" json:"VMID,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *RemoveShimRequest) Reset()         { *m = RemoveShimRequest{} }
func (m *RemoveShimRequest) String() string { return "RemoveShimRequest" }
func (*RemoveShimRequest) ProtoMessage()    {}

func (m *RemoveShimRequest) GetVMID() string {
	if m != nil {
		return m.VMID
	}
	return ""
}
