package harness

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type CA struct {
	CertPath string
	CertPEM  []byte
	cert     *x509.Certificate
	key      *ecdsa.PrivateKey
	dir      string
}

type Leaf struct {
	CertPath string
	KeyPath  string
	CertPEM  []byte
	KeyPEM   []byte
	Cert     *x509.Certificate
}

type ChannelTLS struct {
	ClientCAPath   string
	ServerCertPath string
	ServerPKPath   string
	MinVersion     string
	CA             *CA
	ServerLeaf     *Leaf
	ClientLeaf     *Leaf
}

func NewMTLS(t *testing.T) (*ChannelTLS, error) {
	ca, err := newCA(t.TempDir())
	if err != nil {
		return nil, err
	}
	server, err := ca.issue("server", "server-cert.pem", "server-key.pem")
	if err != nil {
		return nil, err
	}
	client, err := ca.issue("client", "client-cert.pem", "client-key.pem")
	if err != nil {
		return nil, err
	}
	return &ChannelTLS{
		ClientCAPath:   ca.CertPath,
		ServerCertPath: server.CertPath,
		ServerPKPath:   server.KeyPath,
		MinVersion:     "1.2",
		CA:             ca,
		ServerLeaf:     server,
		ClientLeaf:     client,
	}, nil
}

func (ct *ChannelTLS) RotateServerLeaf() (*Leaf, error) {
	leaf, err := ct.CA.issue("server", filepath.Base(ct.ServerCertPath), filepath.Base(ct.ServerPKPath))
	if err != nil {
		return nil, err
	}
	ct.ServerLeaf = leaf
	return leaf, nil
}

func (ct *ChannelTLS) RotateClientCA() (*Leaf, error) {
	ca, err := newCA(ct.CA.dir)
	if err != nil {
		return nil, err
	}
	client, err := ca.issue("client", filepath.Base(ct.ClientLeaf.CertPath), filepath.Base(ct.ClientLeaf.KeyPath))
	if err != nil {
		return nil, err
	}
	ct.ClientLeaf = client
	return client, nil
}

func clientTLS(root *CA, leaf *Leaf) (*tls.Config, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(root.CertPEM) {
		return nil, errors.New("no CA certificate in PEM")
	}
	cfg := &tls.Config{RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}
	if leaf != nil {
		cert, err := tls.X509KeyPair(leaf.CertPEM, leaf.KeyPEM)
		if err != nil {
			return nil, fmt.Errorf("client key pair: %w", err)
		}
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &cert, nil
		}
	}
	return cfg, nil
}

func newCA(dir string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: fmt.Sprintf("harness-ca-%s", serial)},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	certPath := filepath.Join(dir, "ca.pem")
	if err := WriteFileAtomic(certPath, certPEM); err != nil {
		return nil, err
	}
	return &CA{CertPath: certPath, CertPEM: certPEM, cert: cert, key: key, dir: dir}, nil
}

func (ca *CA) issue(cn, certFile, keyFile string) (*Leaf, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certPath := filepath.Join(ca.dir, certFile)
	keyPath := filepath.Join(ca.dir, keyFile)
	if err := WriteFileAtomic(certPath, certPEM); err != nil {
		return nil, err
	}
	if err := WriteFileAtomic(keyPath, keyPEM); err != nil {
		return nil, err
	}
	return &Leaf{CertPath: certPath, KeyPath: keyPath, CertPEM: certPEM, KeyPEM: keyPEM, Cert: cert}, nil
}

func WriteFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}
