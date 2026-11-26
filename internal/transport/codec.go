package transport

import (
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/mem"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// frame is one opaque message: a signed agentv1 envelope, or on the command
// stream an acknowledgement (the command ID).
type frame struct{ data []byte }

type codec struct{}

var _ encoding.CodecV2 = codec{}

func (codec) Name() string { return agentv1.GatewayCodec }

func (codec) Marshal(v any) (mem.BufferSlice, error) {
	f, ok := v.(*frame)
	if !ok {
		return nil, fmt.Errorf("agent codec cannot marshal %T", v)
	}
	return mem.BufferSlice{mem.SliceBuffer(f.data)}, nil
}

func (codec) Unmarshal(data mem.BufferSlice, v any) error {
	f, ok := v.(*frame)
	if !ok {
		return fmt.Errorf("agent codec cannot unmarshal into %T", v)
	}
	f.data = data.Materialize()
	return nil
}

func forceCodec() grpc.CallOption { return grpc.ForceCodecV2(codec{}) }
