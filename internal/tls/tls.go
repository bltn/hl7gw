package tls

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
)

var tlsVersions = map[string]uint16{
	"1.2": tls.VersionTLS12,
	"1.3": tls.VersionTLS13,
}

var Versions = slices.Sorted(maps.Keys(tlsVersions))

func LoadServerCertAndKey(certPath, keyPath string) (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("loading server certificate and key: %w", err)
	}
	return cert, nil
}

func LoadCAPool(path string) (*x509.CertPool, []*x509.Certificate, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304 -- path is operator-supplied config, no traversal boundary
	if err != nil {
		return nil, nil, fmt.Errorf("reading client CA file: %w", err)
	}

	pool := x509.NewCertPool()
	var certs []*x509.Certificate
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing CA certificate %d: %w", len(certs)+1, err)
		}
		pool.AddCert(cert)
		certs = append(certs, cert)
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, nil, errors.New("client CA file has trailing data after the last PEM block")
	}
	if len(certs) == 0 {
		return nil, nil, errors.New("client CA file contains no certificates")
	}
	return pool, certs, nil
}

func LoadMinimumVersion(version string) (uint16, error) {
	v, ok := tlsVersions[version]
	if !ok {
		return 0, fmt.Errorf("invalid tls min_version %q, valid versions: %s", version, strings.Join(Versions, ", "))
	}
	return v, nil
}
