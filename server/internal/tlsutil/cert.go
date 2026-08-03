package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"time"
)

// LoadOrCreate loads the TLS cert+key from the database, or generates a new
// one and persists it. This ensures the cert fingerprint stays stable across
// server restarts — client cert pins remain valid after upgrades.
func LoadOrCreate(db *sql.DB, nextProtos ...string) (*tls.Config, error) {
	// Ensure table exists
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS diskwave_tls (
		id      TEXT PRIMARY KEY,
		cert_pem TEXT NOT NULL,
		key_pem  TEXT NOT NULL
	)`)
	if err != nil {
		return nil, err
	}

	var certPEM, keyPEM string
	err = db.QueryRow(`SELECT cert_pem, key_pem FROM diskwave_tls WHERE id = 'server'`).Scan(&certPEM, &keyPEM)
	if err == nil {
		// Loaded from DB
		tlsCert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
		if err != nil {
			return nil, err
		}
		return &tls.Config{
			Certificates: []tls.Certificate{tlsCert},
			NextProtos:   nextProtos,
			MinVersion:   tls.VersionTLS13,
		}, nil
	}

	// Not found — generate and persist
	cfg, certPEMBytes, keyPEMBytes, err := generateRaw(nextProtos...)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`INSERT INTO diskwave_tls (id, cert_pem, key_pem) VALUES ('server', $1, $2)`,
		string(certPEMBytes), string(keyPEMBytes))
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// generateRaw creates a fresh self-signed cert and returns the config plus raw PEM bytes.
func generateRaw(nextProtos ...string) (*tls.Config, []byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, nil, err
	}

	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "diskwave"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}

	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, nil, err
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, nil, nil, err
	}

	cfg := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   nextProtos,
		MinVersion:   tls.VersionTLS13,
	}
	return cfg, certPEM, keyPEM, nil
}

// GenerateSelfSigned creates an ephemeral self-signed cert (not persisted).
// Use for localhost-only services like the mgmt API where pinning is not needed.
func GenerateSelfSigned(nextProtos ...string) (*tls.Config, error) {
	cfg, _, _, err := generateRaw(nextProtos...)
	return cfg, err
}

// CertFingerprint returns the SHA-256 hex fingerprint of the first certificate
// in a tls.Config. Used for cert pinning: server sends this during pairing,
// client stores it in Keychain and verifies on every subsequent connection.
func CertFingerprint(cfg *tls.Config) string {
	if len(cfg.Certificates) == 0 {
		return ""
	}
	cert := cfg.Certificates[0]
	if len(cert.Certificate) == 0 {
		return ""
	}
	sum := sha256.Sum256(cert.Certificate[0])
	return hex.EncodeToString(sum[:])
}