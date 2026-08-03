package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const codeChars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O/1/I confusion
const codeLen = 6
const codeExpiry = 10 * time.Minute

type ClientRecord struct {
	ID          string    `json:"id"`
	ConnectedAt time.Time `json:"connected_at"`
}

// SMBCredentials are returned to the Mac client after pairing so it can
// mount the Samba share directly via mount_smbfs — no custom protocol needed.
type SMBCredentials struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Share    string `json:"share"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type Manager struct {
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey

	mu          sync.Mutex
	currentCode string
	codeExpires time.Time
	clients     map[string]ClientRecord // clientID → record
	revoked     map[string]struct{}     // revoked clientIDs
	deviceCreds map[string]SMBCredentials // clientID → per-device SMB credentials
}

func NewManager() (*Manager, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate keypair: %w", err)
	}
	m := &Manager{
		privateKey:  priv,
		publicKey:   pub,
		clients:     make(map[string]ClientRecord),
		revoked:     make(map[string]struct{}),
		deviceCreds: make(map[string]SMBCredentials),
	}
	if err := m.rotateCode(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) rotateCode() error {
	code, err := generateCode()
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.currentCode = code
	m.codeExpires = time.Now().Add(codeExpiry)
	m.mu.Unlock()
	return nil
}

func (m *Manager) GetCurrentCode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Now().After(m.codeExpires) {
		_ = m.rotateCode()
	}
	return m.currentCode
}

// StartRotation rotates the pairing code every codeExpiry interval.
func (m *Manager) StartRotation(stop <-chan struct{}) {
	ticker := time.NewTicker(codeExpiry)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = m.rotateCode()
			fmt.Printf("\n[diskwave] New pairing code: %s\n", m.GetCurrentCode())
		case <-stop:
			return
		}
	}
}

func (m *Manager) ValidateCode(code string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Now().After(m.codeExpires) {
		return false
	}
	return strings.EqualFold(code, m.currentCode)
}

type Claims struct {
	jwt.RegisteredClaims
	ClientID string `json:"cid"`
}

func (m *Manager) IssueToken(clientID string) (string, error) {
	m.mu.Lock()
	m.clients[clientID] = ClientRecord{ID: clientID, ConnectedAt: time.Now()}
	m.mu.Unlock()

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(365 * 24 * time.Hour)),
			Issuer:    "diskwave",
			Subject:   clientID,
		},
		ClientID: clientID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	return token.SignedString(m.privateKey)
}

func (m *Manager) ValidateToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodEd25519); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.publicKey, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	m.mu.Lock()
	_, revoked := m.revoked[claims.ClientID]
	m.mu.Unlock()
	if revoked {
		return nil, fmt.Errorf("token revoked")
	}
	return claims, nil
}

func (m *Manager) ListClients() []ClientRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ClientRecord, 0, len(m.clients))
	for _, r := range m.clients {
		if _, rev := m.revoked[r.ID]; !rev {
			out = append(out, r)
		}
	}
	return out
}

func (m *Manager) RevokeClient(_ context.Context, clientID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revoked[clientID] = struct{}{}
	delete(m.clients, clientID)

	// Remove per-device SMB user from Samba so unpair truly revokes disk access
	if creds, ok := m.deviceCreds[clientID]; ok {
		delete(m.deviceCreds, clientID)
		go func(username string) {
			if err := exec.Command("docker", "exec", "diskwave-samba-1",
				"smbpasswd", "-x", username).Run(); err != nil {
				// Non-fatal: container may already be gone, log and continue
				fmt.Printf("[auth] warn: could not remove samba user %s: %v\n", username, err)
			}
		}(creds.Username)
	}
}

// SMBCredsFor returns per-device Samba credentials, generating and registering
// a new user on first call for a given clientID. This ensures unpair truly
// revokes SMB access — each device gets its own username+password.
func (m *Manager) SMBCredsFor(clientID string) SMBCredentials {
	m.mu.Lock()
	if creds, ok := m.deviceCreds[clientID]; ok {
		m.mu.Unlock()
		return creds
	}
	m.mu.Unlock()

	// Derive a deterministic but unguessable username from clientID
	h := sha256.Sum256([]byte(clientID))
	username := "dw_" + hex.EncodeToString(h[:6]) // 15 chars total

	// Generate a random per-device password
	pwBytes := make([]byte, 16)
	_, _ = rand.Read(pwBytes)
	password := hex.EncodeToString(pwBytes)

	// Register the user inside the Samba container:
	//   printf '%s\n%s\n' <pass> <pass> | smbpasswd -a -s <user>
	go func(u, p string) {
		input := fmt.Sprintf("%s\n%s\n", p, p)
		cmd := exec.Command("docker", "exec", "-i", "diskwave-samba-1",
			"smbpasswd", "-a", "-s", u)
		cmd.Stdin = strings.NewReader(input)
		if err := cmd.Run(); err != nil {
			fmt.Printf("[auth] warn: could not add samba user %s: %v\n", u, err)
		}
	}(username, password)

	creds := SMBCredentials{
		Port:     445,
		Share:    "diskwave",
		Username: username,
		Password: password,
	}

	m.mu.Lock()
	m.deviceCreds[clientID] = creds
	m.mu.Unlock()

	return creds
}

func generateCode() (string, error) {
	var sb strings.Builder
	charCount := big.NewInt(int64(len(codeChars)))
	for i := 0; i < codeLen; i++ {
		n, err := rand.Int(rand.Reader, charCount)
		if err != nil {
			return "", err
		}
		sb.WriteByte(codeChars[n.Int64()])
	}
	return sb.String(), nil
}

func NewClientID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}