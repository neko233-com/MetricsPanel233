package grafana

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const CommunityBodyLimit = 4 * 1024 * 1024

var communityPagePath = regexp.MustCompile(`^/(?:grafana/)?dashboards/([1-9][0-9]*)(?:-[^/]*)?/?$`)
var communityDownloadPath = regexp.MustCompile(`^/api/dashboards/([1-9][0-9]*)/revisions/([1-9][0-9]*)/download/?$`)

// ParseCommunityReference accepts a catalog ID or an official Grafana.com URL.
// The supplied URL is parsed for identity; it is never used as a download target.
func ParseCommunityReference(reference string) (id int, revision int, err error) {
	reference = strings.TrimSpace(reference)
	if !strings.ContainsAny(reference, "/:.") {
		id, err = positiveCatalogNumber(reference)
		return
	}
	if strings.HasPrefix(reference, "grafana.com/") || strings.HasPrefix(reference, "www.grafana.com/") {
		reference = "https://" + reference
	}
	u, parseErr := url.Parse(reference)
	if parseErr != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || (u.Host != "grafana.com" && u.Host != "www.grafana.com") {
		return 0, 0, errors.New("use a Grafana.com dashboard ID or official HTTPS dashboard URL")
	}
	if match := communityPagePath.FindStringSubmatch(u.Path); match != nil {
		id, err = positiveCatalogNumber(match[1])
		return
	}
	if match := communityDownloadPath.FindStringSubmatch(u.Path); match != nil {
		id, err = positiveCatalogNumber(match[1])
		if err == nil {
			revision, err = positiveCatalogNumber(match[2])
		}
		return
	}
	return 0, 0, errors.New("URL does not identify a Grafana.com dashboard")
}

func positiveCatalogNumber(text string) (int, error) {
	if text == "" || strings.Trim(text, "0123456789") != "" {
		return 0, errors.New("dashboard ID and revision must be positive integers")
	}
	n, err := strconv.ParseInt(text, 10, 32)
	if err != nil || n <= 0 {
		return 0, errors.New("dashboard ID and revision must be positive 32-bit integers")
	}
	return int(n), nil
}

type CommunityDashboard struct {
	ID          int              `json:"id"`
	Revision    int              `json:"revision"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	URL         string           `json:"url"`
	Revisions   []int            `json:"revisions"`
	Dashboard   json.RawMessage  `json:"dashboard"`
	Inputs      []DashboardInput `json:"inputs"`
}

type CommunityHTTPError struct {
	Status int
}

func (e *CommunityHTTPError) Error() string {
	return fmt.Sprintf("Grafana.com returned HTTP %d", e.Status)
}

// CommunityClient always requests the official host. An injected HTTP client's
// transport allows deterministic tests without making the base URL configurable.
type CommunityClient struct {
	HTTPClient *http.Client
}

func (c *CommunityClient) Load(ctx context.Context, id, revision int) (CommunityDashboard, error) {
	if id <= 0 || id > 2147483647 || revision < 0 || revision > 2147483647 {
		return CommunityDashboard{}, errors.New("invalid community dashboard ID or revision")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	base := "https://grafana.com/api/dashboards/" + strconv.Itoa(id)
	var metadata struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Revision    int    `json:"revision"`
	}
	if err := c.getJSON(ctx, base, &metadata); err != nil {
		return CommunityDashboard{}, err
	}
	var history struct {
		Items []struct {
			Revision int `json:"revision"`
		} `json:"items"`
	}
	if err := c.getJSON(ctx, base+"/revisions", &history); err != nil {
		return CommunityDashboard{}, err
	}
	seen := map[int]bool{}
	for _, item := range history.Items {
		if item.Revision > 0 {
			seen[item.Revision] = true
		}
	}
	if metadata.Revision > 0 {
		seen[metadata.Revision] = true
	}
	revisions := make([]int, 0, len(seen))
	for value := range seen {
		revisions = append(revisions, value)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(revisions)))
	if len(revisions) == 0 {
		return CommunityDashboard{}, errors.New("community dashboard has no downloadable revisions")
	}
	if revision == 0 {
		revision = revisions[0]
	}
	if !seen[revision] {
		return CommunityDashboard{}, errors.New("community dashboard revision does not exist")
	}
	var document json.RawMessage
	if err := c.getJSON(ctx, base+"/revisions/"+strconv.Itoa(revision)+"/download", &document); err != nil {
		return CommunityDashboard{}, err
	}
	title, inputs, err := PreviewInputs(document)
	if err != nil {
		return CommunityDashboard{}, err
	}
	return CommunityDashboard{ID: id, Revision: revision, Title: title, Description: metadata.Description, URL: "https://grafana.com/grafana/dashboards/" + strconv.Itoa(id) + "/", Revisions: revisions, Dashboard: document, Inputs: inputs}, nil
}

func (c *CommunityClient) getJSON(ctx context.Context, address string, result any) error {
	client := http.Client{Timeout: 20 * time.Second}
	if c != nil && c.HTTPClient != nil {
		client = *c.HTTPClient
		if client.Timeout == 0 || client.Timeout > 20*time.Second {
			client.Timeout = 20 * time.Second
		}
	}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 || request.URL.Scheme != "https" || request.URL.Host != "grafana.com" || request.URL.User != nil {
			return errors.New("Grafana.com redirect target is not allowed")
		}
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "MetricsPanel233")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("load Grafana.com dashboard: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return &CommunityHTTPError{Status: response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, CommunityBodyLimit+1))
	if err != nil {
		return err
	}
	if len(body) > CommunityBodyLimit {
		return errors.New("community dashboard response exceeds 4 MiB")
	}
	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("invalid Grafana.com JSON: %w", err)
	}
	return nil
}
