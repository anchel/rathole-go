// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.33.0
// 	protoc        v5.26.1
// source: proto/basic.proto

package basicpb

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type PROTO_VERSION int32

const (
	PROTO_VERSION_V0 PROTO_VERSION = 0
	PROTO_VERSION_V1 PROTO_VERSION = 1
)

// Enum value maps for PROTO_VERSION.
var (
	PROTO_VERSION_name = map[int32]string{
		0: "V0",
		1: "V1",
	}
	PROTO_VERSION_value = map[string]int32{
		"V0": 0,
		"V1": 1,
	}
)

func (x PROTO_VERSION) Enum() *PROTO_VERSION {
	p := new(PROTO_VERSION)
	*p = x
	return p
}

func (x PROTO_VERSION) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (PROTO_VERSION) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_basic_proto_enumTypes[0].Descriptor()
}

func (PROTO_VERSION) Type() protoreflect.EnumType {
	return &file_proto_basic_proto_enumTypes[0]
}

func (x PROTO_VERSION) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use PROTO_VERSION.Descriptor instead.
func (PROTO_VERSION) EnumDescriptor() ([]byte, []int) {
	return file_proto_basic_proto_rawDescGZIP(), []int{0}
}

type HelloMessage struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Ver    PROTO_VERSION `protobuf:"varint,1,opt,name=ver,proto3,enum=basic.PROTO_VERSION" json:"ver,omitempty"`
	Digest string        `protobuf:"bytes,2,opt,name=digest,proto3" json:"digest,omitempty"`
}

func (x *HelloMessage) Reset() {
	*x = HelloMessage{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proto_basic_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *HelloMessage) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*HelloMessage) ProtoMessage() {}

func (x *HelloMessage) ProtoReflect() protoreflect.Message {
	mi := &file_proto_basic_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use HelloMessage.ProtoReflect.Descriptor instead.
func (*HelloMessage) Descriptor() ([]byte, []int) {
	return file_proto_basic_proto_rawDescGZIP(), []int{0}
}

func (x *HelloMessage) GetVer() PROTO_VERSION {
	if x != nil {
		return x.Ver
	}
	return PROTO_VERSION_V0
}

func (x *HelloMessage) GetDigest() string {
	if x != nil {
		return x.Digest
	}
	return ""
}

var File_proto_basic_proto protoreflect.FileDescriptor

var file_proto_basic_proto_rawDesc = []byte{
	0x0a, 0x11, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2f, 0x62, 0x61, 0x73, 0x69, 0x63, 0x2e, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x12, 0x05, 0x62, 0x61, 0x73, 0x69, 0x63, 0x22, 0x4e, 0x0a, 0x0c, 0x48, 0x65,
	0x6c, 0x6c, 0x6f, 0x4d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x12, 0x26, 0x0a, 0x03, 0x76, 0x65,
	0x72, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0e, 0x32, 0x14, 0x2e, 0x62, 0x61, 0x73, 0x69, 0x63, 0x2e,
	0x50, 0x52, 0x4f, 0x54, 0x4f, 0x5f, 0x56, 0x45, 0x52, 0x53, 0x49, 0x4f, 0x4e, 0x52, 0x03, 0x76,
	0x65, 0x72, 0x12, 0x16, 0x0a, 0x06, 0x64, 0x69, 0x67, 0x65, 0x73, 0x74, 0x18, 0x02, 0x20, 0x01,
	0x28, 0x09, 0x52, 0x06, 0x64, 0x69, 0x67, 0x65, 0x73, 0x74, 0x2a, 0x1f, 0x0a, 0x0d, 0x50, 0x52,
	0x4f, 0x54, 0x4f, 0x5f, 0x56, 0x45, 0x52, 0x53, 0x49, 0x4f, 0x4e, 0x12, 0x06, 0x0a, 0x02, 0x56,
	0x30, 0x10, 0x00, 0x12, 0x06, 0x0a, 0x02, 0x56, 0x31, 0x10, 0x01, 0x42, 0x0f, 0x5a, 0x0d, 0x62,
	0x61, 0x73, 0x69, 0x63, 0x2f, 0x62, 0x61, 0x73, 0x69, 0x63, 0x70, 0x62, 0x62, 0x06, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_proto_basic_proto_rawDescOnce sync.Once
	file_proto_basic_proto_rawDescData = file_proto_basic_proto_rawDesc
)

func file_proto_basic_proto_rawDescGZIP() []byte {
	file_proto_basic_proto_rawDescOnce.Do(func() {
		file_proto_basic_proto_rawDescData = protoimpl.X.CompressGZIP(file_proto_basic_proto_rawDescData)
	})
	return file_proto_basic_proto_rawDescData
}

var file_proto_basic_proto_enumTypes = make([]protoimpl.EnumInfo, 1)
var file_proto_basic_proto_msgTypes = make([]protoimpl.MessageInfo, 1)
var file_proto_basic_proto_goTypes = []interface{}{
	(PROTO_VERSION)(0),   // 0: basic.PROTO_VERSION
	(*HelloMessage)(nil), // 1: basic.HelloMessage
}
var file_proto_basic_proto_depIdxs = []int32{
	0, // 0: basic.HelloMessage.ver:type_name -> basic.PROTO_VERSION
	1, // [1:1] is the sub-list for method output_type
	1, // [1:1] is the sub-list for method input_type
	1, // [1:1] is the sub-list for extension type_name
	1, // [1:1] is the sub-list for extension extendee
	0, // [0:1] is the sub-list for field type_name
}

func init() { file_proto_basic_proto_init() }
func file_proto_basic_proto_init() {
	if File_proto_basic_proto != nil {
		return
	}
	if !protoimpl.UnsafeEnabled {
		file_proto_basic_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*HelloMessage); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_proto_basic_proto_rawDesc,
			NumEnums:      1,
			NumMessages:   1,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_proto_basic_proto_goTypes,
		DependencyIndexes: file_proto_basic_proto_depIdxs,
		EnumInfos:         file_proto_basic_proto_enumTypes,
		MessageInfos:      file_proto_basic_proto_msgTypes,
	}.Build()
	File_proto_basic_proto = out.File
	file_proto_basic_proto_rawDesc = nil
	file_proto_basic_proto_goTypes = nil
	file_proto_basic_proto_depIdxs = nil
}