package fakecontroller

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/mem"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// frame is one opaque message (see agentv1.GatewayService).
type frame struct{ data []byte }

type codec struct{}

var _ encoding.CodecV2 = codec{}

func (codec) Name() string { return agentv1.GatewayCodec }

func (codec) Marshal(v any) (mem.BufferSlice, error) {
	f, ok := v.(*frame)
	if !ok {
		return nil, fmt.Errorf("cannot marshal %T", v)
	}
	return mem.BufferSlice{mem.SliceBuffer(f.data)}, nil
}

func (codec) Unmarshal(data mem.BufferSlice, v any) error {
	f, ok := v.(*frame)
	if !ok {
		return fmt.Errorf("cannot unmarshal into %T", v)
	}
	f.data = data.Materialize()
	return nil
}

type service interface {
	register(context.Context, *frame) (*frame, error)
	renew(context.Context, *frame) (*frame, error)
	sendResult(context.Context, *frame) (*frame, error)
	commands(grpc.ServerStream) error
}

func unary(name string, call func(service, context.Context, *frame) (*frame, error)) grpc.MethodDesc {
	return grpc.MethodDesc{MethodName: name, Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		var in frame
		if err := dec(&in); err != nil {
			return nil, err
		}
		handler := func(ctx context.Context, req any) (any, error) { return call(srv.(service), ctx, req.(*frame)) }
		if interceptor == nil {
			return handler(ctx, &in)
		}
		return interceptor(ctx, &in, &grpc.UnaryServerInfo{Server: srv, FullMethod: "/" + agentv1.GatewayService + "/" + name}, handler)
	}}
}

var serviceDesc = grpc.ServiceDesc{
	ServiceName: agentv1.GatewayService,
	HandlerType: (*service)(nil),
	Methods: []grpc.MethodDesc{
		unary(agentv1.MethodRegister, service.register),
		unary(agentv1.MethodRenew, service.renew),
		unary(agentv1.MethodSendResult, service.sendResult),
	},
	Streams: []grpc.StreamDesc{{
		StreamName: agentv1.MethodCommands, ServerStreams: true, ClientStreams: true,
		Handler: func(srv any, stream grpc.ServerStream) error { return srv.(service).commands(stream) },
	}},
}
