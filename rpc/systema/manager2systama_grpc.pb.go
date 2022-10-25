// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.2.0
// - protoc             v3.21.5
// source: rpc/systema/manager2systama.proto

package systema

import (
	context "context"
	common "github.com/telepresenceio/telepresence/rpc/v2/common"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.32.0 or later.
const _ = grpc.SupportPackageIsVersion7

// SystemACRUDClient is the client API for SystemACRUD service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type SystemACRUDClient interface {
	// CreateDomain requires that the manager authenticate using an
	// end-user's API key, to perform the action on behalf of that
	// user.
	CreateDomain(ctx context.Context, in *CreateDomainRequest, opts ...grpc.CallOption) (*CreateDomainResponse, error)
	// RemoveDomain removes a domain that was previously created by the
	// same manager using CreateDomain.  The manager can take this
	// action itself, not on behalf of the user that created the domain,
	// so this requires that the manager authenticate itself, but does
	// not require an end-user's API key.
	RemoveDomain(ctx context.Context, in *RemoveDomainRequest, opts ...grpc.CallOption) (*emptypb.Empty, error)
	// RemoveIntercept is used to inform AmbassadorCloud (SystemA) that an
	// intercept has been removed.
	RemoveIntercept(ctx context.Context, in *InterceptRemoval, opts ...grpc.CallOption) (*emptypb.Empty, error)
	// PreferredAgent returns the active account's perferred agent
	// sidecar, for the given Telepresence version.
	PreferredAgent(ctx context.Context, in *common.VersionInfo, opts ...grpc.CallOption) (*PreferredAgentResponse, error)
}

type systemACRUDClient struct {
	cc grpc.ClientConnInterface
}

func NewSystemACRUDClient(cc grpc.ClientConnInterface) SystemACRUDClient {
	return &systemACRUDClient{cc}
}

func (c *systemACRUDClient) CreateDomain(ctx context.Context, in *CreateDomainRequest, opts ...grpc.CallOption) (*CreateDomainResponse, error) {
	out := new(CreateDomainResponse)
	err := c.cc.Invoke(ctx, "/telepresence.systema.SystemACRUD/CreateDomain", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *systemACRUDClient) RemoveDomain(ctx context.Context, in *RemoveDomainRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	out := new(emptypb.Empty)
	err := c.cc.Invoke(ctx, "/telepresence.systema.SystemACRUD/RemoveDomain", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *systemACRUDClient) RemoveIntercept(ctx context.Context, in *InterceptRemoval, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	out := new(emptypb.Empty)
	err := c.cc.Invoke(ctx, "/telepresence.systema.SystemACRUD/RemoveIntercept", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *systemACRUDClient) PreferredAgent(ctx context.Context, in *common.VersionInfo, opts ...grpc.CallOption) (*PreferredAgentResponse, error) {
	out := new(PreferredAgentResponse)
	err := c.cc.Invoke(ctx, "/telepresence.systema.SystemACRUD/PreferredAgent", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SystemACRUDServer is the server API for SystemACRUD service.
// All implementations must embed UnimplementedSystemACRUDServer
// for forward compatibility
type SystemACRUDServer interface {
	// CreateDomain requires that the manager authenticate using an
	// end-user's API key, to perform the action on behalf of that
	// user.
	CreateDomain(context.Context, *CreateDomainRequest) (*CreateDomainResponse, error)
	// RemoveDomain removes a domain that was previously created by the
	// same manager using CreateDomain.  The manager can take this
	// action itself, not on behalf of the user that created the domain,
	// so this requires that the manager authenticate itself, but does
	// not require an end-user's API key.
	RemoveDomain(context.Context, *RemoveDomainRequest) (*emptypb.Empty, error)
	// RemoveIntercept is used to inform AmbassadorCloud (SystemA) that an
	// intercept has been removed.
	RemoveIntercept(context.Context, *InterceptRemoval) (*emptypb.Empty, error)
	// PreferredAgent returns the active account's perferred agent
	// sidecar, for the given Telepresence version.
	PreferredAgent(context.Context, *common.VersionInfo) (*PreferredAgentResponse, error)
	mustEmbedUnimplementedSystemACRUDServer()
}

// UnimplementedSystemACRUDServer must be embedded to have forward compatible implementations.
type UnimplementedSystemACRUDServer struct {
}

func (UnimplementedSystemACRUDServer) CreateDomain(context.Context, *CreateDomainRequest) (*CreateDomainResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method CreateDomain not implemented")
}
func (UnimplementedSystemACRUDServer) RemoveDomain(context.Context, *RemoveDomainRequest) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method RemoveDomain not implemented")
}
func (UnimplementedSystemACRUDServer) RemoveIntercept(context.Context, *InterceptRemoval) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method RemoveIntercept not implemented")
}
func (UnimplementedSystemACRUDServer) PreferredAgent(context.Context, *common.VersionInfo) (*PreferredAgentResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method PreferredAgent not implemented")
}
func (UnimplementedSystemACRUDServer) mustEmbedUnimplementedSystemACRUDServer() {}

// UnsafeSystemACRUDServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to SystemACRUDServer will
// result in compilation errors.
type UnsafeSystemACRUDServer interface {
	mustEmbedUnimplementedSystemACRUDServer()
}

func RegisterSystemACRUDServer(s grpc.ServiceRegistrar, srv SystemACRUDServer) {
	s.RegisterService(&SystemACRUD_ServiceDesc, srv)
}

func _SystemACRUD_CreateDomain_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(CreateDomainRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SystemACRUDServer).CreateDomain(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/telepresence.systema.SystemACRUD/CreateDomain",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SystemACRUDServer).CreateDomain(ctx, req.(*CreateDomainRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SystemACRUD_RemoveDomain_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(RemoveDomainRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SystemACRUDServer).RemoveDomain(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/telepresence.systema.SystemACRUD/RemoveDomain",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SystemACRUDServer).RemoveDomain(ctx, req.(*RemoveDomainRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SystemACRUD_RemoveIntercept_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(InterceptRemoval)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SystemACRUDServer).RemoveIntercept(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/telepresence.systema.SystemACRUD/RemoveIntercept",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SystemACRUDServer).RemoveIntercept(ctx, req.(*InterceptRemoval))
	}
	return interceptor(ctx, in, info, handler)
}

func _SystemACRUD_PreferredAgent_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(common.VersionInfo)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SystemACRUDServer).PreferredAgent(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/telepresence.systema.SystemACRUD/PreferredAgent",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SystemACRUDServer).PreferredAgent(ctx, req.(*common.VersionInfo))
	}
	return interceptor(ctx, in, info, handler)
}

// SystemACRUD_ServiceDesc is the grpc.ServiceDesc for SystemACRUD service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var SystemACRUD_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "telepresence.systema.SystemACRUD",
	HandlerType: (*SystemACRUDServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "CreateDomain",
			Handler:    _SystemACRUD_CreateDomain_Handler,
		},
		{
			MethodName: "RemoveDomain",
			Handler:    _SystemACRUD_RemoveDomain_Handler,
		},
		{
			MethodName: "RemoveIntercept",
			Handler:    _SystemACRUD_RemoveIntercept_Handler,
		},
		{
			MethodName: "PreferredAgent",
			Handler:    _SystemACRUD_PreferredAgent_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "rpc/systema/manager2systama.proto",
}

// SystemAProxyClient is the client API for SystemAProxy service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type SystemAProxyClient interface {
	// ReverseConnection establishes a stream that is used for System A
	// to send gRPC requests back to the manager.  This requires that
	// the manager authenticate itself, but does not require an
	// end-user's token.
	ReverseConnection(ctx context.Context, opts ...grpc.CallOption) (SystemAProxy_ReverseConnectionClient, error)
}

type systemAProxyClient struct {
	cc grpc.ClientConnInterface
}

func NewSystemAProxyClient(cc grpc.ClientConnInterface) SystemAProxyClient {
	return &systemAProxyClient{cc}
}

func (c *systemAProxyClient) ReverseConnection(ctx context.Context, opts ...grpc.CallOption) (SystemAProxy_ReverseConnectionClient, error) {
	stream, err := c.cc.NewStream(ctx, &SystemAProxy_ServiceDesc.Streams[0], "/telepresence.systema.SystemAProxy/ReverseConnection", opts...)
	if err != nil {
		return nil, err
	}
	x := &systemAProxyReverseConnectionClient{stream}
	return x, nil
}

type SystemAProxy_ReverseConnectionClient interface {
	Send(*Chunk) error
	Recv() (*Chunk, error)
	grpc.ClientStream
}

type systemAProxyReverseConnectionClient struct {
	grpc.ClientStream
}

func (x *systemAProxyReverseConnectionClient) Send(m *Chunk) error {
	return x.ClientStream.SendMsg(m)
}

func (x *systemAProxyReverseConnectionClient) Recv() (*Chunk, error) {
	m := new(Chunk)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

// SystemAProxyServer is the server API for SystemAProxy service.
// All implementations must embed UnimplementedSystemAProxyServer
// for forward compatibility
type SystemAProxyServer interface {
	// ReverseConnection establishes a stream that is used for System A
	// to send gRPC requests back to the manager.  This requires that
	// the manager authenticate itself, but does not require an
	// end-user's token.
	ReverseConnection(SystemAProxy_ReverseConnectionServer) error
	mustEmbedUnimplementedSystemAProxyServer()
}

// UnimplementedSystemAProxyServer must be embedded to have forward compatible implementations.
type UnimplementedSystemAProxyServer struct {
}

func (UnimplementedSystemAProxyServer) ReverseConnection(SystemAProxy_ReverseConnectionServer) error {
	return status.Errorf(codes.Unimplemented, "method ReverseConnection not implemented")
}
func (UnimplementedSystemAProxyServer) mustEmbedUnimplementedSystemAProxyServer() {}

// UnsafeSystemAProxyServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to SystemAProxyServer will
// result in compilation errors.
type UnsafeSystemAProxyServer interface {
	mustEmbedUnimplementedSystemAProxyServer()
}

func RegisterSystemAProxyServer(s grpc.ServiceRegistrar, srv SystemAProxyServer) {
	s.RegisterService(&SystemAProxy_ServiceDesc, srv)
}

func _SystemAProxy_ReverseConnection_Handler(srv interface{}, stream grpc.ServerStream) error {
	return srv.(SystemAProxyServer).ReverseConnection(&systemAProxyReverseConnectionServer{stream})
}

type SystemAProxy_ReverseConnectionServer interface {
	Send(*Chunk) error
	Recv() (*Chunk, error)
	grpc.ServerStream
}

type systemAProxyReverseConnectionServer struct {
	grpc.ServerStream
}

func (x *systemAProxyReverseConnectionServer) Send(m *Chunk) error {
	return x.ServerStream.SendMsg(m)
}

func (x *systemAProxyReverseConnectionServer) Recv() (*Chunk, error) {
	m := new(Chunk)
	if err := x.ServerStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

// SystemAProxy_ServiceDesc is the grpc.ServiceDesc for SystemAProxy service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var SystemAProxy_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "telepresence.systema.SystemAProxy",
	HandlerType: (*SystemAProxyServer)(nil),
	Methods:     []grpc.MethodDesc{},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "ReverseConnection",
			Handler:       _SystemAProxy_ReverseConnection_Handler,
			ServerStreams: true,
			ClientStreams: true,
		},
	},
	Metadata: "rpc/systema/manager2systama.proto",
}

// UserDaemonSystemAProxyClient is the client API for UserDaemonSystemAProxy service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type UserDaemonSystemAProxyClient interface {
	ReverseConnection(ctx context.Context, opts ...grpc.CallOption) (UserDaemonSystemAProxy_ReverseConnectionClient, error)
}

type userDaemonSystemAProxyClient struct {
	cc grpc.ClientConnInterface
}

func NewUserDaemonSystemAProxyClient(cc grpc.ClientConnInterface) UserDaemonSystemAProxyClient {
	return &userDaemonSystemAProxyClient{cc}
}

func (c *userDaemonSystemAProxyClient) ReverseConnection(ctx context.Context, opts ...grpc.CallOption) (UserDaemonSystemAProxy_ReverseConnectionClient, error) {
	stream, err := c.cc.NewStream(ctx, &UserDaemonSystemAProxy_ServiceDesc.Streams[0], "/telepresence.systema.UserDaemonSystemAProxy/ReverseConnection", opts...)
	if err != nil {
		return nil, err
	}
	x := &userDaemonSystemAProxyReverseConnectionClient{stream}
	return x, nil
}

type UserDaemonSystemAProxy_ReverseConnectionClient interface {
	Send(*Chunk) error
	Recv() (*Chunk, error)
	grpc.ClientStream
}

type userDaemonSystemAProxyReverseConnectionClient struct {
	grpc.ClientStream
}

func (x *userDaemonSystemAProxyReverseConnectionClient) Send(m *Chunk) error {
	return x.ClientStream.SendMsg(m)
}

func (x *userDaemonSystemAProxyReverseConnectionClient) Recv() (*Chunk, error) {
	m := new(Chunk)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

// UserDaemonSystemAProxyServer is the server API for UserDaemonSystemAProxy service.
// All implementations must embed UnimplementedUserDaemonSystemAProxyServer
// for forward compatibility
type UserDaemonSystemAProxyServer interface {
	ReverseConnection(UserDaemonSystemAProxy_ReverseConnectionServer) error
	mustEmbedUnimplementedUserDaemonSystemAProxyServer()
}

// UnimplementedUserDaemonSystemAProxyServer must be embedded to have forward compatible implementations.
type UnimplementedUserDaemonSystemAProxyServer struct {
}

func (UnimplementedUserDaemonSystemAProxyServer) ReverseConnection(UserDaemonSystemAProxy_ReverseConnectionServer) error {
	return status.Errorf(codes.Unimplemented, "method ReverseConnection not implemented")
}
func (UnimplementedUserDaemonSystemAProxyServer) mustEmbedUnimplementedUserDaemonSystemAProxyServer() {
}

// UnsafeUserDaemonSystemAProxyServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to UserDaemonSystemAProxyServer will
// result in compilation errors.
type UnsafeUserDaemonSystemAProxyServer interface {
	mustEmbedUnimplementedUserDaemonSystemAProxyServer()
}

func RegisterUserDaemonSystemAProxyServer(s grpc.ServiceRegistrar, srv UserDaemonSystemAProxyServer) {
	s.RegisterService(&UserDaemonSystemAProxy_ServiceDesc, srv)
}

func _UserDaemonSystemAProxy_ReverseConnection_Handler(srv interface{}, stream grpc.ServerStream) error {
	return srv.(UserDaemonSystemAProxyServer).ReverseConnection(&userDaemonSystemAProxyReverseConnectionServer{stream})
}

type UserDaemonSystemAProxy_ReverseConnectionServer interface {
	Send(*Chunk) error
	Recv() (*Chunk, error)
	grpc.ServerStream
}

type userDaemonSystemAProxyReverseConnectionServer struct {
	grpc.ServerStream
}

func (x *userDaemonSystemAProxyReverseConnectionServer) Send(m *Chunk) error {
	return x.ServerStream.SendMsg(m)
}

func (x *userDaemonSystemAProxyReverseConnectionServer) Recv() (*Chunk, error) {
	m := new(Chunk)
	if err := x.ServerStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

// UserDaemonSystemAProxy_ServiceDesc is the grpc.ServiceDesc for UserDaemonSystemAProxy service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var UserDaemonSystemAProxy_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "telepresence.systema.UserDaemonSystemAProxy",
	HandlerType: (*UserDaemonSystemAProxyServer)(nil),
	Methods:     []grpc.MethodDesc{},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "ReverseConnection",
			Handler:       _UserDaemonSystemAProxy_ReverseConnection_Handler,
			ServerStreams: true,
			ClientStreams: true,
		},
	},
	Metadata: "rpc/systema/manager2systama.proto",
}