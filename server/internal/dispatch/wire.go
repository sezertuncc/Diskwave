package dispatch

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/diskwave/server/internal/metadata"
	pb "github.com/diskwave/server/internal/proto"
	"google.golang.org/protobuf/proto"
)

// ReadEnvelope reads a 4-byte big-endian length-prefixed protobuf Envelope.
func ReadEnvelope(r io.Reader) (*pb.Envelope, error) {
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return nil, err
	}
	if length > 64*1024*1024 {
		return nil, fmt.Errorf("message too large: %d", length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	var env pb.Envelope
	if err := proto.Unmarshal(buf, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

// WriteEnvelope writes a length-prefixed protobuf Envelope.
func WriteEnvelope(w io.Writer, env *pb.Envelope) error {
	data, err := proto.Marshal(env)
	if err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, uint32(len(data))); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// MakeEnvelope serializes msg and wraps it in an Envelope.
func MakeEnvelope(reqID uint32, msgType pb.MessageType, msg proto.Message) *pb.Envelope {
	payload, _ := proto.Marshal(msg)
	return &pb.Envelope{RequestId: reqID, Type: msgType, Payload: payload}
}

// EntryToProto converts a metadata.Entry to its protobuf representation.
func EntryToProto(e *metadata.Entry) *pb.DirEntry {
	return &pb.DirEntry{
		Name:  e.Name,
		Type:  pb.FileType(e.Type),
		Size:  e.Size,
		Mtime: e.Mtime,
		Mode:  e.Mode,
	}
}