// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.28.1
// 	protoc        v3.21.9
// source: manager/systema.proto

package manager

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

type ConnectionChunk struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// Types that are assignable to Value:
	//	*ConnectionChunk_InterceptId
	//	*ConnectionChunk_Data
	//	*ConnectionChunk_Error
	Value isConnectionChunk_Value `protobuf_oneof:"value"`
}

func (x *ConnectionChunk) Reset() {
	*x = ConnectionChunk{}
	if protoimpl.UnsafeEnabled {
		mi := &file_manager_systema_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *ConnectionChunk) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ConnectionChunk) ProtoMessage() {}

func (x *ConnectionChunk) ProtoReflect() protoreflect.Message {
	mi := &file_manager_systema_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ConnectionChunk.ProtoReflect.Descriptor instead.
func (*ConnectionChunk) Descriptor() ([]byte, []int) {
	return file_manager_systema_proto_rawDescGZIP(), []int{0}
}

func (m *ConnectionChunk) GetValue() isConnectionChunk_Value {
	if m != nil {
		return m.Value
	}
	return nil
}

func (x *ConnectionChunk) GetInterceptId() string {
	if x, ok := x.GetValue().(*ConnectionChunk_InterceptId); ok {
		return x.InterceptId
	}
	return ""
}

func (x *ConnectionChunk) GetData() []byte {
	if x, ok := x.GetValue().(*ConnectionChunk_Data); ok {
		return x.Data
	}
	return nil
}

func (x *ConnectionChunk) GetError() string {
	if x, ok := x.GetValue().(*ConnectionChunk_Error); ok {
		return x.Error
	}
	return ""
}

type isConnectionChunk_Value interface {
	isConnectionChunk_Value()
}

type ConnectionChunk_InterceptId struct {
	InterceptId string `protobuf:"bytes,1,opt,name=intercept_id,json=interceptId,proto3,oneof"`
}

type ConnectionChunk_Data struct {
	Data []byte `protobuf:"bytes,2,opt,name=data,proto3,oneof"`
}

type ConnectionChunk_Error struct {
	Error string `protobuf:"bytes,3,opt,name=error,proto3,oneof"` // TODO: Probably have a better error type
}

func (*ConnectionChunk_InterceptId) isConnectionChunk_Value() {}

func (*ConnectionChunk_Data) isConnectionChunk_Value() {}

func (*ConnectionChunk_Error) isConnectionChunk_Value() {}

var File_manager_systema_proto protoreflect.FileDescriptor

var file_manager_systema_proto_rawDesc = []byte{
	0x0a, 0x15, 0x6d, 0x61, 0x6e, 0x61, 0x67, 0x65, 0x72, 0x2f, 0x73, 0x79, 0x73, 0x74, 0x65, 0x6d,
	0x61, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x14, 0x74, 0x65, 0x6c, 0x65, 0x70, 0x72, 0x65,
	0x73, 0x65, 0x6e, 0x63, 0x65, 0x2e, 0x6d, 0x61, 0x6e, 0x61, 0x67, 0x65, 0x72, 0x22, 0x6d, 0x0a,
	0x0f, 0x43, 0x6f, 0x6e, 0x6e, 0x65, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x43, 0x68, 0x75, 0x6e, 0x6b,
	0x12, 0x23, 0x0a, 0x0c, 0x69, 0x6e, 0x74, 0x65, 0x72, 0x63, 0x65, 0x70, 0x74, 0x5f, 0x69, 0x64,
	0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x48, 0x00, 0x52, 0x0b, 0x69, 0x6e, 0x74, 0x65, 0x72, 0x63,
	0x65, 0x70, 0x74, 0x49, 0x64, 0x12, 0x14, 0x0a, 0x04, 0x64, 0x61, 0x74, 0x61, 0x18, 0x02, 0x20,
	0x01, 0x28, 0x0c, 0x48, 0x00, 0x52, 0x04, 0x64, 0x61, 0x74, 0x61, 0x12, 0x16, 0x0a, 0x05, 0x65,
	0x72, 0x72, 0x6f, 0x72, 0x18, 0x03, 0x20, 0x01, 0x28, 0x09, 0x48, 0x00, 0x52, 0x05, 0x65, 0x72,
	0x72, 0x6f, 0x72, 0x42, 0x07, 0x0a, 0x05, 0x76, 0x61, 0x6c, 0x75, 0x65, 0x32, 0x74, 0x0a, 0x0c,
	0x4d, 0x61, 0x6e, 0x61, 0x67, 0x65, 0x72, 0x50, 0x72, 0x6f, 0x78, 0x79, 0x12, 0x64, 0x0a, 0x10,
	0x48, 0x61, 0x6e, 0x64, 0x6c, 0x65, 0x43, 0x6f, 0x6e, 0x6e, 0x65, 0x63, 0x74, 0x69, 0x6f, 0x6e,
	0x12, 0x25, 0x2e, 0x74, 0x65, 0x6c, 0x65, 0x70, 0x72, 0x65, 0x73, 0x65, 0x6e, 0x63, 0x65, 0x2e,
	0x6d, 0x61, 0x6e, 0x61, 0x67, 0x65, 0x72, 0x2e, 0x43, 0x6f, 0x6e, 0x6e, 0x65, 0x63, 0x74, 0x69,
	0x6f, 0x6e, 0x43, 0x68, 0x75, 0x6e, 0x6b, 0x1a, 0x25, 0x2e, 0x74, 0x65, 0x6c, 0x65, 0x70, 0x72,
	0x65, 0x73, 0x65, 0x6e, 0x63, 0x65, 0x2e, 0x6d, 0x61, 0x6e, 0x61, 0x67, 0x65, 0x72, 0x2e, 0x43,
	0x6f, 0x6e, 0x6e, 0x65, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x43, 0x68, 0x75, 0x6e, 0x6b, 0x28, 0x01,
	0x30, 0x01, 0x42, 0x37, 0x5a, 0x35, 0x67, 0x69, 0x74, 0x68, 0x75, 0x62, 0x2e, 0x63, 0x6f, 0x6d,
	0x2f, 0x74, 0x65, 0x6c, 0x65, 0x70, 0x72, 0x65, 0x73, 0x65, 0x6e, 0x63, 0x65, 0x69, 0x6f, 0x2f,
	0x74, 0x65, 0x6c, 0x65, 0x70, 0x72, 0x65, 0x73, 0x65, 0x6e, 0x63, 0x65, 0x2f, 0x72, 0x70, 0x63,
	0x2f, 0x76, 0x32, 0x2f, 0x6d, 0x61, 0x6e, 0x61, 0x67, 0x65, 0x72, 0x62, 0x06, 0x70, 0x72, 0x6f,
	0x74, 0x6f, 0x33,
}

var (
	file_manager_systema_proto_rawDescOnce sync.Once
	file_manager_systema_proto_rawDescData = file_manager_systema_proto_rawDesc
)

func file_manager_systema_proto_rawDescGZIP() []byte {
	file_manager_systema_proto_rawDescOnce.Do(func() {
		file_manager_systema_proto_rawDescData = protoimpl.X.CompressGZIP(file_manager_systema_proto_rawDescData)
	})
	return file_manager_systema_proto_rawDescData
}

var file_manager_systema_proto_msgTypes = make([]protoimpl.MessageInfo, 1)
var file_manager_systema_proto_goTypes = []interface{}{
	(*ConnectionChunk)(nil), // 0: telepresence.manager.ConnectionChunk
}
var file_manager_systema_proto_depIdxs = []int32{
	0, // 0: telepresence.manager.ManagerProxy.HandleConnection:input_type -> telepresence.manager.ConnectionChunk
	0, // 1: telepresence.manager.ManagerProxy.HandleConnection:output_type -> telepresence.manager.ConnectionChunk
	1, // [1:2] is the sub-list for method output_type
	0, // [0:1] is the sub-list for method input_type
	0, // [0:0] is the sub-list for extension type_name
	0, // [0:0] is the sub-list for extension extendee
	0, // [0:0] is the sub-list for field type_name
}

func init() { file_manager_systema_proto_init() }
func file_manager_systema_proto_init() {
	if File_manager_systema_proto != nil {
		return
	}
	if !protoimpl.UnsafeEnabled {
		file_manager_systema_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*ConnectionChunk); i {
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
	file_manager_systema_proto_msgTypes[0].OneofWrappers = []interface{}{
		(*ConnectionChunk_InterceptId)(nil),
		(*ConnectionChunk_Data)(nil),
		(*ConnectionChunk_Error)(nil),
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_manager_systema_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   1,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_manager_systema_proto_goTypes,
		DependencyIndexes: file_manager_systema_proto_depIdxs,
		MessageInfos:      file_manager_systema_proto_msgTypes,
	}.Build()
	File_manager_systema_proto = out.File
	file_manager_systema_proto_rawDesc = nil
	file_manager_systema_proto_goTypes = nil
	file_manager_systema_proto_depIdxs = nil
}
