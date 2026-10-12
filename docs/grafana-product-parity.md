# Grafana product acceptance checklist

Confirmed acceptance scope: all Grafana 13.2.3 OSS functionality, matching the pinned frontend SDK. Windows PC browsers are the supported client. Enterprise-only functionality is excluded from this scope.

Full replacement is not complete. This checklist describes implemented behavior and remaining work; it is not a compatibility certificate. The [datasource and visualization matrix](grafana-compatibility.md) lists the tagged core plugins individually.

| Area | Current implementation | Remaining acceptance work |
| --- | --- | --- |
| Collection and storage | Native database/big-data collectors, compatible exporter input, SQLite and ClickHouse persistence, upstream PromQL engine | Native histogram/exemplar storage; cluster discovery; actual big-data cluster verification beyond protocol fixtures |
| Dashboards | Custom panels, 24-column editing, native/Classic/V1/V2 import, community bindings, folders, library panels, version history | All Grafana layout modes and editors; full App Platform resource CRUD and migrations |
| Variables | Six common types, dependency loading, distinct labels/values, multi/All, URL restore, preview, ordered atomic edits | Full ad hoc and group-by editing/semantics; automatic reference updates on rename |
| Panel queries | Prometheus, SQL, expression graphs, installed SDK editors, cancellable previews, raw model preservation | Every core datasource/editor and all public core service contracts |
| Visualizations | Core table SDK, time series implementation, stat/gauge/bar-gauge/text renderers, installed panel plugins | See individual core renderer and option gaps in the visualization matrix; rendered verification for newly integrated components |
| Explore | PromQL and native/SDK query editors, tables, charts, raw frame export | Complete logs, traces, profiles and cross-signal workflows |
| Annotations | Durable events, tag/dashboard filters, alert transitions, time regions, SDK editors, atomic query edits | Every core datasource's annotation behavior and official editor interactions |
| Snapshots and sharing | Browser capture freezes current variables, queries and repeated panels into independent durable dashboards | Grafana snapshot HTTP routes, anonymous links, expiry, external dashboards and rendering/export workflows |
| Alert evaluation | Scheduled per-instance graphs, rule groups, previews, recovery periods, histories, recordings | Full rule resource APIs, file provisioning and permission enforcement |
| Alert notification | Encrypted webhook contacts, recursive routes, grouping/repeats, retries, silences, timezone calendar intervals, named notification templates, and indexed source/target inhibition with desktop preview and versioned CRUD | Full Grafana template functions/defaults and graph values, other native receiver integrations, imported external Alertmanager scoping and remaining Alertmanager APIs |
| Identity and access | Existing deployments retain the shared workspace token. Internal organization isolation, human/account/session/team storage, immutable user UIDs, session rotation/device metadata, membership generation fences, resource ACL storage/evaluation and an opt-in route authorization boundary are implemented; multi-user HTTP administration is not exposed | Login and administration HTTP/PC/CLI flows, team administration, complete action policies and filtered resource access, Live credential revocation and shared plugin installation lifecycle |
| Preferences and navigation | Chinese/English UI, dark theme, folders and plugin application navigation | User/organization preferences, theme switching, starred dashboards and playlists |
| Plugins and provisioning | Signed original package install, official catalog/community imports, public frontend SDK, backend gRPC, live/chunked queries | Individually validate remaining core/plugin APIs; file provisioning and complete platform resource APIs |

Organizations isolate Grafana resources; dashboard/folder permissions and service account roles are part of the acceptance scope. Sources: [organization management](https://grafana.com/docs/grafana/latest/administration/organization-management/), [dashboard permissions](https://grafana.com/docs/grafana/latest/administration/user-management/manage-dashboard-permissions/) and [service accounts](https://grafana.com/docs/grafana/latest/administration/service-accounts/).

The shared token and internal storage primitives do not establish organization or permission parity. Preserve the existing single-workspace deployment while adding authenticated dispatch and resource authorization; tests must verify denied operations and cross-workspace access, not just successful writes. See the [pinned identity HTTP contracts](grafana-identity-http-contract.md) for native storage differences and remaining adapters.

The standalone SMTP/Slack transports and opt-in encrypted receiver store are internal foundations. They are not yet available as product contact-point types. The legacy webhook worker remains the sole owner of its existing queue until configuration/template migration, per-integration dispatch, routing, API/CLI and desktop editing are connected and tested together. The new store records immutable integration/SMTP-profile/prepared revisions and per-operation acknowledgements; activating it prematurely would leave later legacy contact edits out of sync.

Inhibition applies to this service's own active alert instances. Source labels are indexed after every successful evaluation, even without a state transition. Missing and empty equal labels are equivalent; alerts matching both sides cannot inhibit themselves or another alert matching both sides. Source removal, reset, rule deletion or disable resumes only still-active targets. Suppression is checked again after rendering and before HTTP delivery; an already-started network request cannot be recalled. Delivery remains at least once. Completed notification history is bounded to 10,000 rows plus current ungrouped successful firing anchors needed for later resume.

## Verification before publication

Each supported item needs a working desktop operation or HTTP/CLI contract, persistence checks where applicable, and evidence against the pinned upstream behavior. Imported JSON retention alone does not establish rendering or query compatibility.

Run frontend typecheck/build and unit tests, Go tests/vet/race, actual Windows PC browser workflows, and the single-container integration suite. The container runs the real database services together and is restarted to verify durable state. Protocol fixtures remain identified as fixtures.

A release claiming all Grafana OSS features requires every remaining item above and every core plugin in the linked matrix to pass acceptance. Until then, release descriptions must state the tested scope and open gaps.
