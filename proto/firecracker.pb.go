// Code generated by protoc-gen-gogo. DO NOT EDIT.
// source: firecracker.proto

package proto

import (
	fmt "fmt"
	proto "github.com/gogo/protobuf/proto"
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

// DriveExposePolicy is used to configure the method to expose drive files.
// "COPY" is copying the files to the jail, which is the default behavior.
// "BIND" is bind-mounting the files on the jail, assuming a caller pre-configures the permissions of
// the files appropriately.
type DriveExposePolicy int32

const (
	DriveExposePolicy_COPY DriveExposePolicy = 0
	DriveExposePolicy_BIND DriveExposePolicy = 1
)

var DriveExposePolicy_name = map[int32]string{
	0: "COPY",
	1: "BIND",
}

var DriveExposePolicy_value = map[string]int32{
	"COPY": 0,
	"BIND": 1,
}

func (x DriveExposePolicy) String() string {
	return proto.EnumName(DriveExposePolicy_name, int32(x))
}

func (DriveExposePolicy) EnumDescriptor() ([]byte, []int) {
	return fileDescriptor_a73317e9fb8da571, []int{0}
}

// CreateVMRequest specifies creation parameters for a new FC instance
type CreateVMRequest struct {
	// VM identifier to assign
	VMID string `protobuf:"bytes,1,opt,name=VMID,json=vMID,proto3" json:"VMID,omitempty"`
	// Specifies the machine configuration for the VM
	MachineCfg *FirecrackerMachineConfiguration `protobuf:"bytes,2,opt,name=MachineCfg,json=machineCfg,proto3" json:"MachineCfg,omitempty"`
	// Specifies the file path where the kernel image is located
	KernelImagePath string `protobuf:"bytes,3,opt,name=KernelImagePath,json=kernelImagePath,proto3" json:"KernelImagePath,omitempty"`
	// Specifies the commandline arguments that should be passed to the kernel
	KernelArgs string `protobuf:"bytes,4,opt,name=KernelArgs,json=kernelArgs,proto3" json:"KernelArgs,omitempty"`
	// Specifies drive containing the rootfs of the VM
	RootDrive *FirecrackerRootDrive `protobuf:"bytes,5,opt,name=RootDrive,json=rootDrive,proto3" json:"RootDrive,omitempty"`
	// Specifies additional drives whose contents will be mounted inside the VM on boot.
	DriveMounts []*FirecrackerDriveMount `protobuf:"bytes,6,rep,name=DriveMounts,json=driveMounts,proto3" json:"DriveMounts,omitempty"`
	// Specifies the networking configuration for a VM
	NetworkInterfaces []*FirecrackerNetworkInterface `protobuf:"bytes,7,rep,name=NetworkInterfaces,json=networkInterfaces,proto3" json:"NetworkInterfaces,omitempty"`
	// The number of dummy drives to reserve in advance before running FC instance.
	ContainerCount int32 `protobuf:"varint,8,opt,name=ContainerCount,json=containerCount,proto3" json:"ContainerCount,omitempty"`
	// Whether the VM should exit after all tasks running in it have been deleted.
	ExitAfterAllTasksDeleted bool          `protobuf:"varint,9,opt,name=ExitAfterAllTasksDeleted,json=exitAfterAllTasksDeleted,proto3" json:"ExitAfterAllTasksDeleted,omitempty"`
	JailerConfig             *JailerConfig `protobuf:"bytes,10,opt,name=JailerConfig,json=jailerConfig,proto3" json:"JailerConfig,omitempty"`
	TimeoutSeconds           uint32        `protobuf:"varint,11,opt,name=TimeoutSeconds,json=timeoutSeconds,proto3" json:"TimeoutSeconds,omitempty"`
	XXX_NoUnkeyedLiteral     struct{}      `json:"-"`
	XXX_unrecognized         []byte        `json:"-"`
	XXX_sizecache            int32         `json:"-"`
}

func (m *CreateVMRequest) Reset()         { *m = CreateVMRequest{} }
func (m *CreateVMRequest) String() string { return proto.CompactTextString(m) }
func (*CreateVMRequest) ProtoMessage()    {}
func (*CreateVMRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_a73317e9fb8da571, []int{0}
}
func (m *CreateVMRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_CreateVMRequest.Unmarshal(m, b)
}
func (m *CreateVMRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_CreateVMRequest.Marshal(b, m, deterministic)
}
func (m *CreateVMRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_CreateVMRequest.Merge(m, src)
}
func (m *CreateVMRequest) XXX_Size() int {
	return xxx_messageInfo_CreateVMRequest.Size(m)
}
func (m *CreateVMRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_CreateVMRequest.DiscardUnknown(m)
}

var xxx_messageInfo_CreateVMRequest proto.InternalMessageInfo

func (m *CreateVMRequest) GetVMID() string {
	if m != nil {
		return m.VMID
	}
	return ""
}

func (m *CreateVMRequest) GetMachineCfg() *FirecrackerMachineConfiguration {
	if m != nil {
		return m.MachineCfg
	}
	return nil
}

func (m *CreateVMRequest) GetKernelImagePath() string {
	if m != nil {
		return m.KernelImagePath
	}
	return ""
}

func (m *CreateVMRequest) GetKernelArgs() string {
	if m != nil {
		return m.KernelArgs
	}
	return ""
}

func (m *CreateVMRequest) GetRootDrive() *FirecrackerRootDrive {
	if m != nil {
		return m.RootDrive
	}
	return nil
}

func (m *CreateVMRequest) GetDriveMounts() []*FirecrackerDriveMount {
	if m != nil {
		return m.DriveMounts
	}
	return nil
}

func (m *CreateVMRequest) GetNetworkInterfaces() []*FirecrackerNetworkInterface {
	if m != nil {
		return m.NetworkInterfaces
	}
	return nil
}

func (m *CreateVMRequest) GetContainerCount() int32 {
	if m != nil {
		return m.ContainerCount
	}
	return 0
}

func (m *CreateVMRequest) GetExitAfterAllTasksDeleted() bool {
	if m != nil {
		return m.ExitAfterAllTasksDeleted
	}
	return false
}

func (m *CreateVMRequest) GetJailerConfig() *JailerConfig {
	if m != nil {
		return m.JailerConfig
	}
	return nil
}

func (m *CreateVMRequest) GetTimeoutSeconds() uint32 {
	if m != nil {
		return m.TimeoutSeconds
	}
	return 0
}

type CreateVMResponse struct {
	VMID                 string   `protobuf:"bytes,1,opt,name=VMID,json=vMID,proto3" json:"VMID,omitempty"`
	SocketPath           string   `protobuf:"bytes,2,opt,name=SocketPath,json=socketPath,proto3" json:"SocketPath,omitempty"`
	LogFifoPath          string   `protobuf:"bytes,3,opt,name=LogFifoPath,json=logFifoPath,proto3" json:"LogFifoPath,omitempty"`
	MetricsFifoPath      string   `protobuf:"bytes,4,opt,name=MetricsFifoPath,json=metricsFifoPath,proto3" json:"MetricsFifoPath,omitempty"`
	CgroupPath           string   `protobuf:"bytes,5,opt,name=CgroupPath,json=cgroupPath,proto3" json:"CgroupPath,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *CreateVMResponse) Reset()         { *m = CreateVMResponse{} }
func (m *CreateVMResponse) String() string { return proto.CompactTextString(m) }
func (*CreateVMResponse) ProtoMessage()    {}
func (*CreateVMResponse) Descriptor() ([]byte, []int) {
	return fileDescriptor_a73317e9fb8da571, []int{1}
}
func (m *CreateVMResponse) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_CreateVMResponse.Unmarshal(m, b)
}
func (m *CreateVMResponse) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_CreateVMResponse.Marshal(b, m, deterministic)
}
func (m *CreateVMResponse) XXX_Merge(src proto.Message) {
	xxx_messageInfo_CreateVMResponse.Merge(m, src)
}
func (m *CreateVMResponse) XXX_Size() int {
	return xxx_messageInfo_CreateVMResponse.Size(m)
}
func (m *CreateVMResponse) XXX_DiscardUnknown() {
	xxx_messageInfo_CreateVMResponse.DiscardUnknown(m)
}

var xxx_messageInfo_CreateVMResponse proto.InternalMessageInfo

func (m *CreateVMResponse) GetVMID() string {
	if m != nil {
		return m.VMID
	}
	return ""
}

func (m *CreateVMResponse) GetSocketPath() string {
	if m != nil {
		return m.SocketPath
	}
	return ""
}

func (m *CreateVMResponse) GetLogFifoPath() string {
	if m != nil {
		return m.LogFifoPath
	}
	return ""
}

func (m *CreateVMResponse) GetMetricsFifoPath() string {
	if m != nil {
		return m.MetricsFifoPath
	}
	return ""
}

func (m *CreateVMResponse) GetCgroupPath() string {
	if m != nil {
		return m.CgroupPath
	}
	return ""
}

type StopVMRequest struct {
	VMID                 string   `protobuf:"bytes,1,opt,name=VMID,json=vMID,proto3" json:"VMID,omitempty"`
	TimeoutSeconds       uint32   `protobuf:"varint,2,opt,name=TimeoutSeconds,json=timeoutSeconds,proto3" json:"TimeoutSeconds,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *StopVMRequest) Reset()         { *m = StopVMRequest{} }
func (m *StopVMRequest) String() string { return proto.CompactTextString(m) }
func (*StopVMRequest) ProtoMessage()    {}
func (*StopVMRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_a73317e9fb8da571, []int{2}
}
func (m *StopVMRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_StopVMRequest.Unmarshal(m, b)
}
func (m *StopVMRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_StopVMRequest.Marshal(b, m, deterministic)
}
func (m *StopVMRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_StopVMRequest.Merge(m, src)
}
func (m *StopVMRequest) XXX_Size() int {
	return xxx_messageInfo_StopVMRequest.Size(m)
}
func (m *StopVMRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_StopVMRequest.DiscardUnknown(m)
}

var xxx_messageInfo_StopVMRequest proto.InternalMessageInfo

func (m *StopVMRequest) GetVMID() string {
	if m != nil {
		return m.VMID
	}
	return ""
}

func (m *StopVMRequest) GetTimeoutSeconds() uint32 {
	if m != nil {
		return m.TimeoutSeconds
	}
	return 0
}

type GetVMInfoRequest struct {
	VMID                 string   `protobuf:"bytes,1,opt,name=VMID,json=vMID,proto3" json:"VMID,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *GetVMInfoRequest) Reset()         { *m = GetVMInfoRequest{} }
func (m *GetVMInfoRequest) String() string { return proto.CompactTextString(m) }
func (*GetVMInfoRequest) ProtoMessage()    {}
func (*GetVMInfoRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_a73317e9fb8da571, []int{3}
}
func (m *GetVMInfoRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_GetVMInfoRequest.Unmarshal(m, b)
}
func (m *GetVMInfoRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_GetVMInfoRequest.Marshal(b, m, deterministic)
}
func (m *GetVMInfoRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_GetVMInfoRequest.Merge(m, src)
}
func (m *GetVMInfoRequest) XXX_Size() int {
	return xxx_messageInfo_GetVMInfoRequest.Size(m)
}
func (m *GetVMInfoRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_GetVMInfoRequest.DiscardUnknown(m)
}

var xxx_messageInfo_GetVMInfoRequest proto.InternalMessageInfo

func (m *GetVMInfoRequest) GetVMID() string {
	if m != nil {
		return m.VMID
	}
	return ""
}

type GetVMInfoResponse struct {
	VMID                 string   `protobuf:"bytes,1,opt,name=VMID,json=vMID,proto3" json:"VMID,omitempty"`
	SocketPath           string   `protobuf:"bytes,2,opt,name=SocketPath,json=socketPath,proto3" json:"SocketPath,omitempty"`
	LogFifoPath          string   `protobuf:"bytes,3,opt,name=LogFifoPath,json=logFifoPath,proto3" json:"LogFifoPath,omitempty"`
	MetricsFifoPath      string   `protobuf:"bytes,4,opt,name=MetricsFifoPath,json=metricsFifoPath,proto3" json:"MetricsFifoPath,omitempty"`
	CgroupPath           string   `protobuf:"bytes,5,opt,name=CgroupPath,json=cgroupPath,proto3" json:"CgroupPath,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *GetVMInfoResponse) Reset()         { *m = GetVMInfoResponse{} }
func (m *GetVMInfoResponse) String() string { return proto.CompactTextString(m) }
func (*GetVMInfoResponse) ProtoMessage()    {}
func (*GetVMInfoResponse) Descriptor() ([]byte, []int) {
	return fileDescriptor_a73317e9fb8da571, []int{4}
}
func (m *GetVMInfoResponse) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_GetVMInfoResponse.Unmarshal(m, b)
}
func (m *GetVMInfoResponse) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_GetVMInfoResponse.Marshal(b, m, deterministic)
}
func (m *GetVMInfoResponse) XXX_Merge(src proto.Message) {
	xxx_messageInfo_GetVMInfoResponse.Merge(m, src)
}
func (m *GetVMInfoResponse) XXX_Size() int {
	return xxx_messageInfo_GetVMInfoResponse.Size(m)
}
func (m *GetVMInfoResponse) XXX_DiscardUnknown() {
	xxx_messageInfo_GetVMInfoResponse.DiscardUnknown(m)
}

var xxx_messageInfo_GetVMInfoResponse proto.InternalMessageInfo

func (m *GetVMInfoResponse) GetVMID() string {
	if m != nil {
		return m.VMID
	}
	return ""
}

func (m *GetVMInfoResponse) GetSocketPath() string {
	if m != nil {
		return m.SocketPath
	}
	return ""
}

func (m *GetVMInfoResponse) GetLogFifoPath() string {
	if m != nil {
		return m.LogFifoPath
	}
	return ""
}

func (m *GetVMInfoResponse) GetMetricsFifoPath() string {
	if m != nil {
		return m.MetricsFifoPath
	}
	return ""
}

func (m *GetVMInfoResponse) GetCgroupPath() string {
	if m != nil {
		return m.CgroupPath
	}
	return ""
}

type SetVMMetadataRequest struct {
	VMID                 string   `protobuf:"bytes,1,opt,name=VMID,json=vMID,proto3" json:"VMID,omitempty"`
	Metadata             string   `protobuf:"bytes,2,opt,name=Metadata,json=metadata,proto3" json:"Metadata,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *SetVMMetadataRequest) Reset()         { *m = SetVMMetadataRequest{} }
func (m *SetVMMetadataRequest) String() string { return proto.CompactTextString(m) }
func (*SetVMMetadataRequest) ProtoMessage()    {}
func (*SetVMMetadataRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_a73317e9fb8da571, []int{5}
}
func (m *SetVMMetadataRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_SetVMMetadataRequest.Unmarshal(m, b)
}
func (m *SetVMMetadataRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_SetVMMetadataRequest.Marshal(b, m, deterministic)
}
func (m *SetVMMetadataRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_SetVMMetadataRequest.Merge(m, src)
}
func (m *SetVMMetadataRequest) XXX_Size() int {
	return xxx_messageInfo_SetVMMetadataRequest.Size(m)
}
func (m *SetVMMetadataRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_SetVMMetadataRequest.DiscardUnknown(m)
}

var xxx_messageInfo_SetVMMetadataRequest proto.InternalMessageInfo

func (m *SetVMMetadataRequest) GetVMID() string {
	if m != nil {
		return m.VMID
	}
	return ""
}

func (m *SetVMMetadataRequest) GetMetadata() string {
	if m != nil {
		return m.Metadata
	}
	return ""
}

type UpdateVMMetadataRequest struct {
	VMID                 string   `protobuf:"bytes,1,opt,name=VMID,json=vMID,proto3" json:"VMID,omitempty"`
	Metadata             string   `protobuf:"bytes,2,opt,name=Metadata,json=metadata,proto3" json:"Metadata,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *UpdateVMMetadataRequest) Reset()         { *m = UpdateVMMetadataRequest{} }
func (m *UpdateVMMetadataRequest) String() string { return proto.CompactTextString(m) }
func (*UpdateVMMetadataRequest) ProtoMessage()    {}
func (*UpdateVMMetadataRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_a73317e9fb8da571, []int{6}
}
func (m *UpdateVMMetadataRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_UpdateVMMetadataRequest.Unmarshal(m, b)
}
func (m *UpdateVMMetadataRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_UpdateVMMetadataRequest.Marshal(b, m, deterministic)
}
func (m *UpdateVMMetadataRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_UpdateVMMetadataRequest.Merge(m, src)
}
func (m *UpdateVMMetadataRequest) XXX_Size() int {
	return xxx_messageInfo_UpdateVMMetadataRequest.Size(m)
}
func (m *UpdateVMMetadataRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_UpdateVMMetadataRequest.DiscardUnknown(m)
}

var xxx_messageInfo_UpdateVMMetadataRequest proto.InternalMessageInfo

func (m *UpdateVMMetadataRequest) GetVMID() string {
	if m != nil {
		return m.VMID
	}
	return ""
}

func (m *UpdateVMMetadataRequest) GetMetadata() string {
	if m != nil {
		return m.Metadata
	}
	return ""
}

type GetVMMetadataRequest struct {
	VMID                 string   `protobuf:"bytes,1,opt,name=VMID,json=vMID,proto3" json:"VMID,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *GetVMMetadataRequest) Reset()         { *m = GetVMMetadataRequest{} }
func (m *GetVMMetadataRequest) String() string { return proto.CompactTextString(m) }
func (*GetVMMetadataRequest) ProtoMessage()    {}
func (*GetVMMetadataRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_a73317e9fb8da571, []int{7}
}
func (m *GetVMMetadataRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_GetVMMetadataRequest.Unmarshal(m, b)
}
func (m *GetVMMetadataRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_GetVMMetadataRequest.Marshal(b, m, deterministic)
}
func (m *GetVMMetadataRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_GetVMMetadataRequest.Merge(m, src)
}
func (m *GetVMMetadataRequest) XXX_Size() int {
	return xxx_messageInfo_GetVMMetadataRequest.Size(m)
}
func (m *GetVMMetadataRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_GetVMMetadataRequest.DiscardUnknown(m)
}

var xxx_messageInfo_GetVMMetadataRequest proto.InternalMessageInfo

func (m *GetVMMetadataRequest) GetVMID() string {
	if m != nil {
		return m.VMID
	}
	return ""
}

type GetVMMetadataResponse struct {
	Metadata             string   `protobuf:"bytes,1,opt,name=Metadata,json=metadata,proto3" json:"Metadata,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *GetVMMetadataResponse) Reset()         { *m = GetVMMetadataResponse{} }
func (m *GetVMMetadataResponse) String() string { return proto.CompactTextString(m) }
func (*GetVMMetadataResponse) ProtoMessage()    {}
func (*GetVMMetadataResponse) Descriptor() ([]byte, []int) {
	return fileDescriptor_a73317e9fb8da571, []int{8}
}
func (m *GetVMMetadataResponse) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_GetVMMetadataResponse.Unmarshal(m, b)
}
func (m *GetVMMetadataResponse) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_GetVMMetadataResponse.Marshal(b, m, deterministic)
}
func (m *GetVMMetadataResponse) XXX_Merge(src proto.Message) {
	xxx_messageInfo_GetVMMetadataResponse.Merge(m, src)
}
func (m *GetVMMetadataResponse) XXX_Size() int {
	return xxx_messageInfo_GetVMMetadataResponse.Size(m)
}
func (m *GetVMMetadataResponse) XXX_DiscardUnknown() {
	xxx_messageInfo_GetVMMetadataResponse.DiscardUnknown(m)
}

var xxx_messageInfo_GetVMMetadataResponse proto.InternalMessageInfo

func (m *GetVMMetadataResponse) GetMetadata() string {
	if m != nil {
		return m.Metadata
	}
	return ""
}

type PauseVMRequest struct {
	VMID                 string   `protobuf:"bytes,1,opt,name=VMID,json=vMID,proto3" json:"VMID,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *PauseVMRequest) Reset()         { *m = PauseVMRequest{} }
func (m *PauseVMRequest) String() string { return proto.CompactTextString(m) }
func (*PauseVMRequest) ProtoMessage()    {}
func (*PauseVMRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_a73317e9fb8da571, []int{9}
}
func (m *PauseVMRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_PauseVMRequest.Unmarshal(m, b)
}
func (m *PauseVMRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_PauseVMRequest.Marshal(b, m, deterministic)
}
func (m *PauseVMRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_PauseVMRequest.Merge(m, src)
}
func (m *PauseVMRequest) XXX_Size() int {
	return xxx_messageInfo_PauseVMRequest.Size(m)
}
func (m *PauseVMRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_PauseVMRequest.DiscardUnknown(m)
}

var xxx_messageInfo_PauseVMRequest proto.InternalMessageInfo

func (m *PauseVMRequest) GetVMID() string {
	if m != nil {
		return m.VMID
	}
	return ""
}

type ResumeVMRequest struct {
	VMID                 string   `protobuf:"bytes,1,opt,name=VMID,json=vMID,proto3" json:"VMID,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *ResumeVMRequest) Reset()         { *m = ResumeVMRequest{} }
func (m *ResumeVMRequest) String() string { return proto.CompactTextString(m) }
func (*ResumeVMRequest) ProtoMessage()    {}
func (*ResumeVMRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_a73317e9fb8da571, []int{10}
}
func (m *ResumeVMRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_ResumeVMRequest.Unmarshal(m, b)
}
func (m *ResumeVMRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_ResumeVMRequest.Marshal(b, m, deterministic)
}
func (m *ResumeVMRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_ResumeVMRequest.Merge(m, src)
}
func (m *ResumeVMRequest) XXX_Size() int {
	return xxx_messageInfo_ResumeVMRequest.Size(m)
}
func (m *ResumeVMRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_ResumeVMRequest.DiscardUnknown(m)
}

var xxx_messageInfo_ResumeVMRequest proto.InternalMessageInfo

func (m *ResumeVMRequest) GetVMID() string {
	if m != nil {
		return m.VMID
	}
	return ""
}

type JailerConfig struct {
	NetNS string `protobuf:"bytes,1,opt,name=NetNS,json=netNS,proto3" json:"NetNS,omitempty"`
	// List of the physical numbers of the CPUs on which processes in that
	// cpuset are allowed to execute.  See List Format below for a description
	// of the format of cpus.
	//
	// The CPUs allowed to a cpuset may be changed by writing a new list to its
	// cpus file.
	// Taken from http://man7.org/linux/man-pages/man7/cpuset.7.html
	//
	// This is formatted as specified in the cpuset man page under "List Format"
	// http://man7.org/linux/man-pages/man7/cpuset.7.html
	CPUs string `protobuf:"bytes,2,opt,name=CPUs,json=cPUs,proto3" json:"CPUs,omitempty"`
	// List of memory nodes on which processes in this cpuset are allowed to
	// allocate memory.  See List Format below for a description of the format
	// of mems.
	// Taken from http://man7.org/linux/man-pages/man7/cpuset.7.html
	//
	// This is formatted as specified in the cpuset man page under "List Format"
	// http://man7.org/linux/man-pages/man7/cpuset.7.html
	Mems string `protobuf:"bytes,3,opt,name=Mems,json=mems,proto3" json:"Mems,omitempty"`
	UID  uint32 `protobuf:"varint,4,opt,name=UID,json=uID,proto3" json:"UID,omitempty"`
	GID  uint32 `protobuf:"varint,5,opt,name=GID,json=gID,proto3" json:"GID,omitempty"`
	// CgroupPath is used to dictate where the cgroup should be located
	// relative to the cgroup directory which is
	// /sys/fs/cgroup/cpu/<CgroupPath>/<vmID>
	// if no value was provided, then /firecracker-containerd will be used as
	// the default value
	CgroupPath string `protobuf:"bytes,6,opt,name=CgroupPath,json=cgroupPath,proto3" json:"CgroupPath,omitempty"`
	// DriveExposePolicy is used to configure the method to expose drive files.
	DriveExposePolicy    DriveExposePolicy `protobuf:"varint,7,opt,name=DriveExposePolicy,json=driveExposePolicy,proto3,enum=DriveExposePolicy" json:"DriveExposePolicy,omitempty"`
	XXX_NoUnkeyedLiteral struct{}          `json:"-"`
	XXX_unrecognized     []byte            `json:"-"`
	XXX_sizecache        int32             `json:"-"`
}

func (m *JailerConfig) Reset()         { *m = JailerConfig{} }
func (m *JailerConfig) String() string { return proto.CompactTextString(m) }
func (*JailerConfig) ProtoMessage()    {}
func (*JailerConfig) Descriptor() ([]byte, []int) {
	return fileDescriptor_a73317e9fb8da571, []int{11}
}
func (m *JailerConfig) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_JailerConfig.Unmarshal(m, b)
}
func (m *JailerConfig) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_JailerConfig.Marshal(b, m, deterministic)
}
func (m *JailerConfig) XXX_Merge(src proto.Message) {
	xxx_messageInfo_JailerConfig.Merge(m, src)
}
func (m *JailerConfig) XXX_Size() int {
	return xxx_messageInfo_JailerConfig.Size(m)
}
func (m *JailerConfig) XXX_DiscardUnknown() {
	xxx_messageInfo_JailerConfig.DiscardUnknown(m)
}

var xxx_messageInfo_JailerConfig proto.InternalMessageInfo

func (m *JailerConfig) GetNetNS() string {
	if m != nil {
		return m.NetNS
	}
	return ""
}

func (m *JailerConfig) GetCPUs() string {
	if m != nil {
		return m.CPUs
	}
	return ""
}

func (m *JailerConfig) GetMems() string {
	if m != nil {
		return m.Mems
	}
	return ""
}

func (m *JailerConfig) GetUID() uint32 {
	if m != nil {
		return m.UID
	}
	return 0
}

func (m *JailerConfig) GetGID() uint32 {
	if m != nil {
		return m.GID
	}
	return 0
}

func (m *JailerConfig) GetCgroupPath() string {
	if m != nil {
		return m.CgroupPath
	}
	return ""
}

func (m *JailerConfig) GetDriveExposePolicy() DriveExposePolicy {
	if m != nil {
		return m.DriveExposePolicy
	}
	return DriveExposePolicy_COPY
}

func init() {
	proto.RegisterEnum("DriveExposePolicy", DriveExposePolicy_name, DriveExposePolicy_value)
	proto.RegisterType((*CreateVMRequest)(nil), "CreateVMRequest")
	proto.RegisterType((*CreateVMResponse)(nil), "CreateVMResponse")
	proto.RegisterType((*StopVMRequest)(nil), "StopVMRequest")
	proto.RegisterType((*GetVMInfoRequest)(nil), "GetVMInfoRequest")
	proto.RegisterType((*GetVMInfoResponse)(nil), "GetVMInfoResponse")
	proto.RegisterType((*SetVMMetadataRequest)(nil), "SetVMMetadataRequest")
	proto.RegisterType((*UpdateVMMetadataRequest)(nil), "UpdateVMMetadataRequest")
	proto.RegisterType((*GetVMMetadataRequest)(nil), "GetVMMetadataRequest")
	proto.RegisterType((*GetVMMetadataResponse)(nil), "GetVMMetadataResponse")
	proto.RegisterType((*PauseVMRequest)(nil), "PauseVMRequest")
	proto.RegisterType((*ResumeVMRequest)(nil), "ResumeVMRequest")
	proto.RegisterType((*JailerConfig)(nil), "JailerConfig")
}

func init() { proto.RegisterFile("firecracker.proto", fileDescriptor_a73317e9fb8da571) }

var fileDescriptor_a73317e9fb8da571 = []byte{
	// 662 bytes of a gzipped FileDescriptorProto
	0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0xff, 0xd4, 0x55, 0xcd, 0x4e, 0xdb, 0x4c,
	0x14, 0xfd, 0x4c, 0x12, 0x48, 0xae, 0xc9, 0xdf, 0x08, 0xbe, 0x8e, 0x50, 0x85, 0xac, 0x2c, 0xa8,
	0xc5, 0x22, 0x52, 0x61, 0x53, 0x75, 0x05, 0xc4, 0x80, 0x0c, 0x75, 0x1a, 0x4d, 0x08, 0x52, 0xbb,
	0x73, 0x9d, 0x9b, 0xe0, 0x26, 0xf6, 0xb8, 0x33, 0x13, 0x0a, 0xcf, 0xd4, 0x5d, 0xfb, 0x26, 0x7d,
	0xa2, 0xca, 0x93, 0x90, 0x38, 0x81, 0xa2, 0x4a, 0x5d, 0x75, 0x95, 0xb9, 0xe7, 0x9e, 0xfb, 0xa3,
	0x7b, 0x4e, 0x12, 0xa8, 0x0f, 0x42, 0x81, 0x81, 0xf0, 0x83, 0x11, 0x8a, 0x66, 0x22, 0xb8, 0xe2,
	0x3b, 0xa6, 0xba, 0x4f, 0x50, 0x4e, 0x83, 0xc6, 0xb7, 0x3c, 0x54, 0x5b, 0x02, 0x7d, 0x85, 0xd7,
	0x1e, 0xc3, 0x2f, 0x13, 0x94, 0x8a, 0x10, 0xc8, 0x5f, 0x7b, 0xae, 0x43, 0x0d, 0xcb, 0xb0, 0x4b,
	0x2c, 0x7f, 0xeb, 0xb9, 0x0e, 0x39, 0x02, 0xf0, 0xfc, 0xe0, 0x26, 0x8c, 0xb1, 0x35, 0x18, 0xd2,
	0x35, 0xcb, 0xb0, 0xcd, 0x03, 0xab, 0x79, 0xb6, 0x68, 0xfe, 0x90, 0xe5, 0xf1, 0x20, 0x1c, 0x4e,
	0x84, 0xaf, 0x42, 0x1e, 0x33, 0x88, 0xe6, 0x35, 0xc4, 0x86, 0xea, 0x25, 0x8a, 0x18, 0xc7, 0x6e,
	0xe4, 0x0f, 0xb1, 0xe3, 0xab, 0x1b, 0x9a, 0xd3, 0x03, 0xaa, 0xa3, 0x65, 0x98, 0xec, 0x02, 0x4c,
	0x99, 0xc7, 0x62, 0x28, 0x69, 0x5e, 0x93, 0x60, 0x34, 0x47, 0xc8, 0x21, 0x94, 0x18, 0xe7, 0xca,
	0x11, 0xe1, 0x2d, 0xd2, 0x82, 0x5e, 0x65, 0x3b, 0xbb, 0xca, 0x3c, 0xc9, 0x4a, 0xe2, 0xe1, 0x49,
	0xde, 0x80, 0xa9, 0x1f, 0x1e, 0x9f, 0xc4, 0x4a, 0xd2, 0x75, 0x2b, 0x67, 0x9b, 0x07, 0xff, 0x67,
	0xcb, 0x16, 0x69, 0x66, 0xf6, 0x17, 0x54, 0x72, 0x01, 0xf5, 0x36, 0xaa, 0xaf, 0x5c, 0x8c, 0xdc,
	0x58, 0xa1, 0x18, 0xf8, 0x01, 0x4a, 0xba, 0xa1, 0xeb, 0x5f, 0x66, 0xeb, 0x57, 0x49, 0xac, 0x1e,
	0xaf, 0x96, 0x91, 0x3d, 0xa8, 0xb4, 0x78, 0xac, 0xfc, 0x30, 0x46, 0xd1, 0x4a, 0xdb, 0xd3, 0xa2,
	0x65, 0xd8, 0x05, 0x56, 0x09, 0x96, 0x50, 0xf2, 0x16, 0xe8, 0xe9, 0x5d, 0xa8, 0x8e, 0x07, 0x0a,
	0xc5, 0xf1, 0x78, 0x7c, 0xe5, 0xcb, 0x91, 0x74, 0x70, 0x8c, 0x0a, 0xfb, 0xb4, 0x64, 0x19, 0x76,
	0x91, 0x51, 0xfc, 0x4d, 0x9e, 0xbc, 0x86, 0xcd, 0x0b, 0x3f, 0x1c, 0xa7, 0xad, 0x52, 0x2d, 0x28,
	0xe8, 0x0b, 0x95, 0x9b, 0x59, 0x90, 0x6d, 0x7e, 0xce, 0x44, 0xe9, 0x5a, 0x57, 0x61, 0x84, 0x7c,
	0xa2, 0xba, 0x18, 0xf0, 0xb8, 0x2f, 0xa9, 0x69, 0x19, 0x76, 0x99, 0x55, 0xd4, 0x12, 0xda, 0xf8,
	0x6e, 0x40, 0x6d, 0xe1, 0x16, 0x99, 0xf0, 0x58, 0xe2, 0x93, 0x76, 0xd9, 0x05, 0xe8, 0xf2, 0x60,
	0x84, 0x4a, 0xeb, 0xbc, 0x36, 0x95, 0x50, 0xce, 0x11, 0x62, 0x81, 0xf9, 0x8e, 0x0f, 0xcf, 0xc2,
	0x01, 0xcf, 0x18, 0xc1, 0x1c, 0x2f, 0xa0, 0xd4, 0x2e, 0x1e, 0x2a, 0x11, 0x06, 0x72, 0xce, 0x9a,
	0x3a, 0xa1, 0x1a, 0x2d, 0xc3, 0xe9, 0xac, 0xd6, 0x50, 0xf0, 0x49, 0xa2, 0x49, 0x85, 0xe9, 0xac,
	0x60, 0x8e, 0x34, 0x2e, 0xa1, 0xdc, 0x55, 0x3c, 0x79, 0xde, 0xdf, 0x8f, 0x2f, 0xb0, 0xf6, 0xe4,
	0x05, 0xf6, 0xa0, 0x76, 0x8e, 0xea, 0xda, 0x73, 0xe3, 0x01, 0x7f, 0xa6, 0x5f, 0xe3, 0x87, 0x01,
	0xf5, 0x0c, 0xf1, 0x1f, 0x39, 0xd5, 0x19, 0x6c, 0x75, 0xd3, 0xa5, 0x3d, 0x54, 0x7e, 0xdf, 0x57,
	0xfe, 0x73, 0x17, 0xdb, 0x81, 0xe2, 0x03, 0x6d, 0xb6, 0x75, 0x31, 0x9a, 0xc5, 0x0d, 0x17, 0x5e,
	0xf4, 0x92, 0xbe, 0xb6, 0xc9, 0xdf, 0xb6, 0xda, 0x87, 0xad, 0xf3, 0x3f, 0x5c, 0xa9, 0x71, 0x08,
	0xdb, 0x2b, 0xdc, 0xd9, 0xdd, 0xb3, 0x03, 0x8c, 0x95, 0x01, 0x3f, 0x8d, 0xe5, 0xef, 0x0b, 0xd9,
	0x82, 0x42, 0x1b, 0x55, 0xbb, 0x3b, 0x63, 0x16, 0xe2, 0x34, 0x48, 0xe7, 0xb5, 0x3a, 0x3d, 0x39,
	0xdb, 0x2f, 0x1f, 0x74, 0x7a, 0x32, 0xc5, 0x3c, 0x8c, 0xe4, 0x4c, 0x93, 0x7c, 0x84, 0x91, 0x24,
	0x35, 0xc8, 0xf5, 0x5c, 0x47, 0x0b, 0x50, 0x66, 0xb9, 0x89, 0xeb, 0xa4, 0xc8, 0xb9, 0xeb, 0xe8,
	0x6b, 0x97, 0x59, 0x6e, 0x38, 0x95, 0x3c, 0x23, 0xc3, 0xfa, 0xaa, 0x0c, 0xe4, 0x08, 0xea, 0xfa,
	0xc7, 0xe8, 0xf4, 0x2e, 0xe1, 0x12, 0x3b, 0x7c, 0x1c, 0x06, 0xf7, 0x74, 0xc3, 0x32, 0xec, 0xca,
	0x01, 0x69, 0x3e, 0xca, 0xb0, 0x7a, 0x7f, 0x15, 0xda, 0x7f, 0xf5, 0x44, 0x07, 0x52, 0x84, 0x7c,
	0xeb, 0x7d, 0xe7, 0x43, 0xed, 0xbf, 0xf4, 0x75, 0xe2, 0xb6, 0x9d, 0x9a, 0x71, 0xb2, 0xf1, 0xb1,
	0xa0, 0xff, 0x08, 0x3e, 0xad, 0xeb, 0x8f, 0xc3, 0x5f, 0x01, 0x00, 0x00, 0xff, 0xff, 0x0a, 0x62,
	0xff, 0x90, 0x31, 0x06, 0x00, 0x00,
}
