package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"time"
)

// GenerateSelfSigned creates an ephemeral ECDSA P-256 self-signed cert valid
// for 10 years. Suitable for both QUIC and TLS-wrapped TCP transports where
// the client explicitly trusts this specific cert (pin or InsecureSkipVerify
// on a private network).
func GenerateSelfSigned(nextProtos ...string) (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
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
		return nil, err
	}

	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   nextProtos,
		MinVersion:   tls.VersionTLS13,
	}, nil
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