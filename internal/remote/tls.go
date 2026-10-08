package remote

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
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
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
)

// Cert is the certificate in dir/tls.pem, made on first use: self-signed,
// for this machine's name and addresses, good for ten years. fingerprint
// is its SHA-256, as browsers show it.
func Cert(dir string) (cert tls.Certificate, fingerprint string, err error) {
	path := filepath.Join(dir, "tls.pem")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if data, err = newCert(); err != nil {
			return tls.Certificate{}, "", err
		}
		// why: the CLI and the daemon may both make one; the first link wins and both read it.
		if err = writeOnce(path, data); err == nil || errors.Is(err, os.ErrExist) {
			data, err = os.ReadFile(path)
		}
	}
	if err != nil {
		return tls.Certificate{}, "", err
	}
	if cert, err = tls.X509KeyPair(data, data); err != nil {
		return tls.Certificate{}, "", fmt.Errorf("%s: %w", path, err)
	}
	sum := sha256.Sum256(cert.Certificate[0])
	hexs := make([]string, len(sum))
	for i, b := range sum {
		hexs[i] = fmt.Sprintf("%02X", b)
	}
	return cert, strings.Join(hexs, ":"), nil
}

// writeOnce creates path with data, mode 0600, or fails with
// os.ErrExist when it is there.
func writeOnce(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Link(tmp.Name(), path)
}

func newCert() ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber: new(big.Int).SetBytes(random(16)),
		Subject:      pkix.Name{CommonName: "pitwall " + host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}
	if host != "" {
		tmpl.DNSNames = append(tmpl.DNSNames, host)
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			tmpl.IPAddresses = append(tmpl.IPAddresses, n.IP)
		}
	}
	der, err := x509.CreateCertificate(nil, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb})...), nil
}

// URL is the address a phone opens: [remote] url, else the listen
// address, with this machine's first non-loopback address in place of an
// unspecified host such as 0.0.0.0.
func URL(s config.RemoteSettings) string {
	if s.URL != "" {
		return strings.TrimRight(s.URL, "/") + "/"
	}
	scheme := "http"
	if s.TLS {
		scheme = "https"
	}
	host, port, _ := net.SplitHostPort(s.Listen)
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "localhost"
		addrs, _ := net.InterfaceAddrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
				host = n.IP.String()
				break
			}
		}
	}
	return scheme + "://" + net.JoinHostPort(host, port) + "/"
}

// PairURL is URL with code in its fragment, which browsers never send to
// a server.
func PairURL(s config.RemoteSettings, code string) string { return URL(s) + "#pair=" + code }
