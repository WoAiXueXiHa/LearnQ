package imagestore

import (
	"context"
	"net/netip"
	"net/url"
	"testing"
)

func TestPublicImageAddress(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.100.100.200", "0.0.0.0", "192.0.2.1", "198.18.0.1", "224.0.0.1", "::1", "fe80::1", "fc00::1", "::ffff:127.0.0.1", "2001:db8::1", "2002:7f00:1::"} {
		if PublicImageAddress(netip.MustParseAddr(raw)) {
			t.Errorf("allowed prohibited address %s", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !PublicImageAddress(netip.MustParseAddr(raw)) {
			t.Errorf("rejected public address %s", raw)
		}
	}
}

func TestRemoteURLRestrictions(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "ftp://example.com/a.png", "https://user:pass@example.com/a.png", "https://example.com:8080/a.png", "/a.png"} {
		u, err := url.Parse(raw)
		if err == nil && validateRemoteURL(u) == nil {
			t.Errorf("allowed URL %s", raw)
		}
	}
}

func TestRemoteImageRejectsLocalConnection(t *testing.T) {
	// Literal targets need no external DNS or model requests.
	for _, raw := range []string{"http://127.0.0.1/a.png", "http://[::1]/a.png", "http://169.254.169.254/a.png"} {
		if _, err := FetchRemoteImage(context.Background(), raw); err == nil {
			t.Errorf("allowed local fetch %s", raw)
		}
	}
}
