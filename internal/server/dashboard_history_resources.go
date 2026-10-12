package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

const dashboardHistoryResourceBudget = 16 * 1024 * 1024

func dashboardHistoryAPIVersion(version string) bool {
	switch version {
	case "v0alpha1", "v1alpha1", "v1beta1", "v1", "v2alpha1", "v2beta1", "v2":
		return true
	}
	return false
}

// This implements Grafana's history selector, not general resource CRUD.
// UnifiedDashboardAPI reads both schema streams and filters conversion failures
// before merging generations. Keeping the original spec avoids lossy conversion.
func (s *Server) dashboardHistoryResourceRoutes(api routeRegistrar) {
	api.HandleFunc("GET /apis/dashboard.grafana.app/{version}/namespaces/{namespace}/dashboards", func(w http.ResponseWriter, r *http.Request) {
		version := r.PathValue("version")
		if !dashboardHistoryAPIVersion(version) || r.PathValue("namespace") != identity.Namespace(s.OrganizationID()) {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		for _, key := range []string{"labelSelector", "fieldSelector", "limit", "continue"} {
			if len(q[key]) > 1 {
				fail(w, 400, errors.New("history resource parameters must occur once"))
				return
			}
		}
		selector := q.Get("fieldSelector")
		uid := strings.TrimPrefix(selector, "metadata.name=")
		if q.Get("labelSelector") != "grafana.app/get-history=true" || selector == uid || !model.ResourceUID.MatchString(uid) {
			fail(w, 400, errors.New("history resources require grafana.app/get-history=true and metadata.name=<dashboard UID> selectors"))
			return
		}
		limit := 1000
		if raw := q.Get("limit"); raw != "" {
			var err error
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 0 || limit > 1000 {
				fail(w, 400, errors.New("history resource limit must be an integer from 0 to 1000"))
				return
			}
		}
		dashboard, found, err := s.grafanaDashboard(r.Context(), uid)
		if err != nil {
			writeGrafanaError(w, err)
			return
		}
		if !found {
			http.NotFound(w, r)
			return
		}
		page, err := s.Store.DashboardVersions(r.Context(), dashboard.ID, limit, 0, q.Get("continue"))
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		items := make([]json.RawMessage, 0, len(page.Versions))
		responseBytes := 2048 // Reserve space for the list envelope and bounded cursor.
		for _, metadata := range page.Versions {
			entry, err := s.Store.DashboardVersion(r.Context(), dashboard.ID, metadata.Version)
			if err != nil {
				dashboardResourceError(w, err)
				return
			}
			item, err := dashboardHistoryResource(entry, version, dashboard.UID(), s.OrganizationID())
			if err != nil {
				dashboardResourceError(w, err)
				return
			}
			encoded, err := json.Marshal(item)
			if err != nil {
				dashboardResourceError(w, err)
				return
			}
			responseBytes += len(encoded) + 1
			if responseBytes > dashboardHistoryResourceBudget {
				fail(w, http.StatusRequestEntityTooLarge, errors.New("historical dashboard resources exceed the 16 MiB response budget; reduce limit and use continuation pagination"))
				return
			}
			items = append(items, encoded)
		}
		writeJSON(w, 200, map[string]any{
			"apiVersion": "dashboard.grafana.app/" + version,
			"kind":       "DashboardList",
			"metadata":   map[string]string{"continue": page.ContinueToken},
			"items":      items,
		})
	})
}

func dashboardHistoryObject(raw json.RawMessage) (rawObject, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return rawObject{}, nil
	}
	var object rawObject
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, errors.New("historical dashboard resource must contain JSON objects")
	}
	return object, nil
}

func dashboardHistoryResource(entry model.DashboardVersion, requestedVersion, uid string, orgIDs ...int64) (rawObject, error) {
	document, err := dashboardHistoryObject(entry.Snapshot.Grafana)
	if err != nil {
		return nil, err
	}
	if wrapped, ok := document["dashboard"]; ok {
		document, err = dashboardHistoryObject(wrapped)
		if err != nil {
			return nil, err
		}
	}
	if len(document) == 0 {
		document, err = dashboardHistoryObject(rawValue(classicDocument(entry.Snapshot)))
		if err != nil {
			return nil, err
		}
	}
	resource := document
	spec := document
	storedVersion := "v0alpha1"
	if raw, ok := document["spec"]; ok {
		spec, err = dashboardHistoryObject(raw)
		if err != nil {
			return nil, err
		}
		var apiVersion string
		_ = json.Unmarshal(document["apiVersion"], &apiVersion)
		if strings.HasPrefix(apiVersion, "dashboard.grafana.app/") {
			storedVersion = strings.TrimPrefix(apiVersion, "dashboard.grafana.app/")
		} else if elements, ok := spec["elements"]; ok && string(elements) != "null" {
			storedVersion = "v2beta1"
		} else {
			storedVersion = "v1beta1"
		}
	} else {
		resource = rawObject{"spec": rawValue(document)}
	}
	metadata, err := dashboardHistoryObject(resource["metadata"])
	if err != nil {
		return nil, err
	}
	annotations, err := dashboardHistoryObject(metadata["annotations"])
	if err != nil {
		return nil, err
	}
	annotations["grafana.app/updatedBy"] = rawValue(entry.CreatedBy)
	annotations["grafana.app/updatedTimestamp"] = rawValue(entry.Created.UTC().Format(time.RFC3339Nano))
	annotations["grafana.app/message"] = rawValue(entry.Message)
	annotations["grafana.app/folder"] = rawValue(entry.Snapshot.FolderUID)
	annotations["metricspanel.app/parent-version"] = rawValue(strconv.FormatInt(entry.ParentVersion, 10))
	annotations["metricspanel.app/restored-from"] = rawValue(strconv.FormatInt(entry.RestoredFrom, 10))
	if _, ok := annotations["grafana.app/createdBy"]; !ok {
		annotations["grafana.app/createdBy"] = rawValue(entry.CreatedBy)
	}
	metadata["name"] = rawValue(uid)
	orgID := int64(1)
	if len(orgIDs) > 0 {
		orgID = orgIDs[0]
	}
	metadata["namespace"] = rawValue(identity.Namespace(orgID))
	metadata["generation"] = rawValue(entry.Version)
	metadata["resourceVersion"] = rawValue(strconv.FormatInt(entry.ID, 10))
	if _, ok := metadata["creationTimestamp"]; !ok {
		metadata["creationTimestamp"] = rawValue(entry.Created.UTC().Format(time.RFC3339Nano))
	}
	metadata["annotations"] = rawValue(annotations)
	labels, err := dashboardHistoryObject(metadata["labels"])
	if err != nil {
		return nil, err
	}
	labels["grafana.app/deprecatedInternalID"] = rawValue(strconv.FormatInt(entry.DashboardID, 10))
	metadata["labels"] = rawValue(labels)
	resource["metadata"] = rawValue(metadata)
	resource["kind"] = rawValue("Dashboard")
	// Unlike a full schema converter, this history endpoint preserves the stored
	// apiVersion as well as spec. Cross-family entries are explicitly marked so
	// Grafana's unified client takes their matching stream without data loss.
	resource["apiVersion"] = rawValue("dashboard.grafana.app/" + storedVersion)
	status, err := dashboardHistoryObject(resource["status"])
	if err != nil {
		return nil, err
	}
	storedV2 := strings.HasPrefix(storedVersion, "v2")
	if elements, ok := spec["elements"]; ok && string(elements) != "null" {
		storedV2 = true
	}
	failed := storedV2 != strings.HasPrefix(requestedVersion, "v2")
	conversion := map[string]any{"failed": failed, "storedVersion": storedVersion}
	if failed {
		conversion["error"] = "This historical dashboard retains its original schema; read the matching V1 or V2 history stream"
	}
	status["conversion"] = rawValue(conversion)
	resource["status"] = rawValue(status)
	return resource, nil
}
