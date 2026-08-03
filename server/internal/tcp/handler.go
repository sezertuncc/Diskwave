package tcp

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"sync"

	"github.com/diskwave/server/internal/auth"
	"github.com/diskwave/server/internal/blocks"
	"github.com/diskwave/server/internal/dispatch"
	"github.com/diskwave/server/internal/metadata"
	pb "github.com/diskwave/server/internal/proto"
	"github.com/diskwave/server/internal/tlsutil"
	"google.golang.org/protobuf/proto"
)

type Handler struct {
	disp *dispatch.Dispatcher

	mu    sync.RWMutex
	conns map[net.Conn]string // conn → clientID (authenticated connections)
}

func NewHandler(a *auth.Manager, m *metadata.Service, b *blocks.Service) *Handler {
	h := &Handler{
		conns: make(map[net.Conn]string),
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
// Sends an INVALIDATE push to every authenticated TCP connection.
func (h *Handler) BroadcastInvalidate(path string) {
	inv := &pb.InvalidateRequest{Path: path}
	env := dispatch.MakeEnvelope(0, pb.MessageType_INVALIDATE, inv)

	h.mu.RLock()
	defer h.mu.RUnlock()

	for conn := range h.conns {
		go func(c net.Conn) {
			if err := writeEnvelope(c, env); err != nil {
				log.Printf("[tcp/push] invalidate %s: %v", path, err)
			}
		}(conn)
	}
}

func (h *Handler) ListenAndServe(addr string, tlsConf *tls.Config) error {
	h.disp.CertFingerprint = tlsutil.CertFingerprint(tlsConf)

	ln, err := tls.Listen("tcp", addr, tlsConf)
	if err != nil {
		return fmt.Errorf("tcp listen: %w", err)
	}
	defer ln.Close()

	log.Printf("[tcp] Listening on %s (TLS)", addr)

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
	defer func() {
		h.mu.Lock()
		delete(h.conns, conn)
		h.mu.Unlock()
		conn.Close()
	}()

	ctx := context.Background()

	for {
		env, err := readEnvelope(conn)
		if err != nil {
			if err != io.EOF {
				log.Printf("[tcp] Read error from %s: %v", remote, err)
			}
			return
		}

		var resp *pb.Envelope

		switch env.Type {
		case pb.MessageType_PAIR_REQUEST:
			resp = h.disp.HandlePair(env, remote)

		case pb.MessageType_CONNECT_REQUEST:
			var respEnv *pb.Envelope
			var clientID string
			respEnv, clientID = h.disp.HandleConnect(env)
			if clientID != "" {
				h.mu.Lock()
				h.conns[conn] = clientID
				h.mu.Unlock()
			}
			resp = respEnv

		default:
			if !h.isAuthorized(conn) {
				resp = makeError(env, "unauthorized")
				break
			}
			switch env.Type {
			case pb.MessageType_TUNNEL_OPEN_REQUEST:
				// Upgrade this connection to an SMB tunnel.
				// Send the response, then hand off raw bytes between client and Samba.
				openResp := dispatch.MakeEnvelope(env.RequestId, pb.MessageType_TUNNEL_OPEN_RESPONSE,
					&pb.TunnelOpenResponse{Ok: true})
				if err := writeEnvelope(conn, openResp); err != nil {
					log.Printf("[tcp/tunnel] send open response: %v", err)
					return
				}
				h.runSMBTunnel(conn)
				return // connection is consumed by the tunnel goroutine

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
				log.Printf("[tcp] Unknown message type: %v", env.Type)
				continue
			}
		}

		if resp != nil {
			if err := writeEnvelope(conn, resp); err != nil {
				log.Printf("[tcp] Write error to %s: %v", remote, err)
				return
			}
		}
	}
}

// runSMBTunnel dials Samba on localhost and pipes raw bytes between the TLS
// client connection and Samba. The caller must not touch conn after this call.
func (h *Handler) runSMBTunnel(conn net.Conn) {
	samba, err := net.Dial("tcp", "127.0.0.1:445")
	if err != nil {
		log.Printf("[tcp/tunnel] dial samba: %v", err)
		return
	}
	defer samba.Close()

	log.Printf("[tcp/tunnel] SMB tunnel open for %s", conn.RemoteAddr())
	done := make(chan struct{}, 2)
	copy := func(dst, src net.Conn) {
		io.Copy(dst, src) //nolint:errcheck
		dst.Close()
		done <- struct{}{}
	}
	go copy(samba, conn)
	go copy(conn, samba)
	<-done
	log.Printf("[tcp/tunnel] SMB tunnel closed for %s", conn.RemoteAddr())
}

func (h *Handler) isAuthorized(conn net.Conn) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[conn]
	return ok
}

func makeError(env *pb.Envelope, msg string) *pb.Envelope {
	respType := env.Type + 1
	return dispatch.MakeEnvelope(env.RequestId, respType, &pb.StatResponse{Error: msg})
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