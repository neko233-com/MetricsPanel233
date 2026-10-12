//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/instance"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/require"
)

// Evidence belongs to the parent test's host directory, so it survives the
// backend subtest cleanup and every restart of the one existing suite container.
type organizationIsolationEvidence struct {
	root      string
	timestamp int64
	keyHashes map[int64]string
}

func (e environment) verifyOrganizationIsolation(evidence *organizationIsolationEvidence, checkpoint string) *organizationIsolationEvidence {
	e.t.Helper()
	create := evidence == nil
	if create {
		evidence = &organizationIsolationEvidence{root: e.t.TempDir(), timestamp: time.Now().Add(-time.Minute).UnixMilli() / 1000 * 1000, keyHashes: map[int64]string{}}
	}
	port, err := e.compose("port", "clickhouse", "8123")
	require.NoError(e.t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	// Creating these dedicated databases with the existing fixture account is
	// itself the createDB permission check; no production privilege is widened.
	ch, err := store.OpenClickHouse(ctx, "http://"+strings.Split(port, "\n")[0], "metricspanel_org_fixture", "metricspanel", "integration-only-password", 7)
	require.NoError(e.t, err, "existing fixture ClickHouse user must support dedicated organization databases")
	control, err := store.Open(filepath.Join(evidence.root, "primary", "original.db"))
	require.NoError(e.t, err)
	control.Backend = ch
	primary := server.New(control, "organization-fixture-token", 7)
	catalog, err := identity.Open(filepath.Join(evidence.root, "identity.db"))
	require.NoError(e.t, err)
	registry, err := instance.New(instance.Config{PrimaryServer: primary, OrganizationLookup: catalog.Organization, OrganizationList: catalog.Organizations, OrganizationsRoot: filepath.Join(evidence.root, "organizations")})
	require.NoError(e.t, err)
	defer func() {
		// No worker runs between checkpoints. Close every host control store,
		// SDK/Live/SQL manager and idle HTTP connection before Docker restarts.
		require.NoError(e.t, registry.Close(context.Background()))
		primary.Live.Close()
		primary.Plugins.Close()
		primary.CloseSQLSources()
		require.NoError(e.t, primary.Store.DB.Close())
		require.NoError(e.t, catalog.Close())
		ch.Client.CloseIdleConnections()
	}()
	var org identity.Organization
	if create {
		org, err = catalog.BeginOrganization(ctx, "Fixture second organization")
		require.NoError(e.t, err)
		require.Equal(e.t, int64(2), org.ID)
		_, err = registry.Initialize(ctx, org.ID)
		require.NoError(e.t, err)
		org, err = catalog.ActivateOrganization(ctx, org.ID, org.Version)
		require.NoError(e.t, err)
	} else {
		org, err = catalog.Organization(ctx, 2)
		require.NoError(e.t, err)
		require.Equal(e.t, identity.Active, org.State)
	}
	secondary, err := registry.Resolve(ctx, org.ID)
	require.NoError(e.t, err)
	secondaryCH, ok := secondary.Store.Backend.(*store.ClickHouse)
	require.True(e.t, ok)
	require.Equal(e.t, "metricspanel_org_fixture_org_2", secondaryCH.Database)
	defer secondaryCH.Client.CloseIdleConnections()
	for _, app := range []*server.Server{primary, secondary} {
		id := app.OrganizationID()
		name := fmt.Sprintf("organization-fixture-%d", id)
		principal := identity.Principal{OrgID: id, ID: 233, Type: identity.TypeUser, Login: name, Name: name, Role: identity.RoleAdmin}
		local := identity.WithPrincipal(ctx, principal)
		if create {
			_, err := app.Store.CreateFolder(local, model.Folder{UID: "same-folder", Title: name})
			require.NoError(e.t, err)
			doc := json.RawMessage(fmt.Sprintf(`{"uid":"same-dashboard","title":%q,"opaque":900719925474099312345,"panels":[]}`, name))
			_, err = app.Store.SaveDashboard(local, model.Dashboard{ID: "same-dashboard", Name: name, FolderUID: "same-folder", Panels: []model.Panel{}, Grafana: doc})
			require.NoError(e.t, err)
			_, err = app.Store.SaveLibraryElement(local, model.LibraryElement{UID: "same-library", Name: name, Kind: 1, FolderUID: "same-folder", Model: []byte(`{"type":"stat","opaque":18446744073709551615}`)}, true)
			require.NoError(e.t, err)
			_, err = app.Store.SaveDataSource(local, model.DataSourceInput{DataSource: model.DataSource{UID: "same-source", Name: "Same source", Type: "mysql"}, SecureJSONData: map[string]string{"password": name}})
			require.NoError(e.t, err)
			_, err = app.Store.CreateAnnotation(local, model.Annotation{DashboardUID: "same-dashboard", Time: evidence.timestamp, Text: name, UserID: principal.ID, Login: name}, "same-idempotency-key")
			require.NoError(e.t, err)
			target, err := app.Store.SaveTarget(local, model.Target{Name: name, Kind: "prometheus", URL: "http://127.0.0.1:9099/prometheus", IntervalSeconds: 86400, Labels: map[string]string{}, SecureSettings: map[string]string{"bearer_token": name}})
			require.NoError(e.t, err)
			require.Equal(e.t, int64(1), target.ID)
			_, err = app.Store.SaveContactPoint(local, model.ContactPointInput{ContactPoint: model.ContactPoint{UID: "same-receiver", Name: name, URL: "http://127.0.0.1:17777"}, BearerToken: &name})
			require.NoError(e.t, err)
			_, err = app.Store.SaveAlertRule(local, model.AlertRule{UID: "same-rule", Title: name, Expr: fmt.Sprintf("vector(%d)", id), FolderUID: "same-folder", IntervalSeconds: 30})
			require.NoError(e.t, err)
			// Identical metric, labels and timestamp must resolve to each database's
			// acknowledged replacement, rather than collapsing across orgs.
			for _, value := range []float64{float64(id), float64(id * 101)} {
				require.NoError(e.t, app.Store.Ingest(local, []model.Sample{{Name: "organization_isolation_marker", Labels: map[string]string{"same": "labels"}, Timestamp: evidence.timestamp, Value: value}}))
			}
		}
		dashboard, err := app.Store.Dashboard(local, "same-dashboard")
		require.NoError(e.t, err)
		require.Equal(e.t, name, dashboard.Name)
		require.Contains(e.t, string(dashboard.Grafana), "900719925474099312345")
		folder, err := app.Store.Folder(local, "same-folder")
		require.NoError(e.t, err)
		require.Equal(e.t, name, folder.Title)
		library, err := app.Store.LibraryElement(local, "same-library")
		require.NoError(e.t, err)
		require.Equal(e.t, id, library.OrgID)
		require.Equal(e.t, name, library.Name)
		require.Contains(e.t, string(library.Model), "18446744073709551615")
		source, err := app.Store.DataSource(local, "same-source")
		require.NoError(e.t, err)
		require.Equal(e.t, id, source.OrgID)
		secrets, err := app.Store.DataSourceSecrets(local, source.UID)
		require.NoError(e.t, err)
		require.Equal(e.t, name, secrets["password"])
		history, err := app.Store.DashboardVersions(local, dashboard.ID, 10, 0, "")
		require.NoError(e.t, err)
		require.Len(e.t, history.Versions, 1)
		require.Equal(e.t, name, history.Versions[0].CreatedBy)
		annotations, err := app.Store.Annotations(local, model.AnnotationQuery{DashboardUID: "same-dashboard"})
		require.NoError(e.t, err)
		require.Len(e.t, annotations, 1)
		require.Equal(e.t, name, annotations[0].Text)
		targets, err := app.Store.Targets(local)
		require.NoError(e.t, err)
		require.Len(e.t, targets, 1)
		require.Equal(e.t, name, targets[0].Name)
		targetSecret, err := app.Store.TargetSecrets(local, 1)
		require.NoError(e.t, err)
		require.Equal(e.t, name, targetSecret["bearer_token"])
		point, err := app.Store.ContactPoint(local, "same-receiver")
		require.NoError(e.t, err)
		require.Equal(e.t, name, point.Name)
		pointSecret, err := app.Store.ContactPointSecrets(local, point.UID)
		require.NoError(e.t, err)
		require.Equal(e.t, name, pointSecret["bearer_token"])
		rule, err := app.Store.AlertRule(local, "same-rule")
		require.NoError(e.t, err)
		require.Equal(e.t, name, rule.Title)
		require.Equal(e.t, fmt.Sprintf("vector(%d)", id), rule.Expr)
		result, err := app.Store.Query(local, model.Query{Metric: "organization_isolation_marker", Labels: map[string]string{"same": "labels"}, Start: evidence.timestamp, End: evidence.timestamp + 1000, Step: 1000, Aggregation: "last"})
		require.NoError(e.t, err)
		require.Len(e.t, result.Series, 1)
		require.Len(e.t, result.Series[0].Points, 1)
		require.Equal(e.t, float64(id*101), result.Series[0].Points[0].Value)
		key, err := os.ReadFile(filepath.Join(filepath.Dir(app.Store.Path), "secrets.key"))
		require.NoError(e.t, err)
		hash := sha256.Sum256(key)
		fingerprint := hex.EncodeToString(hash[:])
		if create {
			evidence.keyHashes[id] = fingerprint
		} else {
			require.Equal(e.t, evidence.keyHashes[id], fingerprint, "organization key changed across restart")
		}
	}
	if create {
		// Exercise a ciphertext swap in real control-store rows, then restore the
		// legitimate org2 row before the subsequent restart checkpoints.
		var foreign, original []byte
		require.NoError(e.t, primary.Store.DB.QueryRow(`SELECT secrets FROM datasources WHERE uid='same-source'`).Scan(&foreign))
		require.NoError(e.t, secondary.Store.DB.QueryRow(`SELECT secrets FROM datasources WHERE uid='same-source'`).Scan(&original))
		_, err := secondary.Store.DB.Exec(`UPDATE datasources SET secrets=? WHERE uid='same-source'`, foreign)
		require.NoError(e.t, err)
		_, rejected := secondary.Store.DataSourceSecrets(ctx, "same-source")
		_, restoreErr := secondary.Store.DB.Exec(`UPDATE datasources SET secrets=? WHERE uid='same-source'`, original)
		require.NoError(e.t, restoreErr)
		require.Error(e.t, rejected, "copied org1 ciphertext must fail in org2")
	}
	require.NotEqual(e.t, evidence.keyHashes[1], evidence.keyHashes[2])
	e.t.Logf("organization isolation %s: physical org1/org2 stores, keys, raw dashboards, datasource/target/contact-point secrets, rules, annotation/history/library IDs and real ClickHouse same-series replacements are independent", checkpoint)
	return evidence
}
