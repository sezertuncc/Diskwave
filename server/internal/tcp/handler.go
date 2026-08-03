package tcp

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"

	"github.com/diskwave/server/internal/auth"
	"github.com/diskwave/server/internal/blocks"
	"github.com/diskwave/server/internal/metadata"
	pb "github.com/diskwave/server/internal/proto"
	"google.golang.org/protobuf/proto"
)

type Handler struct {
	authMgr  *auth.Manager
	metaSvc  *metadata.Service
	blockSvc *blocks.Service
}

func NewHandler(a *auth.Manager, m *metadata.Service, b *blocks.Service) *Handler {
	return &Handler{authMgr: a, metaSvc: m, blockSvc: b}
}

func (h *Handler) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("tcp listen: %w", err)
	}
	defer ln.Close()

	log.Printf("[tcp] Listening on %s", addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[tcp] Accept error: %v", err)
			continue
		}
		go h.handleConn(conn)
	}
}

func (h *Handler) handleConn(conn net.Conn) {
	remote := conn.RemoteAddr().String()
	log.Printf("[tcp] New connection from %s", remote)
	defer conn.Close()

	var clientID string // set after successful CONNECT

	for {
		env, err := readEnvelope(conn)
		if err != nil {
			if err != io.EOF {
				log.Printf("[tcp] Read error from %s: %v", remote, err)
			}
			return
		}

		ctx := context.Background()
		var resp *pb.Envelope

		switch env.Type {
		case pb.MessageType_PAIR_REQUEST:
			resp = h.handlePair(ctx, env)
			// Extract clientID from issued token for subsequent auth
		case pb.MessageType_CONNECT_REQUEST:
			var id string
			resp, id = h.handleConnect(ctx, env)
			if id != "" {
				clientID = id
			}
		case pb.MessageType_STAT_REQUEST:
			resp = h.authorized(clientID, env, h.handleStat(ctx, env))
		case pb.MessageType_READDIR_REQUEST:
			resp = h.authorized(clientID, env, h.handleReadDir(ctx, env))
		case pb.MessageType_MKDIR_REQUEST:
			resp = h.authorized(clientID, env, h.handleMkdir(ctx, env))
		case pb.MessageType_MKNOD_REQUEST:
			resp = h.authorized(clientID, env, h.handleMknod(ctx, env))
		case pb.MessageType_RENAME_REQUEST:
			resp = h.authorized(clientID, env, h.handleRename(ctx, env))
		case pb.MessageType_DELETE_REQUEST:
			resp = h.authorized(clientID, env, h.handleDelete(ctx, env))
		case pb.MessageType_BLOCK_READ_REQUEST:
			resp = h.authorized(clientID, env, h.handleBlockRead(ctx, env))
		case pb.MessageType_BLOCK_WRITE_REQUEST:
			resp = h.authorized(clientID, env, h.handleBlockWrite(ctx, env))
		case pb.MessageType_SET_SIZE_REQUEST:
			resp = h.authorized(clientID, env, h.handleSetSize(ctx, env))
		case pb.MessageType_BLOCK_EXISTS_REQUEST:
			resp = h.authorized(clientID, env, h.handleBlockExists(ctx, env))
		default:
			log.Printf("[tcp] Unknown type: %v", env.Type)
			continue
		}

		if resp != nil {
			if err := writeEnvelope(conn, resp); err != nil {
				log.Printf("[tcp] Write error to %s: %v", remote, err)
				return
			}
		}
	}
}

// authorized returns errResp if not authenticated, otherwise returns given resp.
func (h *Handler) authorized(clientID string, env *pb.Envelope, resp *pb.Envelope) *pb.Envelope {
	if clientID == "" {
		return makeError(env, "unauthorized")
	}
	return resp
}

// makeError builds a generic error envelope matching the request type pattern.
func makeError(env *pb.Envelope, msg string) *pb.Envelope {
	// Map request type → response type
	respType := env.Type + 1
	return makeEnvelope(env.RequestId, respType, &pb.StatResponse{Error: msg})
}

func (h *Handler) handlePair(_ context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.PairRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_PAIR_RESPONSE, &pb.PairResponse{})
	}
	if !h.authMgr.ValidateCode(req.Code) {
		return makeEnvelope(env.RequestId, pb.MessageType_PAIR_RESPONSE, &pb.PairResponse{})
	}
	clientID := auth.NewClientID()
	token, err := h.authMgr.IssueToken(clientID)
	if err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_PAIR_RESPONSE, &pb.PairResponse{})
	}
	return makeEnvelope(env.RequestId, pb.MessageType_PAIR_RESPONSE, &pb.PairResponse{
		JwtToken:   token,
		ServerName: "Diskwave",
	})
}

func (h *Handler) handleConnect(_ context.Context, env *pb.Envelope) (*pb.Envelope, string) {
	var req pb.ConnectRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_CONNECT_RESPONSE,
			&pb.ConnectResponse{Ok: false, Error: "invalid request"}), ""
	}
	claims, err := h.authMgr.ValidateToken(req.JwtToken)
	if err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_CONNECT_RESPONSE,
			&pb.ConnectResponse{Ok: false, Error: "invalid token"}), ""
	}
	log.Printf("[tcp/auth] Client %s connected", claims.ClientID)
	return makeEnvelope(env.RequestId, pb.MessageType_CONNECT_RESPONSE,
		&pb.ConnectResponse{Ok: true}), claims.ClientID
}

func (h *Handler) handleStat(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.StatRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_STAT_RESPONSE, &pb.StatResponse{Error: "invalid"})
	}
	entry, err := h.metaSvc.Stat(ctx, req.Path)
	if err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_STAT_RESPONSE, &pb.StatResponse{Error: err.Error()})
	}
	return makeEnvelope(env.RequestId, pb.MessageType_STAT_RESPONSE, &pb.StatResponse{Entry: entryToProto(entry)})
}

func (h *Handler) handleReadDir(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.ReadDirRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_READDIR_RESPONSE, &pb.ReadDirResponse{Error: "invalid"})
	}
	entries, err := h.metaSvc.ReadDir(ctx, req.Path)
	if err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_READDIR_RESPONSE, &pb.ReadDirResponse{Error: err.Error()})
	}
	var pbEntries []*pb.DirEntry
	for _, e := range entries {
		pbEntries = append(pbEntries, entryToProto(e))
	}
	return makeEnvelope(env.RequestId, pb.MessageType_READDIR_RESPONSE, &pb.ReadDirResponse{Entries: pbEntries})
}

func (h *Handler) handleMkdir(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.MkdirRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_MKDIR_RESPONSE, &pb.MkdirResponse{Error: "invalid"})
	}
	if err := h.metaSvc.Mkdir(ctx, req.Path, req.Mode); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_MKDIR_RESPONSE, &pb.MkdirResponse{Error: err.Error()})
	}
	return makeEnvelope(env.RequestId, pb.MessageType_MKDIR_RESPONSE, &pb.MkdirResponse{})
}

func (h *Handler) handleMknod(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.MknodRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_MKNOD_RESPONSE, &pb.MknodResponse{Error: "invalid"})
	}
	if err := h.metaSvc.Mknod(ctx, req.Path, req.Mode); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_MKNOD_RESPONSE, &pb.MknodResponse{Error: err.Error()})
	}
	return makeEnvelope(env.RequestId, pb.MessageType_MKNOD_RESPONSE, &pb.MknodResponse{})
}

func (h *Handler) handleRename(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.RenameRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_RENAME_RESPONSE, &pb.RenameResponse{Error: "invalid"})
	}
	if err := h.metaSvc.Rename(ctx, req.OldPath, req.NewPath); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_RENAME_RESPONSE, &pb.RenameResponse{Error: err.Error()})
	}
	if err := h.blockSvc.Rename(ctx, req.OldPath, req.NewPath); err != nil {
		log.Printf("[tcp] block rename %s→%s: %v", req.OldPath, req.NewPath, err)
	}
	return makeEnvelope(env.RequestId, pb.MessageType_RENAME_RESPONSE, &pb.RenameResponse{})
}

func (h *Handler) handleDelete(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.DeleteRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_DELETE_RESPONSE, &pb.DeleteResponse{Error: "invalid"})
	}
	_ = h.blockSvc.Delete(ctx, req.Path)
	if err := h.metaSvc.Delete(ctx, req.Path); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_DELETE_RESPONSE, &pb.DeleteResponse{Error: err.Error()})
	}
	return makeEnvelope(env.RequestId, pb.MessageType_DELETE_RESPONSE, &pb.DeleteResponse{})
}

func (h *Handler) handleBlockRead(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.BlockReadRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_BLOCK_READ_RESPONSE, &pb.BlockReadResponse{Error: "invalid"})
	}
	data, err := h.blockSvc.Read(ctx, req.Path, req.Offset, req.Size)
	if err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_BLOCK_READ_RESPONSE, &pb.BlockReadResponse{Error: err.Error()})
	}
	return makeEnvelope(env.RequestId, pb.MessageType_BLOCK_READ_RESPONSE, &pb.BlockReadResponse{Data: data})
}

func (h *Handler) handleBlockWrite(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.BlockWriteRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_BLOCK_WRITE_RESPONSE, &pb.BlockWriteResponse{Error: "invalid"})
	}
	written, err := h.blockSvc.Write(ctx, req.Path, req.Offset, req.Data)
	if err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_BLOCK_WRITE_RESPONSE, &pb.BlockWriteResponse{Error: err.Error()})
	}
	size, _ := h.blockSvc.Size(ctx, req.Path)
	_ = h.metaSvc.SetSize(ctx, req.Path, size)
	return makeEnvelope(env.RequestId, pb.MessageType_BLOCK_WRITE_RESPONSE, &pb.BlockWriteResponse{Written: written})
}

func (h *Handler) handleSetSize(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.SetSizeRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_SET_SIZE_RESPONSE, &pb.SetSizeResponse{Error: "invalid"})
	}
	if err := h.blockSvc.Truncate(ctx, req.Path, req.Size); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_SET_SIZE_RESPONSE, &pb.SetSizeResponse{Error: err.Error()})
	}
	if err := h.metaSvc.SetSize(ctx, req.Path, req.Size); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_SET_SIZE_RESPONSE, &pb.SetSizeResponse{Error: err.Error()})
	}
	return makeEnvelope(env.RequestId, pb.MessageType_SET_SIZE_RESPONSE, &pb.SetSizeResponse{})
}

func (h *Handler) handleBlockExists(ctx context.Context, env *pb.Envelope) *pb.Envelope {
	var req pb.BlockExistsRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_BLOCK_EXISTS_RESPONSE, &pb.BlockExistsResponse{})
	}
	exists, _ := h.blockSvc.BlockExists(ctx, req.Hash)
	return makeEnvelope(env.RequestId, pb.MessageType_BLOCK_EXISTS_RESPONSE, &pb.BlockExistsResponse{Exists: exists})
}

// --- Wire helpers ---

func readEnvelope(r io.Reader) (*pb.Envelope, error) {
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

func writeEnvelope(w io.Writer, env *pb.Envelope) error {
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

func makeEnvelope(reqID uint32, msgType pb.MessageType, msg proto.Message) *pb.Envelope {
	payload, _ := proto.Marshal(msg)
	return &pb.Envelope{RequestId: reqID, Type: msgType, Payload: payload}
}

func entryToProto(e *metadata.Entry) *pb.DirEntry {
	return &pb.DirEntry{
		Name:  e.Name,
		Type:  pb.FileType(e.Type),
		Size:  e.Size,
		Mtime: e.Mtime,
		Mode:  e.Mode,
	}
}