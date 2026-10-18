package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DefaultImageBase is ESPN's image CDN. Its resizer serves player headshots
// and team logos at phone sizes.
const DefaultImageBase = "https://a.espncdn.com"

// Images fetches player headshots and team logos from ESPN's CDN and caches
// them, so the app loads them from this server: the page's CSP stays
// same-origin, and the phone never contacts ESPN.
type Images struct {
	Base   string // DefaultImageBase when empty
	Client *http.Client
	// TeamLogo returns the logo URL a fantasy team's manager chose. Those
	// can be on any host, so they're fetched with a client that refuses
	// private and loopback addresses.
	TeamLogo func(id int) string
	// LogoClient fetches team logos (a public-addresses-only client when nil).
	LogoClient *http.Client

	mu    sync.Mutex
	cache map[string]image
	order []string // insertion order, for eviction
}

type image struct {
	body    []byte
	ctype   string
	fetched time.Time
	missing bool // ESPN has no image; remembered briefly so it isn't refetched on every view
}

const (
	maxImageBytes  = 512 << 10
	maxCachedItems = 400
	imageTTL       = 7 * 24 * time.Hour
	missingTTL     = 6 * time.Hour
)

var teamAbbrev = regexp.MustCompile(`^[a-z]{2,4}$`)

// imagePath maps an app image path to the ESPN CDN path it comes from:
// "p/4241389" is a player headshot, "t/jax" an NFL team logo. Nothing
// else is accepted, so the proxy can only fetch those kinds of images from
// the CDN. ("f/3", a fantasy team's logo, is resolved separately.)
func imagePath(kind, key string) (string, bool) {
	switch kind {
	case "p":
		id, err := strconv.Atoi(key)
		if err != nil || id <= 0 || id > 1e9 {
			return "", false
		}
		return fmt.Sprintf("/combiner/i?img=/i/headshots/nfl/players/full/%d.png&w=160&h=116&scale=crop&cquality=80", id), true
	case "t":
		key = strings.ToLower(key)
		if !teamAbbrev.MatchString(key) {
			return "", false
		}
		return fmt.Sprintf("/combiner/i?img=/i/teamlogos/nfl/500/%s.png&w=96&h=96", key), true
	}
	return "", false
}

// ServeHTTP serves /img/{kind}/{key}.
func (im *Images) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSuffix(r.PathValue("key"), ".png")
	var img image
	var err error
	if r.PathValue("kind") == "f" {
		u, ok := im.teamLogoURL(key)
		if !ok {
			http.NotFound(w, r)
			return
		}
		img, err = im.get(r.Context(), u, true)
	} else {
		path, ok := imagePath(r.PathValue("kind"), key)
		if !ok {
			http.NotFound(w, r)
			return
		}
		base := im.Base
		if base == "" {
			base = DefaultImageBase
		}
		img, err = im.get(r.Context(), base+path, false)
	}
	if err != nil {
		http.Error(w, "image unavailable", http.StatusBadGateway)
		return
	}
	if img.missing {
		w.Header().Set("Cache-Control", "private, max-age=3600")
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", img.ctype)
	w.Header().Set("Cache-Control", "private, max-age=604800")
	_, _ = w.Write(img.body)
}

// teamLogoURL looks up a fantasy team's logo. The app only sends the team
// ID; the URL comes from the league data and must be https.
func (im *Images) teamLogoURL(key string) (string, bool) {
	id, err := strconv.Atoi(key)
	if err != nil || id <= 0 || id > 1000 || im.TeamLogo == nil {
		return "", false
	}
	raw := im.TeamLogo(id)
	u, err := url.Parse(raw)
	if raw == "" || err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" {
		return "", false
	}
	return u.String(), true
}

func (im *Images) get(ctx context.Context, path string, untrusted bool) (image, error) {
	now := time.Now()
	im.mu.Lock()
	if c, ok := im.cache[path]; ok {
		ttl := imageTTL
		if c.missing {
			ttl = missingTTL
		}
		if now.Sub(c.fetched) < ttl {
			im.mu.Unlock()
			return c, nil
		}
	}
	im.mu.Unlock()

	img, err := im.fetch(ctx, path, untrusted)
	if err != nil {
		return image{}, err
	}
	img.fetched = now
	im.mu.Lock()
	defer im.mu.Unlock()
	if im.cache == nil {
		im.cache = map[string]image{}
	}
	if _, ok := im.cache[path]; !ok {
		im.order = append(im.order, path)
		for len(im.order) > maxCachedItems {
			delete(im.cache, im.order[0])
			im.order = im.order[1:]
		}
	}
	im.cache[path] = img
	return img, nil
}

func (im *Images) fetch(ctx context.Context, rawURL string, untrusted bool) (image, error) {
	client := im.Client
	if untrusted {
		client = im.LogoClient
		if client == nil {
			client = publicOnlyClient
		}
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return image{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return image{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return image{missing: true}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return image{}, fmt.Errorf("image CDN: %s", resp.Status)
	}
	ctype, _, _ := strings.Cut(resp.Header.Get("Content-Type"), ";")
	ctype = strings.ToLower(strings.TrimSpace(ctype))
	if ctype != "image/png" && ctype != "image/jpeg" && ctype != "image/webp" && ctype != "image/gif" {
		return image{}, fmt.Errorf("image CDN: unexpected content type %q", ctype)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return image{}, err
	}
	if len(body) > maxImageBytes {
		return image{}, fmt.Errorf("image CDN: image too large")
	}
	return image{body: body, ctype: ctype}, nil
}

// publicOnlyClient fetches URLs chosen by other people (fantasy team logos).
// It connects only to public addresses, checked after DNS resolution and
// on every redirect, so a logo URL can't reach this machine or its network.
var publicOnlyClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		Proxy: nil, // connect directly, so the address check sees the real destination
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
			Control: func(network, address string, _ syscall.RawConn) error {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return err
				}
				ip, err := netip.ParseAddr(host)
				if err != nil {
					return err
				}
				if !publicAddr(ip) {
					return errors.New("refusing to fetch a logo from a non-public address")
				}
				return nil
			},
		}).DialContext,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		MaxResponseHeaderBytes: 16 << 10,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" {
			return errors.New("logo redirect is not https")
		}
		return nil
	},
}

func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() && !netip.MustParsePrefix("100.64.0.0/10").Contains(ip) && // CGNAT, incl. Tailscale
		!netip.MustParsePrefix("fc00::/7").Contains(ip)
}
