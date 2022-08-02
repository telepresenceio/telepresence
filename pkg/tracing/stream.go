package tracing

import (
	"context"
	"io"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

type ProtoReader[T proto.Message] struct {
	in        io.Reader
	allocator func() T
}

func NewProtoReader[T proto.Message](in io.Reader, allocator func() T) *ProtoReader[T] {
	return &ProtoReader[T]{
		in:        in,
		allocator: allocator,
	}
}

func (p *ProtoReader[T]) ReadAll(ctx context.Context) ([]T, error) {
	var err error
	result := []T{}
	for err == nil {
		var resource T
		resource, err = p.ReadNext(ctx)
		if err == nil {
			result = append(result, resource)
		}
	}
	if err == io.EOF {
		return result, nil
	}
	return nil, err
}

func (p *ProtoReader[T]) ReadNext(ctx context.Context) (T, error) {
	szBuf := make([]byte, protowire.SizeFixed32())
	// ReadFull will return UnexpectedEOF if it fails to read the entire buffer,
	// unless it read nothing at all, in which case it returns plain EOF
	_, err := io.ReadFull(p.in, szBuf)
	if err != nil {
		return *new(T), err //nolint:gocritic // golint wants T(nil) but go won't allow it
	}
	size, n := protowire.ConsumeFixed32(szBuf)
	if n < 0 {
		return *new(T), protowire.ParseError(n) //nolint:gocritic // golint wants T(nil) but go won't allow it
	}

	buf := make([]byte, size)
	n, err = io.ReadFull(p.in, buf)
	// If we read a size but no message, that's an error.
	if n == 0 {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		return *new(T), err //nolint:gocritic // golint wants T(nil) but go won't allow it
	}
	result := p.allocator()
	err = proto.Unmarshal(buf, result)
	if err != nil {
		return *new(T), err //nolint:gocritic // golint wants T(nil) but go won't allow it
	}

	return result, nil
}

type ProtoWriter struct {
	out io.Writer
}

func NewProtoWriter(out io.Writer) *ProtoWriter {
	return &ProtoWriter{
		out: out,
	}
}

func (p *ProtoWriter) SetWriter(out io.Writer) {
	p.out = out
}

func (p *ProtoWriter) Encode(m proto.Message) error {
	buf, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	szBuf := protowire.AppendFixed32([]byte{}, uint32(len(buf)))
	_, err = p.out.Write(szBuf)
	if err != nil {
		return err
	}
	_, err = p.out.Write(buf)
	if err != nil {
		return err
	}
	return nil
}
