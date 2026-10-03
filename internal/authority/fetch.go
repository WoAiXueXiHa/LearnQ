package authority

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/imagestore"
	"golang.org/x/net/html"
)

func ValidateURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return nil, errors.New("authority URL must be HTTPS on port 443 without credentials")
	}
	u.Fragment = ""
	return u, nil
}

func Fetch(ctx context.Context, source domain.AuthoritySource, version string) (domain.AuthoritySnapshot, error) {
	var snapshot domain.AuthoritySnapshot
	u, err := ValidateURL(source.URL)
	if err != nil {
		return snapshot, err
	}
	allow := func(target *url.URL) error {
		if target.Scheme != "https" || (target.Port() != "" && target.Port() != "443") || !strings.EqualFold(target.Hostname(), source.Hostname) {
			return errors.New("source redirect leaves registered HTTPS host")
		}
		return nil
	}
	if err := allow(u); err != nil {
		return snapshot, err
	}
	client := imagestore.PublicHTTPClient(allow)
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return snapshot, err
	}
	response, err := client.Do(req)
	if err != nil {
		return snapshot, errors.New("authority source fetch failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return snapshot, errors.New("authority source did not return HTTP 200")
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || (mediaType != "text/html" && mediaType != "text/plain" && mediaType != "text/markdown") {
		return snapshot, errors.New("authority source content type unsupported")
	}
	const maxBytes = 2 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return snapshot, err
	}
	if len(body) == 0 || len(body) > maxBytes || !utf8.Valid(body) {
		return snapshot, errors.New("authority source is empty, oversized or not UTF-8")
	}
	title, text := source.Topic, string(body)
	if mediaType == "text/html" {
		doc, err := html.Parse(strings.NewReader(string(body)))
		if err != nil {
			return snapshot, err
		}
		var parts []string
		var walk func(*html.Node, bool)
		walk = func(n *html.Node, inTitle bool) {
			if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "noscript") {
				return
			}
			if n.Type == html.ElementNode && n.Data == "title" {
				inTitle = true
			}
			if n.Type == html.TextNode {
				value := strings.TrimSpace(n.Data)
				if value != "" {
					parts = append(parts, value)
					if inTitle {
						title = value
					}
				}
			}
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				walk(child, inTitle)
			}
		}
		walk(doc, false)
		text = strings.Join(parts, "\n")
	}
	if strings.TrimSpace(text) == "" {
		return snapshot, errors.New("authority source contains no readable text")
	}
	sum := sha256.Sum256(body)
	textSum := sha256.Sum256([]byte(text))
	now := time.Now().UTC()
	return domain.AuthoritySnapshot{AuthoritySourceID: source.ID, FinalURL: response.Request.URL.String(), Title: title, VersionLabel: version, Content: string(body), ExtractedText: text, ContentHash: hex.EncodeToString(sum[:]), TextHash: hex.EncodeToString(textSum[:]), AccessedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour)}, nil
}
