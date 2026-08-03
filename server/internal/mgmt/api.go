package mgmt

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/diskwave/server/internal/auth"
	"github.com/diskwave/server/internal/tlsutil"
)

type API struct {
	authMgr *auth.Manager
	start   time.Time
}

func NewAPI(a *auth.Manager) *API {
	return &API{authMgr: a, start: time.Now()}
}

func (a *API) ListenAndServe(addr string) error {
	tlsConf, err := tlsutil.GenerateSelfSigned()
	if err != nil {
		return fmt.Errorf("mgmt tls config: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status",          a.handleStatus)
	mux.HandleFunc("/pair-code",       a.handlePairCode)
	mux.HandleFunc("/clients",         a.handleClients)
	mux.HandleFunc("/clients/",        a.handleClientDelete) // DELETE /clients/{id}
	mux.HandleFunc("/smb-credentials", a.handleSMBCredentials)

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		TLSConfig:    tlsConf,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	log.Printf("[mgmt] Listening on %s (HTTPS)", addr)
	return srv.ListenAndServeTLS("", "") // cert/key embedded in TLSConfig
}

type statusResponse struct {
	OK      bool   `json:"ok"`
	Uptime  string `json:"uptime"`
	Version string `json:"version"`
}

func (a *API) handleStatus(w http.ResponseWriter, r *http.Request) {
	uptime := time.Since(a.start).Round(time.Second).String()
	json.NewEncoder(w).Encode(statusResponse{OK: true, Uptime: uptime, Version: "1.0.0"})
}

type pairCodeResponse struct {
	Code    string `json:"code"`
	Expires string `json:"expires"`
}

func (a *API) handlePairCode(w http.ResponseWriter, r *http.Request) {
	code := a.authMgr.GetCurrentCode()
	json.NewEncoder(w).Encode(pairCodeResponse{
		Code:    code,
		Expires: "10m (rotates automatically)",
	})
}

func (a *API) handleClients(w http.ResponseWriter, r *http.Request) {
	clients := a.authMgr.ListClients()
	json.NewEncoder(w).Encode(clients)
}

func (a *API) handleClientDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/clients/")
	if id == "" {
		http.Error(w, "missing client id", http.StatusBadRequest)
		return
	}
	a.authMgr.RevokeClient(context.Background(), id)
	w.WriteHeader(http.StatusNoContent)
}

// handleSMBCredentials returns Samba mount credentials for an authenticated client.
// Called by the Mac app after pairing to get the info needed for mount_smbfs.
func (a *API) handleSMBCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	bearerToken := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if bearerToken == "" {
		http.Error(w, "missing authorization", http.StatusUnauthorized)
		return
	}
	claims, err := a.authMgr.ValidateToken(bearerToken)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	creds := a.authMgr.SMBCredsFor(claims.ClientID)
	// Host is not stored server-side; client passes its own address.
	// We return everything except host so the client fills it in.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(creds)
}