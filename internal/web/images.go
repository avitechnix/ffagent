package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
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
// "p/4241389" is a player headshot, "t/jax" a team logo. Nothing else is
// accepted, so the proxy can only ever fetch those two kinds of images.
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
	path, ok := imagePath(r.PathValue("kind"), key)
	if !ok {
		http.NotFound(w, r)
		return
	}
	img, err := im.get(r.Context(), path)
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

func (im *Images) get(ctx context.Context, path string) (image, error) {
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

	img, err := im.fetch(ctx, path)
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

func (im *Images) fetch(ctx context.Context, path string) (image, error) {
	base := im.Base
	if base == "" {
		base = DefaultImageBase
	}
	client := im.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
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
	ctype := resp.Header.Get("Content-Type")
	if ctype != "image/png" && ctype != "image/jpeg" && ctype != "image/webp" {
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
