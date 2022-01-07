// Code generated by protoc-gen-go-grpc. DO NOT EDIT.

package daemon

import (
	context "context"
	common "github.com/telepresenceio/telepresence/rpc/v2/common"
	manager "github.com/telepresenceio/telepresence/rpc/v2/manager"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.32.0 or later.
const _ = grpc.SupportPackageIsVersion7

// DaemonClient is the client API for Daemon service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type DaemonClient interface {
	// Version returns version information from the Daemon
	Version(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*common.VersionInfo, error)
	// Status returns the current connectivity status
	Status(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*DaemonStatus, error)
	// Quit quits (terminates) the service.
	Quit(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*emptypb.Empty, error)
	// Connect creates a new session that provides outbound connectivity to the cluster
	Connect(ctx context.Context, in *OutboundInfo, opts ...grpc.CallOption) (*emptypb.Empty, error)
	// Disconnect disconnects the current session.
	Disconnect(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*emptypb.Empty, error)
	// GetClusterSubnets gets the outbound info that has been set on daemon
	GetClusterSubnets(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*ClusterSubnets, error)
	// SetDnsSearchPath sets a new search path.
	SetDnsSearchPath(ctx context.Context, in *Paths, opts ...grpc.CallOption) (*emptypb.Empty, error)
	// SetLogLevel will temporarily set the log-level for the daemon for a duration that is determined b the request.
	SetLogLevel(ctx context.Context, in *manager.LogLevelRequest, opts ...grpc.CallOption) (*emptypb.Empty, error)
}

type daemonClient struct {
	cc grpc.ClientConnInterface
}

func NewDaemonClient(cc grpc.ClientConnInterface) DaemonClient {
	return &daemonClient{cc}
}

func (c *daemonClient) Version(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*common.VersionInfo, error) {
	out := new(common.VersionInfo)
	err := c.cc.Invoke(ctx, "/telepresence.daemon.Daemon/Version", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *daemonClient) Status(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*DaemonStatus, error) {
	out := new(DaemonStatus)
	err := c.cc.Invoke(ctx, "/telepresence.daemon.Daemon/Status", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *daemonClient) Quit(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	out := new(emptypb.Empty)
	err := c.cc.Invoke(ctx, "/telepresence.daemon.Daemon/Quit", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *daemonClient) Connect(ctx context.Context, in *OutboundInfo, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	out := new(emptypb.Empty)
	err := c.cc.Invoke(ctx, "/telepresence.daemon.Daemon/Connect", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *daemonClient) Disconnect(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	out := new(emptypb.Empty)
	err := c.cc.Invoke(ctx, "/telepresence.daemon.Daemon/Disconnect", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *daemonClient) GetClusterSubnets(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*ClusterSubnets, error) {
	out := new(ClusterSubnets)
	err := c.cc.Invoke(ctx, "/telepresence.daemon.Daemon/GetClusterSubnets", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *daemonClient) SetDnsSearchPath(ctx context.Context, in *Paths, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	out := new(emptypb.Empty)
	err := c.cc.Invoke(ctx, "/telepresence.daemon.Daemon/SetDnsSearchPath", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *daemonClient) SetLogLevel(ctx context.Context, in *manager.LogLevelRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	out := new(emptypb.Empty)
	err := c.cc.Invoke(ctx, "/telepresence.daemon.Daemon/SetLogLevel", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DaemonServer is the server API for Daemon service.
// All implementations must embed UnimplementedDaemonServer
// for forward compatibility
type DaemonServer interface {
	// Version returns version information from the Daemon
	Version(context.Context, *emptypb.Empty) (*common.VersionInfo, error)
	// Status returns the current connectivity status
	Status(context.Context, *emptypb.Empty) (*DaemonStatus, error)
	// Quit quits (terminates) the service.
	Quit(context.Context, *emptypb.Empty) (*emptypb.Empty, error)
	// Connect creates a new session that provides outbound connectivity to the cluster
	Connect(context.Context, *OutboundInfo) (*emptypb.Empty, error)
	// Disconnect disconnects the current session.
	Disconnect(context.Context, *emptypb.Empty) (*emptypb.Empty, error)
	// GetClusterSubnets gets the outbound info that has been set on daemon
	GetClusterSubnets(context.Context, *emptypb.Empty) (*ClusterSubnets, error)
	// SetDnsSearchPath sets a new search path.
	SetDnsSearchPath(context.Context, *Paths) (*emptypb.Empty, error)
	// SetLogLevel will temporarily set the log-level for the daemon for a duration that is determined b the request.
	SetLogLevel(context.Context, *manager.LogLevelRequest) (*emptypb.Empty, error)
	mustEmbedUnimplementedDaemonServer()
}

// UnimplementedDaemonServer must be embedded to have forward compatible implementations.
type UnimplementedDaemonServer struct {
}

func (UnimplementedDaemonServer) Version(context.Context, *emptypb.Empty) (*common.VersionInfo, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Version not implemented")
}
func (UnimplementedDaemonServer) Status(context.Context, *emptypb.Empty) (*DaemonStatus, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Status not implemented")
}
func (UnimplementedDaemonServer) Quit(context.Context, *emptypb.Empty) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Quit not implemented")
}
func (UnimplementedDaemonServer) Connect(context.Context, *OutboundInfo) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Connect not implemented")
}
func (UnimplementedDaemonServer) Disconnect(context.Context, *emptypb.Empty) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Disconnect not implemented")
}
func (UnimplementedDaemonServer) GetClusterSubnets(context.Context, *emptypb.Empty) (*ClusterSubnets, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetClusterSubnets not implemented")
}
func (UnimplementedDaemonServer) SetDnsSearchPath(context.Context, *Paths) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method SetDnsSearchPath not implemented")
}
func (UnimplementedDaemonServer) SetLogLevel(context.Context, *manager.LogLevelRequest) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method SetLogLevel not implemented")
}
func (UnimplementedDaemonServer) mustEmbedUnimplementedDaemonServer() {}

// UnsafeDaemonServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to DaemonServer will
// result in compilation errors.
type UnsafeDaemonServer interface {
	mustEmbedUnimplementedDaemonServer()
}

func RegisterDaemonServer(s grpc.ServiceRegistrar, srv DaemonServer) {
	s.RegisterService(&Daemon_ServiceDesc, srv)
}

func _Daemon_Version_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(emptypb.Empty)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(DaemonServer).Version(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/telepresence.daemon.Daemon/Version",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(DaemonServer).Version(ctx, req.(*emptypb.Empty))
	}
	return interceptor(ctx, in, info, handler)
}

func _Daemon_Status_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(emptypb.Empty)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(DaemonServer).Status(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/telepresence.daemon.Daemon/Status",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(DaemonServer).Status(ctx, req.(*emptypb.Empty))
	}
	return interceptor(ctx, in, info, handler)
}

func _Daemon_Quit_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(emptypb.Empty)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(DaemonServer).Quit(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/telepresence.daemon.Daemon/Quit",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(DaemonServer).Quit(ctx, req.(*emptypb.Empty))
	}
	return interceptor(ctx, in, info, handler)
}

func _Daemon_Connect_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(OutboundInfo)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(DaemonServer).Connect(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/telepresence.daemon.Daemon/Connect",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(DaemonServer).Connect(ctx, req.(*OutboundInfo))
	}
	return interceptor(ctx, in, info, handler)
}

func _Daemon_Disconnect_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(emptypb.Empty)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(DaemonServer).Disconnect(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/telepresence.daemon.Daemon/Disconnect",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(DaemonServer).Disconnect(ctx, req.(*emptypb.Empty))
	}
	return interceptor(ctx, in, info, handler)
}

func _Daemon_GetClusterSubnets_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(emptypb.Empty)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(DaemonServer).GetClusterSubnets(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/telepresence.daemon.Daemon/GetClusterSubnets",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(DaemonServer).GetClusterSubnets(ctx, req.(*emptypb.Empty))
	}
	return interceptor(ctx, in, info, handler)
}

func _Daemon_SetDnsSearchPath_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(Paths)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(DaemonServer).SetDnsSearchPath(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/telepresence.daemon.Daemon/SetDnsSearchPath",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(DaemonServer).SetDnsSearchPath(ctx, req.(*Paths))
	}
	return interceptor(ctx, in, info, handler)
}

func _Daemon_SetLogLevel_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(manager.LogLevelRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(DaemonServer).SetLogLevel(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/telepresence.daemon.Daemon/SetLogLevel",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(DaemonServer).SetLogLevel(ctx, req.(*manager.LogLevelRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// Daemon_ServiceDesc is the grpc.ServiceDesc for Daemon service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var Daemon_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "telepresence.daemon.Daemon",
	HandlerType: (*DaemonServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Version",
			Handler:    _Daemon_Version_Handler,
		},
		{
			MethodName: "Status",
			Handler:    _Daemon_Status_Handler,
		},
		{
			MethodName: "Quit",
			Handler:    _Daemon_Quit_Handler,
		},
		{
			MethodName: "Connect",
			Handler:    _Daemon_Connect_Handler,
		},
		{
			MethodName: "Disconnect",
			Handler:    _Daemon_Disconnect_Handler,
		},
		{
			MethodName: "GetClusterSubnets",
			Handler:    _Daemon_GetClusterSubnets_Handler,
		},
		{
			MethodName: "SetDnsSearchPath",
			Handler:    _Daemon_SetDnsSearchPath_Handler,
		},
		{
			MethodName: "SetLogLevel",
			Handler:    _Daemon_SetLogLevel_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "rpc/daemon/daemon.proto",
}
