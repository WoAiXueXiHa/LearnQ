package imagestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	_ "golang.org/x/image/webp"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

// RemoteImage is a validated snapshot; callers persist its provenance separately.
type RemoteImage struct {
	Body          []byte
	SHA256        string
	MediaType     string
	Extension     string
	Width, Height int
}

// PublicImageAddress rejects non-public targets, including IPv4-mapped IPv6.
func PublicImageAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	for _, block := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001::/32", "2002::/16"} {
		if netip.MustParsePrefix(block).Contains(addr) {
			return false
		}
	}
	return true
}

func validateRemoteURL(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return errors.New("image URL must be HTTP(S) without credentials")
	}
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		return errors.New("image URL port is not allowed")
	}
	return nil
}

// FetchRemoteImage pins each connection to a checked DNS result. Redirects use
// the same transport; proxies and connection reuse cannot bypass address checks.
func FetchRemoteImage(ctx context.Context, rawURL string) (*RemoteImage, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("invalid image URL")
	}
	if err = validateRemoteURL(u); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	transport := publicTransport()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many image redirects")
		}
		return validateRemoteURL(req.URL)
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("remote image download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image HTTP status %d", resp.StatusCode)
	}
	if resp.ContentLength > MaxBytes {
		return nil, errors.New("remote image exceeds size limit")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || len(body) > MaxBytes {
		return nil, errors.New("remote image size outside limits")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || (format != "jpeg" && format != "png" && format != "gif" && format != "webp") {
		return nil, errors.New("remote image must be valid JPEG, PNG, GIF or WebP")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 20_000_000 {
		return nil, errors.New("remote image exceeds pixel limit")
	}
	if _, _, err := image.Decode(bytes.NewReader(body)); err != nil {
		return nil, errors.New("remote image is corrupt")
	}
	sum := sha256.Sum256(body)
	result := &RemoteImage{Body: body, SHA256: hex.EncodeToString(sum[:]), MediaType: "image/png", Extension: ".png", Width: cfg.Width, Height: cfg.Height}
	if format == "jpeg" {
		result.MediaType = "image/jpeg"
		result.Extension = ".jpg"
	}
	if format == "gif" {
		result.MediaType = "image/gif"
		result.Extension = ".gif"
	}
	if format == "webp" {
		result.MediaType = "image/webp"
		result.Extension = ".webp"
	}
	return result, nil
}

func publicTransport() *http.Transport {
	transport := &http.Transport{
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, errors.New("image host resolution failed")
			}
			if len(addresses) == 0 {
				return nil, errors.New("image host has no addresses")
			}
			for _, ip := range addresses {
				if !PublicImageAddress(ip) {
					return nil, errors.New("image host resolves to a prohibited address")
				}
			}
			var last error
			for _, ip := range addresses {
				conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				last = err
			}
			return nil, last
		},
	}
	return transport
}

// PublicHTTPClient applies the same pinned-address restrictions to source fetches.
func PublicHTTPClient(allow func(*url.URL) error) *http.Client {
	return &http.Client{Transport: publicTransport(), Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if err := validateRemoteURL(req.URL); err != nil {
			return err
		}
		if allow != nil {
			return allow(req.URL)
		}
		return nil
	}}
}
