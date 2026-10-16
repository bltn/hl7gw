package config

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"slices"
	"time"

	"github.com/bltn/hl7gw/internal/metrics"
	itls "github.com/bltn/hl7gw/internal/tls"
)

func (t *TLS) innerConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{t.serverCert},
		ClientCAs:    t.clientCAs,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   t.minVersion,
	}
}

func (t *TLS) ServerLeaf() *x509.Certificate {
	return t.serving.Load().Certificates[0].Leaf
}

func (t *TLS) reloadServerCert() (bool, error) {
	cert, err := itls.LoadServerCertAndKey(t.ServerCertPath, t.ServerPKPath)
	if err != nil {
		return false, err
	}
	if slices.EqualFunc(cert.Certificate, t.serverCert.Certificate, bytes.Equal) {
		return false, nil
	}
	t.serverCert = cert
	return true, nil
}

func (t *TLS) reloadClientCAs() (bool, error) {
	pool, certs, err := itls.LoadCAPool(t.ClientCAPath)
	if err != nil {
		return false, err
	}
	if pool.Equal(t.clientCAs) {
		return false, nil
	}
	t.clientCAs, t.clientCACerts = pool, certs
	return true, nil
}

func (c Config) ReloadTLS(ctx context.Context, log *slog.Logger, m *metrics.Set) {
	ticker := time.NewTicker(time.Duration(c.TLSReloadIntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, ch := range c.Channels {
				t := ch.TCPListener.TLS
				if t.Mode != TLSModeMTLS {
					continue
				}
				cl := log.With("channel", ch.Name)
				certChanged, certErr := t.reloadServerCert()
				if certErr != nil {
					cl.Warn("server cert reload failed, still serving previous cert", "err", certErr.Error())
				}
				caChanged, caErr := t.reloadClientCAs()
				if caErr != nil {
					cl.Warn("client ca reload failed, still verifying against previous pool", "err", caErr.Error())
				}
				if !certChanged && !caChanged {
					continue
				}
				t.serving.Store(t.innerConfig())
				if certChanged {
					leaf := t.ServerLeaf()
					m.Channel(ch.Name).TLSCertExpiry.Set(float64(leaf.NotAfter.Unix()))
					cl.Info("server cert reloaded", "serial", leaf.SerialNumber.String(), "expires", leaf.NotAfter.UTC().Format(time.RFC3339))
				}
				if caChanged {
					cl.Info("client ca pool reloaded", "certs", len(t.clientCACerts))
				}
			}
		}
	}
}
