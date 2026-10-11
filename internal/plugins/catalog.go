package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Masterminds/semver/v3"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

const (
	catalogOrigin       = "https://grafana.com/api"
	catalogResponseMax  = 4 << 20
	catalogCacheMax     = 8 << 20
	catalogCacheEntries = 64
	catalogCacheTTL     = 5 * time.Minute
)

var (
	ErrCatalogInput    = errors.New("invalid plugin catalog request")
	ErrCatalogNotFound = errors.New("plugin not found in the Grafana catalog")
)

// CatalogPlugin describes published metadata, not a runtime compatibility guarantee.
type CatalogPlugin struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Type              string `json:"type"`
	Version           string `json:"version"`
	Description       string `json:"description"`
	Author            string `json:"author"`
	Downloads         int64  `json:"downloads"`
	Deprecated        bool   `json:"deprecated"`
	AngularDetected   bool   `json:"angular_detected"`
	GrafanaDependency string `json:"grafana_dependency"`
	URL               string `json:"url"`
	Backend           bool   `json:"backend"`
}

type CatalogLink struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type CatalogPluginDetail struct {
	CatalogPlugin
	Links         []CatalogLink `json:"links"`
	SignatureType string        `json:"signature_type"`
}

type CatalogVersion struct {
	Version           string `json:"version"`
	CreatedAt         string `json:"created_at"`
	GrafanaDependency string `json:"grafana_dependency"`
	AngularDetected   bool   `json:"angular_detected"`
	PlatformAvailable bool   `json:"platform_available"`
}

type CatalogList struct {
	Items []CatalogPlugin `json:"items"`
}

type CatalogVersions struct {
	Items []CatalogVersion `json:"items"`
}

type catalogPackage struct {
	DownloadURL string `json:"downloadUrl"`
}

type catalogRemote struct {
	Slug              string                    `json:"slug"`
	Name              string                    `json:"name"`
	Type              string                    `json:"typeCode"`
	Version           string                    `json:"version"`
	Description       string                    `json:"description"`
	Keywords          []string                  `json:"keywords"`
	Author            string                    `json:"orgName"`
	Downloads         int64                     `json:"downloads"`
	Status            string                    `json:"status"`
	AngularDetected   bool                      `json:"angularDetected"`
	GrafanaDependency string                    `json:"grafanaDependency"`
	Packages          map[string]catalogPackage `json:"packages"`
	SignatureType     string                    `json:"signatureType"`
	RepositoryURL     string                    `json:"url"`
	RepositoryLink    string                    `json:"repositoryUrl"`
	DocumentationURL  string                    `json:"documentationUrl"`
	LicenseURL        string                    `json:"licenseUrl"`
	IssuesURL         string                    `json:"raiseAnIssueUrl"`
	JSON              struct {
		Backend bool `json:"backend"`
		Info    struct {
			Author struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"info"`
		Dependencies struct {
			GrafanaDependency string `json:"grafanaDependency"`
			GrafanaVersion    string `json:"grafanaVersion"`
		} `json:"dependencies"`
	} `json:"json"`
}

type catalogRemoteVersion struct {
	Version           string                    `json:"version"`
	PluginSlug        string                    `json:"pluginSlug"`
	CreatedAt         string                    `json:"createdAt"`
	GrafanaDependency string                    `json:"grafanaDependency"`
	AngularDetected   bool                      `json:"angularDetected"`
	Packages          map[string]catalogPackage `json:"packages"`
}

type catalogCacheEntry struct {
	Raw     []byte
	Expires time.Time
}

// CatalogClient reads only the official Grafana host. The transport is injectable
// for tests; callers cannot select a catalog URL or forward workspace credentials.
type CatalogClient struct {
	client     *http.Client
	requests   chan struct{}
	mu         sync.Mutex
	cache      map[string]catalogCacheEntry
	cacheBytes int
	now        func() time.Time
}

func NewCatalogClient(transport http.RoundTripper) *CatalogClient {
	return &CatalogClient{
		client: &http.Client{
			Transport: transport,
			Timeout:   12 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if req.URL.Scheme != "https" || req.URL.Host != "grafana.com" || req.URL.User != nil || len(via) >= 3 {
					return errors.New("catalog redirects must stay on https://grafana.com")
				}
				return nil
			},
		},
		requests: make(chan struct{}, 4),
		cache:    make(map[string]catalogCacheEntry),
		now:      time.Now,
	}
}

func ValidateCatalogQuery(query, kind string) error {
	if len(query) > 256 || !utf8.ValidString(query) {
		return fmt.Errorf("%w: search must be UTF-8 and at most 256 bytes", ErrCatalogInput)
	}
	for _, r := range query {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: search cannot contain control characters", ErrCatalogInput)
		}
	}
	if kind != "" && kind != "panel" && kind != "datasource" && kind != "app" {
		return fmt.Errorf("%w: type must be panel, datasource or app", ErrCatalogInput)
	}
	return nil
}

func validateCatalogID(id string) error {
	if !model.PluginID.MatchString(id) {
		return fmt.Errorf("%w: valid plugin ID required", ErrCatalogInput)
	}
	return nil
}

func (c *CatalogClient) List(ctx context.Context, query, kind string) (CatalogList, error) {
	result := CatalogList{Items: []CatalogPlugin{}}
	if err := ValidateCatalogQuery(query, kind); err != nil {
		return result, err
	}
	words := strings.Fields(strings.ToLower(query))
	err := c.read(ctx, "/plugins?includeDeprecated=true", func(raw []byte) error {
		var remote struct {
			Items []catalogRemote `json:"items"`
		}
		if err := decodeCatalog(raw, &remote); err != nil {
			return err
		}
		if remote.Items == nil || len(remote.Items) > 2000 {
			return errors.New("Grafana catalog must contain at most 2000 items")
		}
		for _, item := range remote.Items {
			plugin, err := normalizeCatalogPlugin(item)
			if err != nil {
				return err
			}
			if kind != "" && plugin.Type != kind {
				continue
			}
			search := strings.ToLower(strings.Join([]string{plugin.ID, plugin.Name, plugin.Description, plugin.Author, strings.Join(item.Keywords, " ")}, " "))
			matches := true
			for _, word := range words {
				if !strings.Contains(search, word) {
					matches = false
					break
				}
			}
			if matches {
				result.Items = append(result.Items, plugin)
			}
		}
		sort.Slice(result.Items, func(i, j int) bool {
			a, b := strings.ToLower(result.Items[i].Name), strings.ToLower(result.Items[j].Name)
			if a == b {
				return result.Items[i].ID < result.Items[j].ID
			}
			return a < b
		})
		return nil
	})
	if err != nil {
		return CatalogList{Items: []CatalogPlugin{}}, err
	}
	return result, nil
}

func (c *CatalogClient) Detail(ctx context.Context, id string) (CatalogPluginDetail, error) {
	var result CatalogPluginDetail
	if err := validateCatalogID(id); err != nil {
		return result, err
	}
	err := c.read(ctx, "/plugins/"+id, func(raw []byte) error {
		var remote catalogRemote
		if err := decodeCatalog(raw, &remote); err != nil {
			return err
		}
		if remote.Slug != id {
			return errors.New("Grafana catalog returned a different plugin ID")
		}
		plugin, err := normalizeCatalogPlugin(remote)
		if err != nil {
			return err
		}
		result = CatalogPluginDetail{CatalogPlugin: plugin, Links: []CatalogLink{}, SignatureType: remote.SignatureType}
		for _, link := range []CatalogLink{
			{Name: "Grafana catalog", URL: plugin.URL},
			{Name: "Repository", URL: remote.RepositoryLink},
			{Name: "Repository", URL: remote.RepositoryURL},
			{Name: "Documentation", URL: remote.DocumentationURL},
			{Name: "License", URL: remote.LicenseURL},
			{Name: "Report an issue", URL: remote.IssuesURL},
		} {
			link.URL = safeCatalogLink(link.URL)
			if link.URL == "" {
				continue
			}
			duplicate := false
			for _, prior := range result.Links {
				if prior.URL == link.URL {
					duplicate = true
					break
				}
			}
			if !duplicate {
				result.Links = append(result.Links, link)
			}
		}
		return nil
	})
	return result, err
}

func (c *CatalogClient) Versions(ctx context.Context, id string) (CatalogVersions, error) {
	result := CatalogVersions{Items: []CatalogVersion{}}
	if err := validateCatalogID(id); err != nil {
		return result, err
	}
	// The detail distinguishes frontend-only universal archives from native ones.
	plugin, err := c.Detail(ctx, id)
	if err != nil {
		return result, err
	}
	err = c.read(ctx, "/plugins/"+id+"/versions", func(raw []byte) error {
		var remote struct {
			Items []catalogRemoteVersion `json:"items"`
		}
		if err := decodeCatalog(raw, &remote); err != nil {
			return err
		}
		if remote.Items == nil || len(remote.Items) > 1000 {
			return errors.New("Grafana catalog must contain at most 1000 versions")
		}
		seen := make(map[string]bool)
		for _, item := range remote.Items {
			if item.PluginSlug != "" && item.PluginSlug != id {
				return errors.New("Grafana catalog returned versions for a different plugin")
			}
			if _, err := semver.StrictNewVersion(item.Version); err != nil || seen[item.Version] {
				return errors.New("Grafana catalog returned invalid or duplicate versions")
			}
			seen[item.Version] = true
			_, native := item.Packages[runtime.GOOS+"-"+runtime.GOARCH]
			_, universal := item.Packages["any"]
			result.Items = append(result.Items, CatalogVersion{
				Version: item.Version, CreatedAt: item.CreatedAt, GrafanaDependency: item.GrafanaDependency,
				AngularDetected: item.AngularDetected, PlatformAvailable: native || (!plugin.Backend && universal),
			})
		}
		sort.Slice(result.Items, func(i, j int) bool {
			return semver.MustParse(result.Items[i].Version).GreaterThan(semver.MustParse(result.Items[j].Version))
		})
		return nil
	})
	if err != nil {
		return CatalogVersions{Items: []CatalogVersion{}}, err
	}
	return result, nil
}

func normalizeCatalogPlugin(remote catalogRemote) (CatalogPlugin, error) {
	if !model.PluginID.MatchString(remote.Slug) || remote.Name == "" || (remote.Type != "panel" && remote.Type != "datasource" && remote.Type != "app" && remote.Type != "renderer") {
		return CatalogPlugin{}, errors.New("Grafana catalog returned invalid plugin metadata")
	}
	backend := remote.JSON.Backend
	for name := range remote.Packages {
		if name != "any" {
			backend = true
		}
	}
	dependency := remote.GrafanaDependency
	if dependency == "" {
		dependency = remote.JSON.Dependencies.GrafanaDependency
	}
	if dependency == "" {
		dependency = remote.JSON.Dependencies.GrafanaVersion
	}
	author := remote.Author
	if author == "" {
		author = remote.JSON.Info.Author.Name
	}
	return CatalogPlugin{
		ID: remote.Slug, Name: remote.Name, Type: remote.Type, Version: remote.Version,
		Description: remote.Description, Author: author, Downloads: max(0, remote.Downloads),
		Deprecated: remote.Status == "deprecated", AngularDetected: remote.AngularDetected,
		GrafanaDependency: dependency, URL: "https://grafana.com/grafana/plugins/" + remote.Slug + "/", Backend: backend,
	}, nil
}

func safeCatalogLink(value string) string {
	if value == "" || len(value) > 4096 {
		return ""
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return ""
		}
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return ""
	}
	return u.String()
}

func decodeCatalog(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid Grafana catalog JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("Grafana catalog response must contain one JSON value")
	}
	return nil
}

func (c *CatalogClient) read(ctx context.Context, endpoint string, consume func([]byte) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	cached, present := c.cache[endpoint]
	if present && !c.now().Before(cached.Expires) {
		delete(c.cache, endpoint)
		c.cacheBytes -= len(cached.Raw)
		present = false
	}
	c.mu.Unlock()
	if present {
		return consume(cached.Raw)
	}
	select {
	case c.requests <- struct{}{}:
		defer func() { <-c.requests }()
	case <-ctx.Done():
		return ctx.Err()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, catalogOrigin+endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "MetricsPanel233 plugin catalog")
	request.Header.Set("grafana-version", RuntimeVersion)
	request.Header.Set("grafana-os", runtime.GOOS)
	request.Header.Set("grafana-arch", runtime.GOARCH)
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("read Grafana catalog: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return ErrCatalogNotFound
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Grafana catalog returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, catalogResponseMax+1))
	if err != nil {
		return err
	}
	if len(raw) > catalogResponseMax {
		return errors.New("Grafana catalog response exceeds 4 MiB")
	}
	if err = consume(raw); err != nil {
		return err
	}
	c.putCache(endpoint, raw)
	return nil
}

func (c *CatalogClient) putCache(endpoint string, raw []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for key, entry := range c.cache {
		if !now.Before(entry.Expires) || key == endpoint {
			delete(c.cache, key)
			c.cacheBytes -= len(entry.Raw)
		}
	}
	for len(c.cache) >= catalogCacheEntries || c.cacheBytes+len(raw) > catalogCacheMax {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range c.cache {
			if oldestKey == "" || entry.Expires.Before(oldest) {
				oldestKey, oldest = key, entry.Expires
			}
		}
		if oldestKey == "" {
			break
		}
		c.cacheBytes -= len(c.cache[oldestKey].Raw)
		delete(c.cache, oldestKey)
	}
	c.cache[endpoint] = catalogCacheEntry{Raw: raw, Expires: now.Add(catalogCacheTTL)}
	c.cacheBytes += len(raw)
}
