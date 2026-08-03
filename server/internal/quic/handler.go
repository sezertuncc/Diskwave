package quic

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"sync"
	"time"

	"github.com/diskwave/server/internal/auth"
	"github.com/diskwave/server/internal/blocks"
	"github.com/diskwave/server/internal/metadata"
	pb "github.com/diskwave/server/internal/proto"
	quiclib "github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"
)

type Handler struct {
	authMgr  *auth.Manager
	metaSvc  *metadata.Service
	blockSvc *blocks.Service

	mu      sync.RWMutex
	clients map[string]*quiclib.Conn
}

func NewHandler(a *auth.Manager, m *metadata.Service, b *blocks.Service) *Handler {
	return &Handler{
		authMgr:  a,
		metaSvc:  m,
		blockSvc: b,
		clients:  make(map[string]*quiclib.Conn),
	}
}

func (h *Handler) ListenAndServe(addr string) error {
	tlsConf, err := generateTLSConfig()
	if err != nil {
		return fmt.Errorf("tls config: %w", err)
	}

	listener, err := quiclib.ListenAddr(addr, tlsConf, &quiclib.Config{
		MaxIdleTimeout:  30 * time.Second,
		KeepAlivePeriod: 10 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("quic listen: %w", err)
	}
	defer listener.Close()

	log.Printf("[quic] Listening on %s", addr)

	for {
		conn, err := listener.Accept(context.Background())
		if err != nil {
			log.Printf("[quic] Accept error: %v", err)
			continue
		}
		go h.handleConnection(conn)
	}
}

func (h *Handler) handleConnection(conn *quiclib.Conn) {
	remote := conn.RemoteAddr().String()
	log.Printf("[quic] New connection from %s", remote)

	for {
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			log.Printf("[quic] Stream accept error from %s: %v", remote, err)
			return
		}
		go h.handleStream(conn, stream)
	}
}

func (h *Handler) handleStream(conn *quiclib.Conn, stream *quiclib.Stream) {
	defer stream.Close()

	env, err := readEnvelope(stream)
	if err != nil {
		if err != io.EOF {
			log.Printf("[quic] Read envelope error: %v", err)
		}
		return
	}

	ctx := context.Background()

	var respEnv *pb.Envelope

	switch env.Type {
	case pb.MessageType_PAIR_REQUEST:
		respEnv = h.handlePair(ctx, env)
	case pb.MessageType_CONNECT_REQUEST:
		respEnv = h.handleConnect(ctx, conn, env)
	case pb.MessageType_STAT_REQUEST:
		respEnv = h.handleStat(ctx, conn, env)
	case pb.MessageType_READDIR_REQUEST:
		respEnv = h.handleReadDir(ctx, conn, env)
	case pb.MessageType_MKDIR_REQUEST:
		respEnv = h.handleMkdir(ctx, conn, env)
	case pb.MessageType_MKNOD_REQUEST:
		respEnv = h.handleMknod(ctx, conn, env)
	case pb.MessageType_RENAME_REQUEST:
		respEnv = h.handleRename(ctx, conn, env)
	case pb.MessageType_DELETE_REQUEST:
		respEnv = h.handleDelete(ctx, conn, env)
	case pb.MessageType_BLOCK_READ_REQUEST:
		respEnv = h.handleBlockRead(ctx, conn, env)
	case pb.MessageType_BLOCK_WRITE_REQUEST:
		respEnv = h.handleBlockWrite(ctx, conn, env)
	case pb.MessageType_SET_SIZE_REQUEST:
		respEnv = h.handleSetSize(ctx, conn, env)
	default:
		log.Printf("[quic] Unknown message type: %v", env.Type)
		return
	}

	if respEnv != nil {
		if err := writeEnvelope(stream, respEnv); err != nil {
			log.Printf("[quic] Write response error: %v", err)
		}
	}
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

func (h *Handler) handleConnect(_ context.Context, conn *quiclib.Conn, env *pb.Envelope) *pb.Envelope {
	var req pb.ConnectRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_CONNECT_RESPONSE, &pb.ConnectResponse{Ok: false, Error: "invalid request"})
	}

	claims, err := h.authMgr.ValidateToken(req.JwtToken)
	if err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_CONNECT_RESPONSE, &pb.ConnectResponse{Ok: false, Error: "invalid token"})
	}

	h.mu.Lock()
	h.clients[claims.ClientID] = conn
	h.mu.Unlock()

	log.Printf("[auth] Client %s connected", claims.ClientID)
	return makeEnvelope(env.RequestId, pb.MessageType_CONNECT_RESPONSE, &pb.ConnectResponse{Ok: true})
}

func (h *Handler) handleStat(ctx context.Context, conn *quiclib.Conn, env *pb.Envelope) *pb.Envelope {
	if !h.isAuthorized(conn) {
		return makeEnvelope(env.RequestId, pb.MessageType_STAT_RESPONSE, &pb.StatResponse{Error: "unauthorized"})
	}
	var req pb.StatRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_STAT_RESPONSE, &pb.StatResponse{Error: "invalid request"})
	}
	entry, err := h.metaSvc.Stat(ctx, req.Path)
	if err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_STAT_RESPONSE, &pb.StatResponse{Error: err.Error()})
	}
	return makeEnvelope(env.RequestId, pb.MessageType_STAT_RESPONSE, &pb.StatResponse{
		Entry: entryToProto(entry),
	})
}

func (h *Handler) handleReadDir(ctx context.Context, conn *quiclib.Conn, env *pb.Envelope) *pb.Envelope {
	if !h.isAuthorized(conn) {
		return makeEnvelope(env.RequestId, pb.MessageType_READDIR_RESPONSE, &pb.ReadDirResponse{Error: "unauthorized"})
	}
	var req pb.ReadDirRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_READDIR_RESPONSE, &pb.ReadDirResponse{Error: "invalid request"})
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

func (h *Handler) handleMkdir(ctx context.Context, conn *quiclib.Conn, env *pb.Envelope) *pb.Envelope {
	if !h.isAuthorized(conn) {
		return makeEnvelope(env.RequestId, pb.MessageType_MKDIR_RESPONSE, &pb.MkdirResponse{Error: "unauthorized"})
	}
	var req pb.MkdirRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_MKDIR_RESPONSE, &pb.MkdirResponse{Error: "invalid request"})
	}
	if err := h.metaSvc.Mkdir(ctx, req.Path, req.Mode); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_MKDIR_RESPONSE, &pb.MkdirResponse{Error: err.Error()})
	}
	h.broadcastInvalidate(req.Path)
	return makeEnvelope(env.RequestId, pb.MessageType_MKDIR_RESPONSE, &pb.MkdirResponse{})
}

func (h *Handler) handleMknod(ctx context.Context, conn *quiclib.Conn, env *pb.Envelope) *pb.Envelope {
	if !h.isAuthorized(conn) {
		return makeEnvelope(env.RequestId, pb.MessageType_MKNOD_RESPONSE, &pb.MknodResponse{Error: "unauthorized"})
	}
	var req pb.MknodRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_MKNOD_RESPONSE, &pb.MknodResponse{Error: "invalid request"})
	}
	if err := h.metaSvc.Mknod(ctx, req.Path, req.Mode); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_MKNOD_RESPONSE, &pb.MknodResponse{Error: err.Error()})
	}
	h.broadcastInvalidate(req.Path)
	return makeEnvelope(env.RequestId, pb.MessageType_MKNOD_RESPONSE, &pb.MknodResponse{})
}

func (h *Handler) handleRename(ctx context.Context, conn *quiclib.Conn, env *pb.Envelope) *pb.Envelope {
	if !h.isAuthorized(conn) {
		return makeEnvelope(env.RequestId, pb.MessageType_RENAME_RESPONSE, &pb.RenameResponse{Error: "unauthorized"})
	}
	var req pb.RenameRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_RENAME_RESPONSE, &pb.RenameResponse{Error: "invalid request"})
	}
	if err := h.metaSvc.Rename(ctx, req.OldPath, req.NewPath); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_RENAME_RESPONSE, &pb.RenameResponse{Error: err.Error()})
	}
	if err := h.blockSvc.Rename(ctx, req.OldPath, req.NewPath); err != nil {
		// Block rename hatası metadata'yı geri almaz ama log'a düşer
		log.Printf("[quic] block rename %s→%s: %v", req.OldPath, req.NewPath, err)
	}
	h.broadcastInvalidate(req.OldPath)
	h.broadcastInvalidate(req.NewPath)
	return makeEnvelope(env.RequestId, pb.MessageType_RENAME_RESPONSE, &pb.RenameResponse{})
}

func (h *Handler) handleDelete(ctx context.Context, conn *quiclib.Conn, env *pb.Envelope) *pb.Envelope {
	if !h.isAuthorized(conn) {
		return makeEnvelope(env.RequestId, pb.MessageType_DELETE_RESPONSE, &pb.DeleteResponse{Error: "unauthorized"})
	}
	var req pb.DeleteRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_DELETE_RESPONSE, &pb.DeleteResponse{Error: "invalid request"})
	}
	_ = h.blockSvc.Delete(ctx, req.Path)
	if err := h.metaSvc.Delete(ctx, req.Path); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_DELETE_RESPONSE, &pb.DeleteResponse{Error: err.Error()})
	}
	h.broadcastInvalidate(req.Path)
	return makeEnvelope(env.RequestId, pb.MessageType_DELETE_RESPONSE, &pb.DeleteResponse{})
}

func (h *Handler) handleBlockRead(ctx context.Context, conn *quiclib.Conn, env *pb.Envelope) *pb.Envelope {
	if !h.isAuthorized(conn) {
		return makeEnvelope(env.RequestId, pb.MessageType_BLOCK_READ_RESPONSE, &pb.BlockReadResponse{Error: "unauthorized"})
	}
	var req pb.BlockReadRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_BLOCK_READ_RESPONSE, &pb.BlockReadResponse{Error: "invalid request"})
	}
	data, err := h.blockSvc.Read(ctx, req.Path, req.Offset, req.Size)
	if err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_BLOCK_READ_RESPONSE, &pb.BlockReadResponse{Error: err.Error()})
	}
	return makeEnvelope(env.RequestId, pb.MessageType_BLOCK_READ_RESPONSE, &pb.BlockReadResponse{Data: data})
}

func (h *Handler) handleBlockWrite(ctx context.Context, conn *quiclib.Conn, env *pb.Envelope) *pb.Envelope {
	if !h.isAuthorized(conn) {
		return makeEnvelope(env.RequestId, pb.MessageType_BLOCK_WRITE_RESPONSE, &pb.BlockWriteResponse{Error: "unauthorized"})
	}
	var req pb.BlockWriteRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_BLOCK_WRITE_RESPONSE, &pb.BlockWriteResponse{Error: "invalid request"})
	}
	written, err := h.blockSvc.Write(ctx, req.Path, req.Offset, req.Data)
	if err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_BLOCK_WRITE_RESPONSE, &pb.BlockWriteResponse{Error: err.Error()})
	}
	size, _ := h.blockSvc.Size(ctx, req.Path)
	_ = h.metaSvc.SetSize(ctx, req.Path, size)
	return makeEnvelope(env.RequestId, pb.MessageType_BLOCK_WRITE_RESPONSE, &pb.BlockWriteResponse{Written: written})
}

func (h *Handler) handleSetSize(ctx context.Context, conn *quiclib.Conn, env *pb.Envelope) *pb.Envelope {
	if !h.isAuthorized(conn) {
		return makeEnvelope(env.RequestId, pb.MessageType_SET_SIZE_RESPONSE, &pb.SetSizeResponse{Error: "unauthorized"})
	}
	var req pb.SetSizeRequest
	if err := proto.Unmarshal(env.Payload, &req); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_SET_SIZE_RESPONSE, &pb.SetSizeResponse{Error: "invalid request"})
	}
	if err := h.blockSvc.Truncate(ctx, req.Path, req.Size); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_SET_SIZE_RESPONSE, &pb.SetSizeResponse{Error: err.Error()})
	}
	if err := h.metaSvc.SetSize(ctx, req.Path, req.Size); err != nil {
		return makeEnvelope(env.RequestId, pb.MessageType_SET_SIZE_RESPONSE, &pb.SetSizeResponse{Error: err.Error()})
	}
	return makeEnvelope(env.RequestId, pb.MessageType_SET_SIZE_RESPONSE, &pb.SetSizeResponse{})
}

func (h *Handler) isAuthorized(conn *quiclib.Conn) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	remote := conn.RemoteAddr().String()
	for _, c := range h.clients {
		if c.RemoteAddr().String() == remote {
			return true
		}
	}
	return false
}

func (h *Handler) broadcastInvalidate(path string) {
	inv := &pb.InvalidateRequest{Path: path}
	env := makeEnvelope(0, pb.MessageType_INVALIDATE, inv)

	h.mu.RLock()
	defer h.mu.RUnlock()

	for clientID, conn := range h.clients {
		go func(id string, c *quiclib.Conn) {
			stream, err := c.OpenStreamSync(context.Background())
			if err != nil {
				log.Printf("[push] Open stream for %s: %v", id, err)
				return
			}
			defer stream.Close()
			if err := writeEnvelope(stream, env); err != nil {
				log.Printf("[push] Write invalidate to %s: %v", id, err)
			}
		}(clientID, conn)
	}
}

// --- Wire format: 4-byte length prefix + protobuf Envelope ---

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
	return &pb.Envelope{
		RequestId: reqID,
		Type:      msgType,
		Payload:   payload,
	}
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

func generateTLSConfig() (*tls.Config, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"diskwave-quic"},
	}, nil
}