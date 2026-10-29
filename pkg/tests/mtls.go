package tests

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
)

const (
	// Certificates only have to outlive a single spec, with slack for clock skew.
	mtlsCertValidity  = 24 * time.Hour
	mtlsCertClockSkew = time.Hour
)

// MTLSCerts holds the PEM-encoded CA plus the server and client keypairs signed by it.
type MTLSCerts struct {
	CACert     string
	ServerCert string
	ServerKey  string
	ClientCert string
	ClientKey  string
}

// ServiceDNSNames expands every service into the three in-cluster resolvable forms,
// e.g. "vminsert-x" -> "vminsert-x.ns.svc.cluster.local", "vminsert-x.ns.svc", "vminsert-x.ns".
// A wildcard prefix is preserved, so "*.vmstorage-x" covers headless per-pod hostnames.
func ServiceDNSNames(namespace string, services ...string) []string {
	names := make([]string, 0, len(services)*3)
	for _, svc := range services {
		base := svc + "." + namespace
		names = append(names, base+".svc.cluster.local", base+".svc", base)
	}
	return names
}

// NewMTLSCerts generates a short-lived CA and a server/client keypair signed by it.
// The server cert is valid for serverDNSNames; the client cert is for client auth only.
func NewMTLSCerts(caCN, serverCN, clientCN string, serverDNSNames []string) (MTLSCerts, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		return MTLSCerts{}, err
	}

	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: caCN},
		NotBefore:             now.Add(-mtlsCertClockSkew),
		NotAfter:              now.Add(mtlsCertValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(cryptorand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return MTLSCerts{}, err
	}

	serverCert, serverKey, err := signCert(ca, caKey, serverCN,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, serverDNSNames)
	if err != nil {
		return MTLSCerts{}, err
	}
	clientCert, clientKey, err := signCert(ca, caKey, clientCN,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil)
	if err != nil {
		return MTLSCerts{}, err
	}

	return MTLSCerts{
		CACert:     encodePEM("CERTIFICATE", caDER),
		ServerCert: serverCert,
		ServerKey:  serverKey,
		ClientCert: clientCert,
		ClientKey:  clientKey,
	}, nil
}

// ApplySecret stores the certs in a secret under the conventional
// ca.crt/server.crt/server.key/client.crt/client.key keys.
func (c MTLSCerts) ApplySecret(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, name string) error {
	return NewSecretBuilder(name).
		WithStringData("ca.crt", c.CACert).
		WithStringData("server.crt", c.ServerCert).
		WithStringData("server.key", c.ServerKey).
		WithStringData("client.crt", c.ClientCert).
		WithStringData("client.key", c.ClientKey).
		Apply(ctx, t, kubeOpts)
}

// TCPProbe builds a TCP-socket probe, which unlike an HTTP health check does not
// need to complete the mTLS handshake.
func TCPProbe(port int) map[string]interface{} {
	return map[string]interface{}{"tcpSocket": map[string]interface{}{"port": port}}
}

func signCert(ca *x509.Certificate, caKey *ecdsa.PrivateKey, commonName string, usages []x509.ExtKeyUsage, dnsNames []string) (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := cryptorand.Int(cryptorand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    now.Add(-mtlsCertClockSkew),
		NotAfter:     now.Add(mtlsCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  usages,
		DNSNames:     dnsNames,
	}
	certDER, err := x509.CreateCertificate(cryptorand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}

	return encodePEM("CERTIFICATE", certDER), encodePEM("EC PRIVATE KEY", keyDER), nil
}

func encodePEM(blockType string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}))
}
