package quic

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/diskwave/server/internal/auth"
	"github.com/diskwave/server/internal/blocks"
	"github.com/diskwave/server/internal/dispatch"
	"github.com/diskwave/server/internal/metadata"
	pb "github.com/diskwave/server/internal/proto"
	"github.com/diskwave/server/internal/tlsutil"
	quiclib "github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"
)

type Handler struct {
	disp *dispatch.Dispatcher

	mu      sync.RWMutex
	clients map[string]*quiclib.Conn  // clientID → conn (for broadcast)
	connIDs map[*quiclib.Conn]string  // conn → clientID (for isAuthorized)
}

func NewHandler(a *auth.Manager, m *metadata.Service, b *blocks.Service) *Handler {
	h := &Handler{
		clients: make(map[string]*quiclib.Conn),
		connIDs: make(map[*quiclib.Conn]string),
	}
	h.disp = &dispatch.Dispatcher{
		AuthMgr:   a,
		MetaSvc:   m,
		BlockSvc:  b,
		Broadcast: h,
	}
	return h
}

// BroadcastInvalidate implements dispatch.Broadcaster.
// Opens a new QUIC stream per client and sends an INVALIDATE message.
func (h *Handler) BroadcastInvalidate(path string) {
	inv := &pb.InvalidateRequest{Path: path}
	env := dispatch.MakeEnvelope(0, pb.MessageType_INVALIDATE, inv)

	h.mu.RLock()
	defer h.mu.RUnlock()

	for clientID, conn := range h.clients {
		go func(id string, c *quiclib.Conn) {
			stream, err := c.OpenStreamSync(context.Background())
			if err != nil {
				log.Printf("[quic/push] open stream for %s: %v", id, err)
				return
			}
			defer stream.Close()
			if err := writeEnvelope(stream, env); err != nil {
				log.Printf("[quic/push] write invalidate to %s: %v", id, err)
			}
		}(clientID, conn)
	}
}

func (h *Handler) ListenAndServe(addr string) error {
	tlsConf, err := tlsutil.GenerateSelfSigned("diskwave-quic")
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

	defer func() {
		h.mu.Lock()
		if id, ok := h.connIDs[conn]; ok {
			delete(h.clients, id)
			delete(h.connIDs, conn)
		}
		h.mu.Unlock()
	}()

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
	var resp *pb.Envelope

	switch env.Type {
	case pb.MessageType_PAIR_REQUEST:
		resp = h.disp.HandlePair(env)

	case pb.MessageType_CONNECT_REQUEST:
		var clientID string
		resp, clientID = h.disp.HandleConnect(env)
		if clientID != "" {
			h.mu.Lock()
			h.clients[clientID] = conn
			h.connIDs[conn] = clientID
			h.mu.Unlock()
		}

	default:
		if !h.isAuthorized(conn) {
			resp = dispatch.MakeEnvelope(env.RequestId, env.Type+1, &pb.StatResponse{Error: "unauthorized"})
			break
		}
		switch env.Type {
		case pb.MessageType_STAT_REQUEST:
			resp = h.disp.HandleStat(ctx, env)
		case pb.MessageType_READDIR_REQUEST:
			resp = h.disp.HandleReadDir(ctx, env)
		case pb.MessageType_MKDIR_REQUEST:
			resp = h.disp.HandleMkdir(ctx, env)
		case pb.MessageType_MKNOD_REQUEST:
			resp = h.disp.HandleMknod(ctx, env)
		case pb.MessageType_RENAME_REQUEST:
			resp = h.disp.HandleRename(ctx, env)
		case pb.MessageType_DELETE_REQUEST:
			resp = h.disp.HandleDelete(ctx, env)
		case pb.MessageType_BLOCK_READ_REQUEST:
			resp = h.disp.HandleBlockRead(ctx, env)
		case pb.MessageType_BLOCK_WRITE_REQUEST:
			resp = h.disp.HandleBlockWrite(ctx, env)
		case pb.MessageType_SET_SIZE_REQUEST:
			resp = h.disp.HandleSetSize(ctx, env)
		case pb.MessageType_BLOCK_EXISTS_REQUEST:
			resp = h.disp.HandleBlockExists(ctx, env)
		default:
			log.Printf("[quic] Unknown message type: %v", env.Type)
			return
		}
	}

	if resp != nil {
		if err := writeEnvelope(stream, resp); err != nil {
			log.Printf("[quic] Write response error: %v", err)
		}
	}
}

func (h *Handler) isAuthorized(conn *quiclib.Conn) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.connIDs[conn]
	return ok
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