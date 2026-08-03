package dispatch

import (
	"context"
	"log"
	"path/filepath"

	"github.com/diskwave/server/internal/auth"
	"github.com/diskwave/server/internal/blocks"
	"github.com/diskwave/server/internal/metadata"
	pb "github.com/diskwave/server/internal/proto"
	"google.golang.org/protobuf/proto"
)

// Broadcaster is implemented by each transport handler to push server-initiated
// INVALIDATE messages to all connected clients.
type Broadcaster interface {
	BroadcastInvalidate(path string)
}

// Dispatcher holds shared business logic used by both TCP and QUIC transports.
type Dispatcher struct {
	AuthMgr         *auth.Manager
	MetaSvc         *metadata.Service
	BlockSvc        *blocks.Service
	Broadcast       Broadcaster
	CertFingerprint string // SHA-256 hex of server TLS cert; included in PairResponse for pinning
}

// --- Auth ---

func (d *Dispatcher) HandlePair(env *pb.Envelope) *pb.Envelope {
	var req pb.PairRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_PAIR_RESPONSE, &pb.PairResponse{})
	}
	if !d.AuthMgr.ValidateCode(req.Code) {
		return MakeEnvelope(env.RequestId, pb.MessageType_PAIR_RESPONSE, &pb.PairResponse{})
	}
	clientID := auth.NewClientID()
	token, err := d.AuthMgr.IssueToken(clientID)
	if err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_PAIR_RESPONSE, &pb.PairResponse{})
	}
	return MakeEnvelope(env.RequestId, pb.MessageType_PAIR_RESPONSE, &pb.PairResponse{
		JwtToken:        token,
		ServerName:      "Diskwave",
		CertFingerprint: d.CertFingerprint,
	})
}

// HandleConnect validates the JWT and returns the clientID on success.
// The transport is responsible for storing the authenticated clientID.
func (d *Dispatcher) HandleConnect(env *pb.Envelope) (*pb.Envelope, string) {
	var req pb.ConnectRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_CONNECT_RESPONSE,
			&pb.ConnectResponse{Ok: false, Error: "invalid request"}), ""
	}
	claims, err := d.AuthMgr.ValidateToken(req.JwtToken)
	if err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_CONNECT_RESPONSE,
			&pb.ConnectResponse{Ok: false, Error: "invalid token"}), ""
	}
	log.Printf("[auth] client %s authenticated", claims.ClientID)
	return MakeEnvelope(env.RequestId, pb.MessageType_CONNECT_RESPONSE,
		&pb.ConnectResponse{Ok: true}), claims.ClientID
}

// --- Metadata ---

func (d *Dispatcher) HandleStat(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.StatRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_STAT_RESPONSE, &pb.StatResponse{Error: "invalid request"})
	}
	entry, err := d.MetaSvc.Stat(ctx, req.Path)
	if err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_STAT_RESPONSE, &pb.StatResponse{Error: err.Error()})
	}
	return MakeEnvelope(env.RequestId, pb.MessageType_STAT_RESPONSE, &pb.StatResponse{Entry: EntryToProto(entry)})
}

func (d *Dispatcher) HandleReadDir(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.ReadDirRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_READDIR_RESPONSE, &pb.ReadDirResponse{Error: "invalid request"})
	}
	entries, err := d.MetaSvc.ReadDir(ctx, req.Path)
	if err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_READDIR_RESPONSE, &pb.ReadDirResponse{Error: err.Error()})
	}
	pbEntries := make([]*pb.DirEntry, 0, len(entries))
	for _, e := range entries {
		pbEntries = append(pbEntries, EntryToProto(e))
	}
	return MakeEnvelope(env.RequestId, pb.MessageType_READDIR_RESPONSE, &pb.ReadDirResponse{Entries: pbEntries})
}

func (d *Dispatcher) HandleMkdir(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.MkdirRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_MKDIR_RESPONSE, &pb.MkdirResponse{Error: "invalid request"})
	}
	if err := d.MetaSvc.Mkdir(ctx, req.Path, req.Mode); err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_MKDIR_RESPONSE, &pb.MkdirResponse{Error: err.Error()})
	}
	d.broadcastMutation(req.Path)
	return MakeEnvelope(env.RequestId, pb.MessageType_MKDIR_RESPONSE, &pb.MkdirResponse{})
}

func (d *Dispatcher) HandleMknod(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.MknodRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_MKNOD_RESPONSE, &pb.MknodResponse{Error: "invalid request"})
	}
	if err := d.MetaSvc.Mknod(ctx, req.Path, req.Mode); err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_MKNOD_RESPONSE, &pb.MknodResponse{Error: err.Error()})
	}
	d.broadcastMutation(req.Path)
	return MakeEnvelope(env.RequestId, pb.MessageType_MKNOD_RESPONSE, &pb.MknodResponse{})
}

func (d *Dispatcher) HandleRename(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.RenameRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_RENAME_RESPONSE, &pb.RenameResponse{Error: "invalid request"})
	}

	if err := d.MetaSvc.Rename(ctx, req.OldPath, req.NewPath); err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_RENAME_RESPONSE, &pb.RenameResponse{Error: err.Error()})
	}

	if err := d.BlockSvc.Rename(ctx, req.OldPath, req.NewPath); err != nil {
		// Block rename failed → roll back metadata to keep consistency
		if rollbackErr := d.MetaSvc.Rename(ctx, req.NewPath, req.OldPath); rollbackErr != nil {
			log.Printf("[rename] metadata rollback failed %s→%s: %v", req.NewPath, req.OldPath, rollbackErr)
		}
		return MakeEnvelope(env.RequestId, pb.MessageType_RENAME_RESPONSE, &pb.RenameResponse{Error: err.Error()})
	}

	d.broadcastMutation(req.OldPath)
	d.broadcastMutation(req.NewPath)
	return MakeEnvelope(env.RequestId, pb.MessageType_RENAME_RESPONSE, &pb.RenameResponse{})
}

func (d *Dispatcher) HandleDelete(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.DeleteRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_DELETE_RESPONSE, &pb.DeleteResponse{Error: "invalid request"})
	}
	if err := d.BlockSvc.Delete(ctx, req.Path); err != nil {
		log.Printf("[delete] orphan block at %s: %v", req.Path, err)
	}
	if err := d.MetaSvc.Delete(ctx, req.Path); err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_DELETE_RESPONSE, &pb.DeleteResponse{Error: err.Error()})
	}
	d.broadcastMutation(req.Path)
	return MakeEnvelope(env.RequestId, pb.MessageType_DELETE_RESPONSE, &pb.DeleteResponse{})
}

func (d *Dispatcher) HandleSetSize(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.SetSizeRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_SET_SIZE_RESPONSE, &pb.SetSizeResponse{Error: "invalid request"})
	}
	if err := d.BlockSvc.Truncate(ctx, req.Path, req.Size); err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_SET_SIZE_RESPONSE, &pb.SetSizeResponse{Error: err.Error()})
	}
	if err := d.MetaSvc.SetSize(ctx, req.Path, req.Size); err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_SET_SIZE_RESPONSE, &pb.SetSizeResponse{Error: err.Error()})
	}
	return MakeEnvelope(env.RequestId, pb.MessageType_SET_SIZE_RESPONSE, &pb.SetSizeResponse{})
}

// --- Blocks ---

func (d *Dispatcher) HandleBlockRead(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.BlockReadRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_BLOCK_READ_RESPONSE, &pb.BlockReadResponse{Error: "invalid request"})
	}
	data, err := d.BlockSvc.Read(ctx, req.Path, req.Offset, req.Size)
	if err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_BLOCK_READ_RESPONSE, &pb.BlockReadResponse{Error: err.Error()})
	}
	return MakeEnvelope(env.RequestId, pb.MessageType_BLOCK_READ_RESPONSE, &pb.BlockReadResponse{Data: data})
}

func (d *Dispatcher) HandleBlockWrite(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.BlockWriteRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_BLOCK_WRITE_RESPONSE, &pb.BlockWriteResponse{Error: "invalid request"})
	}
	written, err := d.BlockSvc.Write(ctx, req.Path, req.Offset, req.Data)
	if err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_BLOCK_WRITE_RESPONSE, &pb.BlockWriteResponse{Error: err.Error()})
	}
	size, _ := d.BlockSvc.Size(ctx, req.Path)
	_ = d.MetaSvc.SetSize(ctx, req.Path, size)
	return MakeEnvelope(env.RequestId, pb.MessageType_BLOCK_WRITE_RESPONSE, &pb.BlockWriteResponse{Written: written})
}

func (d *Dispatcher) HandleBlockExists(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.BlockExistsRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return MakeEnvelope(env.RequestId, pb.MessageType_BLOCK_EXISTS_RESPONSE, &pb.BlockExistsResponse{})
	}
	exists, _ := d.BlockSvc.BlockExists(ctx, req.Hash)
	return MakeEnvelope(env.RequestId, pb.MessageType_BLOCK_EXISTS_RESPONSE, &pb.BlockExistsResponse{Exists: exists})
}

// --- Broadcast helpers ---

// broadcastMutation invalidates both the path itself and its parent directory
// so that readdir caches on all connected clients are flushed correctly.
func (d *Dispatcher) broadcastMutation(path string) {
	if d.Broadcast == nil {
		return
	}
	d.Broadcast.BroadcastInvalidate(path)
	if parent := filepath.Dir(path); parent != path {
		d.Broadcast.BroadcastInvalidate(parent)
	}
}