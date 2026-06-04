// Package cert 负责生成和管理 MITM 代理所需的 CA 根证书和动态子证书
package cert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"sync"
	"time"
)

const (
	CACertFile = "ca.crt"
	CAKeyFile  = "ca.key"
)

// CA 持有根证书和私钥，以及动态签发缓存
type CA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	tlsCert tls.Certificate
	cache   map[string]*tls.Certificate
	mu      sync.RWMutex
}

// LoadOrCreate 加载已有 CA，或生成新的 CA 证书和私钥
func LoadOrCreate() (*CA, error) {
	if fileExists(CACertFile) && fileExists(CAKeyFile) {
		return load()
	}
	return create()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func load() (*CA, error) {
	certPEM, err := os.ReadFile(CACertFile)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(CAKeyFile)
	if err != nil {
		return nil, err
	}
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	x509Cert, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		return nil, err
	}
	ecKey, ok := tlsCert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, err
	}
	return &CA{
		cert:    x509Cert,
		key:     ecKey,
		tlsCert: tlsCert,
		cache:   make(map[string]*tls.Certificate),
	}, nil
}

func create() (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "ChessMaster MITM CA",
			Organization: []string{"ChessMaster Proxy"},
		},
		NotBefore:             time.Now().Add(-10 * time.Minute),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}

	// 保存证书
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if err := os.WriteFile(CACertFile, certPEM, 0644); err != nil {
		return nil, err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(CAKeyFile, keyPEM, 0600); err != nil {
		return nil, err
	}

	x509Cert, _ := x509.ParseCertificate(certDER)
	tlsCert, _ := tls.X509KeyPair(certPEM, keyPEM)

	return &CA{
		cert:    x509Cert,
		key:     key,
		tlsCert: tlsCert,
		cache:   make(map[string]*tls.Certificate),
	}, nil
}

// IssueCert 为指定域名动态签发一个 TLS 证书（带缓存）
func (ca *CA) IssueCert(host string) (*tls.Certificate, error) {
	ca.mu.RLock()
	if c, ok := ca.cache[host]; ok {
		ca.mu.RUnlock()
		return c, nil
	}
	ca.mu.RUnlock()

	ca.mu.Lock()
	defer ca.mu.Unlock()

	// double-check
	if c, ok := ca.cache[host]; ok {
		return c, nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-10 * time.Minute),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, err
	}

	keyDER, _ := x509.MarshalECPrivateKey(key)
	tlsCert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		return nil, err
	}

	ca.cache[host] = &tlsCert
	return &tlsCert, nil
}

// TLSConfig 返回可用于 HTTPS 拦截的 TLS 服务端配置
func (ca *CA) TLSConfig(host string) (*tls.Config, error) {
	cert, err := ca.IssueCert(host)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{*cert},
		// 不校验客户端证书
	}, nil
}
