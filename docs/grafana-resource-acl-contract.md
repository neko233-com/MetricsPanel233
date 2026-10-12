# Grafana OSS resource authorization contract

Status: **source audit, pure ACL storage foundation and remaining integration design**, 2026-10-12. Target: **Grafana v13.2.3 OSS**, without Enterprise custom roles, Enterprise datasource permissions, SAML or SCIM. This document does not claim that MetricsPanel233 currently enforces resource ACLs through HTTP. It supplements [identity isolation](grafana-identity-design.md) and [the identity HTTP contract](grafana-identity-http-contract.md).

`Verified` below means the cited tagged source contains the behavior. `Proposed` means a MetricsPanel233 implementation requirement or extension. `Unresolved` identifies a source/documentation discrepancy or a path that still needs a version-specific integration test. Links pin the tag; `latest` documentation is not the contract.

The organization foundation already separates stores, encryption keys/AAD, metric databases, collectors, alert workers, plugin managers, SQL pools and Live hubs. The single-container integration test has now passed organization isolation at creation, ClickHouse service restart and whole-container restart. **Authentication dispatch and resource authorization remain separate acceptance gates.** The legacy global Viewer gate stays restrictive when no authorizer is configured. The new opt-in route boundary permits authenticated None identities to reach an explicit grant decision and wraps all 204 current protected registrations; its default action policy, credential dispatcher and list/resource filtering are not activated. The catalog ACL resolver requires either actual request credential revalidation or an explicitly configured canonical internal principal, and reloads live membership/account/team state instead of trusting an input role.

## 1. Permission levels, subjects and inheritance

Verified numeric values are **View=1, Edit=2, Admin=4**. They are levels/action sets, not an arbitrary bitmask: `3`, `5`, `6`, `7` do not name valid permissions. Edit includes View; Admin includes Edit. [Numeric definition](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/dashboards/dashboardaccess/dashboard_access.go).

| Grant | Dashboard actions | Folder actions, including its resources |
| --- | --- | --- |
| View | Dashboard read; annotation read on that dashboard | Dashboard View plus folder, rule, rule-silence and library-panel read |
| Edit | View plus dashboard write/delete and annotation create/write/delete | Dashboard Edit plus folder write/delete, dashboard create, rule create/write/delete, rule-silence create/write and library-panel create/write/delete |
| Admin | Edit plus dashboard permission read/write | Dashboard Admin plus Folder Edit and folder permission read/write |

The precise action names are `dashboards:{read,write,delete}`, `dashboards.permissions:{read,write}`, `annotations:{read,create,write,delete}`, `folders:{read,write,delete}`, `dashboards:create`, `alert.rules:{read,create,write,delete}`, `alert.silences:{read,create,write}`, `library.panels:{read,create,write,delete}` and `folders.permissions:{read,write}`. The View/Edit/Admin expansions must be shared by mutation, evaluation and permission DTOs. [Dashboard registration](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/accesscontrol/ossaccesscontrol/dashboard.go#L30), [folder registration](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/accesscontrol/ossaccesscontrol/folder.go#L27).

Dashboard/folder permission services accept users, teams, service accounts and built-in roles. Human and service-account identifiers must remain typed in our database even though Grafana's legacy ACL represents both through `userId`. A resource grant never creates membership in an organization. Subject existence is validated in the target organization. [Subject validation](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/accesscontrol/resourcepermissions/service.go#L344).

Effective authorization is the **union of matching actions/scopes**, including all ancestor folders. There is no negative deny entry. A child View grant cannot revoke inherited Edit/Admin. A missing parent, cycle or wrong organization must fail resolution, not silently fall back to root. Dashboard scopes resolve to the dashboard UID, its containing folder and all ancestors; root dashboards additionally resolve to the `general` folder sentinel. Library and annotation scopes resolve their actual stored ownership similarly. [Evaluator](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/accesscontrol/evaluator.go), [dashboard scope resolver](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/dashboards/accesscontrol.go#L97), [folder scope resolver](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/folder/access_control.go#L80).

**Important role distinction:** directly managed built-in-role grants match the current organization role **exactly**. Grafana stores that role in `builtin_role`; `GetOrgRoles` returns the current role, with a separate Grafana-admin role when applicable. Fixed-role registrations independently expand their grants through the role hierarchy. Do not apply `Role.AtLeast(Viewer)` to every ACL Role subject. [Managed-role insertion](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/accesscontrol/resourcepermissions/store.go#L633), [request role selection](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/accesscontrol/accesscontrol.go#L408), [fixed-role hierarchy](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/accesscontrol/models.go#L590).

## 2. Defaults that ACLs can and cannot narrow

| Identity/default | Contract |
| --- | --- |
| Viewer | Root resources ordinarily receive a removable Viewer View grant. A private folder/dashboard need not grant Viewer access. Explicit user/team/SA grants can increase access. |
| Editor | Root resources ordinarily receive a removable Editor Edit grant. An explicit user View does not reduce an existing Editor Edit grant; remove the matching broader grant to narrow access. |
| Org Admin | Fixed dashboard/folder reader/writer and permission-manager registrations retain organization-wide administration. Removing stored ACL rows cannot revoke this baseline. |
| None | Has no ordinary basic-role resource rights, but remains a valid authenticated identity and can receive explicit resource grants. It must not be rejected before resource authorization. |
| Grafana/server Admin | Global administration is distinct from the selected organization's role. Do not turn a service account or a server-admin flag into an unrestricted cross-org query principal. |

The documented highest-permission rule agrees with the positive-grant evaluator. [Pinned permission examples](https://github.com/grafana/grafana/blob/v13.2.3/docs/sources/administration/user-management/manage-dashboard-permissions/index.md), [basic/organization role distinction](https://github.com/grafana/grafana/blob/v13.2.3/docs/sources/administration/roles-and-permissions/_index.md).

Verified configuration defaults enable managed grants on creation for dashboard, folder, service-account and datasource, while **wildcard resource seeding defaults to an empty list**. Optional dashboard/folder Viewer/Edit wildcard registrations would prevent ordinary ACL narrowing while enabled. This does not remove the separate Org Admin baseline. [Settings defaults](https://github.com/grafana/grafana/blob/v13.2.3/pkg/setting/settings_rbac.go), [Org Admin and root creator registrations](https://github.com/grafana/grafana/blob/v13.2.3/pkg/api/accesscontrol.go#L320).

### Creation paths are not interchangeable

| Tagged path | Default grants |
| --- | --- |
| Legacy `SetDefaultPermissions`, enabled by configuration | Human/SA creator gets Admin for non-provisioned creation, including nested resources. Root gets Editor Edit and Viewer View. Provisioned creation skips creator Admin. |
| App Platform `SetDefaultPermissionsAfterCreate` | Nested resources return early and inherit. Root gets human/SA creator Admin plus Editor Edit and Viewer View. |
| Unified folder create | Requests default-grant annotation only for a root folder. Its default setter includes a valid human/SA creator and skips nested resources. |

Sources: [legacy versus App Platform setters](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/dashboards/service/dashboard_service.go#L1213), [unified folder request](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/folder/folderimpl/unifiedstore.go#L55), [folder default setter](https://github.com/grafana/grafana/blob/v13.2.3/pkg/registry/apis/folders/register.go#L332).

**Proposed default policy:** new native and App Platform resources use root-only defaults and nested inheritance; a compatibility handler must explicitly select legacy behavior when reproducing it. Existing organization-1 resources are not retroactively private: a resumable migration seeds their known legacy defaults once, records completion and never resets a subsequently edited ACL. Native creation must commit resource, creator/default ACL, revision and history in one transaction. Detached ACL seeding after a successful HTTP response is insufficient.

**Unresolved discrepancy:** pinned folder documentation includes `folders:create` in Folder Edit, but `FolderEditActions` does not. The action-set service registers the same source expansion. The folder API delegates user checks downstream to unified storage, while folder move explicitly accepts destination write **or** create. Thus a documentation table alone does not prove that adding `folders:create` is equivalent for every path. Implement the verified action expansion first; test nested creation with a None identity holding only Folder Edit before claiming equivalence. [Pinned folder documentation](https://github.com/grafana/grafana/blob/v13.2.3/docs/sources/administration/roles-and-permissions/folder-access-control/index.md), [action-set initialization](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/accesscontrol/resourcepermissions/service.go#L85), [downstream authorization boundary](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/apiserver/auth/authorizer/service.go).

## 3. Permission HTTP mutations and administrator safety

### Verified legacy/generic contracts

| Endpoint | Method/body | Behavior |
| --- | --- | --- |
| `/api/dashboards/uid/:uid/permissions` | GET; **POST** `{items:[{userId?,teamId?,role?,permission:1|2|4}]}` | Permission read/write action on that resource. POST replaces its managed assignments; omitted direct assignments are removed. Hidden-user grants are preserved by the legacy adapter. |
| `/api/folders/:uid/permissions` | GET; **POST** same `items` shape | Same numeric mapping/replacement; provisioned-folder mutation can return 403. |
| `/api/access-control/{resource}/:resourceID` | GET; **POST** `{permissions:[{userId?,teamId?,builtinRole?,permission:"View"|"Edit"|"Admin"|""}]}` | Bulk subject updates. Empty permission removes that subject; omission alone does **not** mean replacement. |
| Generic subject endpoint | POST `.../users/:userID`, `.../teams/:teamID`, `.../builtInRoles/:builtInRole` with `{permission}` | Update only that assignment. Validate resource/subject in the current organization. |

There is **no legacy PUT permissions replacement** in these registrations. Legacy success is 200 with a message. Binding/mutually exclusive subject failures are 400; route authorization is 403; missing resources are 404; the legacy adapter can wrap downstream permission validation failures in 500. It has no client version/If-Match contract and must not promise a 409 CAS response. [Dashboard handler](https://github.com/grafana/grafana/blob/v13.2.3/pkg/api/dashboard_permission.go#L82), [folder handler](https://github.com/grafana/grafana/blob/v13.2.3/pkg/api/folder_permission.go#L74).

The generic API uses typed bad-request errors for invalid permission, assignment, parameter, body or wildcard resource ID. Its OSS response also appends a virtual immutable Org Admin entry when built-in-role assignments are supported and Enterprise enforcement is absent. Inherited grants are returned with source scope metadata rather than edited as child-owned rows. [Generic registration/DTO](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/accesscontrol/resourcepermissions/api.go#L98), [typed errors](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/accesscontrol/resourcepermissions/error.go), [managed/inherited storage classification](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/accesscontrol/resourcepermissions/store.go#L51).

When optional Kubernetes redirects are enabled, the adapter reads the existing permission resourceVersion and uses Kubernetes Update/Create; that is **internal optimistic concurrency**, not a legacy client's If-Match guarantee. Do not conflate this with dashboard content version checks. [Redirect adapter](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/accesscontrol/resourcepermissions/api_adapter.go#L413).

### Proposed native contract and last manager rule

Use a separate `/api/v1/{folders|dashboards}/{uid}/permissions` GET/PUT API. GET returns `{version,items,effective,inherited}`. PUT requires `If-Match` for the ACL version, replaces **direct writable** items and returns the new representation. Invalid subjects/levels are 400, missing resource 404, denied action 403, stale revision 409 and missing condition 428, matching our existing native mutation conventions. These native status choices are extensions, not Grafana legacy facts.

The evaluator always retains Org Admin's resource-management baseline. A client cannot delete the virtual Admin anchor by omitting it. There is no verified last-direct-resource-admin guard in the inspected legacy permission setter. Do not manufacture one as a Grafana compatibility claim. The actual last active organization administrator constraint belongs to organization membership management. [Grafana organization error](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/org/model.go), [current identity invariant](../internal/identity/accounts.go).

Native safety rule: before a mutation could remove the last **effective** manager, count active eligible human Org Admins plus valid direct/inherited human/team Admin grants using fresh identity state. Service-account Admin alone is not a recovery administrator. In ordinary active organizations the immutable Org Admin baseline satisfies this; if catalog corruption or an unactivated organization leaves none, reject and require trusted recovery. Do not reject removal of the last direct Admin when an active Org Admin still exists. No ACL creation grants server-admin, org-role changes or service-account-management rights.

## 4. Read, history, export, snapshots and search

| Operation | Verified distinction |
| --- | --- |
| Read dashboard JSON | `dashboards:read` on UID, including inherited scopes |
| Delete dashboard | `dashboards:delete` on UID |
| Version list/detail and restore | Legacy routes require **dashboard write**, even for GET history; ordinary View is insufficient |
| Dashboard create/save | Coarse route precheck is followed by content/destination checks; create and update must not share only an org-role check |
| Dashboard tags/search | Service filtering determines visible hits; absence of a per-UID route check does not grant all-resource access |

Sources: [legacy registrations](https://github.com/grafana/grafana/blob/v13.2.3/pkg/api/api.go#L464), [filtered-query helper](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/accesscontrol/filter.go), [search service](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/dashboards/service/search/search.go).

**Proposed:** native current-content export requires View; historical export/diff retains the history Write boundary. Filter before pagination/counts/tag aggregation. Dashboard count, title, UID, folder paths, tags, raw query text and historical snapshots all count as protected metadata. A direct dashboard grant need not expose every sibling or all ancestor contents; return only navigational ancestor metadata the policy permits. Restore authorizes the current resource and proposed destination, preserves current ACL identity and creates history atomically. Dashboard JSON import/export must not carry ACL changes implicitly; a separate authorized permissions operation is required.

### Snapshot behavior

Verified authenticated snapshot creation requires `snapshots:create` **and source-dashboard read**. Public external snapshot-hosting compatibility is a separately configured bypass. Key-based snapshot GET is intentionally public; a delete key is a separate secret capability. Authenticated DELETE additionally checks organization and source write, with creator/missing-source exceptions. A snapshot is deliberately detached public data, not a live panel read. [Legacy snapshot handlers](https://github.com/grafana/grafana/blob/v13.2.3/pkg/api/dashboard_snapshot.go#L89).

Snapshot listing is organization-wide for Org Admin and creator-only for other human/SA identities; it does not merely list every currently readable dashboard's snapshots. [Snapshot list store](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/dashboardsnapshots/database/database.go#L115). Optional Kubernetes snapshots allow anonymous object/dashboard GET and otherwise map snapshot verbs to snapshot actions; disabled feature routes deny. [Snapshot authorizer](https://github.com/grafana/grafana/blob/v13.2.3/pkg/registry/apis/dashboard/snapshot/authorizer.go).

Our existing native frozen-data panel JSON is a dashboard resource, not a Grafana public snapshot service. It must retain dashboard ACL protection until a separately tested public snapshot API exists. Public snapshot creation/export must freeze values after authorized queries; never publish source credentials, permission rows or a live backend query capability.

## 5. Moves, deletion and library panels

| Resource operation | Required authorization and transactional behavior |
| --- | --- |
| Dashboard move | Source dashboard Write plus dashboard Create on destination folder. Resolve source permissions against **stored old folder**, then destination against proposed new folder; never accept a body-supplied folder as the source ownership. |
| Folder move | Source folder Write; destination Write or Create for a nonroot destination, Create on General for root. Check cycles/depth and destination ancestry. |
| Folder move escalation | For each action granted through destination/ancestors, require that action on the source folder. This includes plugin folder actions, not just numeric View/Edit/Admin. |
| Library move | Source library Write plus library Create on destination. An element's own write grant cannot relocate it into an inaccessible folder. |
| Library deletion | Library Delete and connection constraints; a referenced library cannot be silently deleted or mutate unrelated dashboards. |

Verified move checks: [dashboard save validation](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/dashboards/service/dashboard_service.go#L733), [folder destination/escalation](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/folder/folderimpl/folder_unifiedstorage.go#L645), [library patch destination](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/libraryelements/database.go#L607).

Library permissions use the library UID and its own folder ancestry, not the consuming dashboard's folder. Editing shared library content can affect connected dashboards; require library Write rather than pretending every dashboard must be edited. Inserting/unlinking in our native dashboard editor instead requires dashboard Write and library Read where content is fetched. [Library scope resolution](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/libraryelements/accesscontrol.go#L39).

Tagged library connection listing filters connected nonroot dashboards by folder read, with Org Admin/root exceptions. It does not explicitly evaluate every individual dashboard's ACL. Our proposed stricter connection-metadata filter is per-dashboard View; record that difference if adopted. General-folder library helpers also have legacy Editor/Viewer special cases. [Connection handler](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/libraryelements/api.go#L288), [General helper](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/libraryelements/guard.go#L31).

Proposed delete preflight must enumerate the entire affected subtree and connected resources before mutation. Do not remove folders first and then discover an unauthorized descendant. Preserve provisioned restrictions. Resource deletion, ACL deletion, library connections and content/history updates share a transaction in the organization store; identity catalog state is not committed through that SQLite transaction. A folder move preserves direct grants and dynamically changes inherited grants; invalidate all affected authorization/search/Live caches after commit.

## 6. Annotations and annotation tag discovery

Verified annotation list authorization separates organization annotations from readable dashboard annotations; by-ID lookup first resolves the stored annotation to its organization/dashboard ownership. Write/delete resolve the same owner. Dashboard/panel IDs supplied in an update cannot relocate an annotation without checking both old and new scopes. Readable dashboard annotations are selected through the permission-aware search service. [Annotation authorizer](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/annotations/accesscontrol/accesscontrol.go#L63), [ownership resolver/create/mass-delete](https://github.com/grafana/grafana/blob/v13.2.3/pkg/api/annotations.go#L514).

Organization annotations have separate type-scoped default grants; dashboard annotation rights come from resource grants. Graphite creates organization annotations and cannot bypass the type check. Mass deletion must authorize the selected owner before deleting any rows. SQL/external datasource annotations are query results and additionally require datasource Query; mapping them to a chart does not grant local annotation mutation rights.

**Verified tag discrepancy:** the legacy tag handler passes org/tag/limit, and the XORM tag query aggregates within the organization without a dashboard-ACL predicate. This differs from annotation item filtering; do not describe the pinned tag path as filtering private-dashboard tags. Proposed MetricsPanel233 behavior aggregates only authorized dashboard and organization annotations, so tag counts do not reveal hidden resources. This is an intentional stricter behavior requiring an explicit compatibility note/test. [Tag query](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/annotations/annotationsimpl/xorm_store.go#L579), [repository delegation](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/annotations/annotationsimpl/annotations.go#L141).

## 7. Alert rules, silences and notification resources

Folder rule access is an intersection: folder Read plus rule Read. Rules' real datasource dependencies are checked separately; expression pseudo-sources are skipped while actual source UIDs are evaluated. Group replacement checks create/write/delete for each affected rule, not just group read. Rule history and export retain rule/folder visibility; provisioning status/secrets use additional organization actions. [Rule authorizer](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/ngalert/accesscontrol/rules.go#L39), [route evaluators](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/ngalert/api/authorization.go#L24), [provisioning services](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/ngalert/api/api_provisioning.go#L343).

Silence route permission alone is not sufficient:

| Silence | Verified service-level behavior |
| --- | --- |
| General, without exact rule matcher | An identity with some silence-read permission may read it. Create/update require broader `alert.instances:create`/`alert.instances:write` plus read prerequisites. |
| Rule-specific | Exact equality on the rule UID matcher resolves the rule's real folder; folder-scoped silence read/create/write applies. Unknown rule ownership denies unless the caller already has the corresponding all-silence capability. |
| List | Filter per silence; requesting access-control metadata does not grant access. |
| Update/expire | Authorize the stored/current scope. A rule UID matcher cannot be changed or added/removed by update; create a new silence instead. |

The specialized evaluator explicitly distinguishes general from rule-specific silences. Route read uses instance-read OR silence-read; mutation combines read with the corresponding instance/silence action. [Silence evaluator](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/ngalert/accesscontrol/silences.go), [exact matcher detection](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/ngalert/models/silence.go#L16), [immutable matcher/update service](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/ngalert/notifier/silence_svc.go#L265).

Folder Admin does not implicitly administer all contact points, policy trees, templates, mute timings or inhibition rules. These use organization/resource-specific notification actions. Ordinary Viewer/Editor defaults remain through their separate alerting registrations even when folder ACLs narrow rule visibility. In particular, folder ACL removal alone is not a revocation of all-silence instance privileges. [Alerting default registrations](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/ngalert/accesscontrol/roles.go).

Proposed native alert preview/manual evaluation checks each real query dependency under the caller. Scheduled evaluation uses a trusted organization System principal, distinct from an interactive preview. Its internal authorization cannot be obtained by sending a user header. Persisted runtime/history/notification views filter by authorized rule ownership; a native folderless rule requires an explicit General-scope policy. Retry/test/send and decrypted-secret export require notification mutation/secrets actions, not dashboard View. Inhibition preview/history must not expose hidden source/target rule metadata through diagnostic rows.

## 8. Delegated service-account management

Managing a service account and acting as that service account are independent authorizations. Tagged delegated management grants support **Edit** (account read/write) and **Admin** (plus delete and permission read/write), assigned to humans/teams; this service does not enable built-in-role or SA assignments as permission subjects. Ordinary Org Admin has a separate account-management baseline. [Delegated permission service](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/accesscontrol/ossaccesscontrol/service_account.go), [default manager roles](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/serviceaccounts/manager/roles.go).

Account token list needs account Read; token create/revoke needs Write. Changing the account's basic role may not exceed the caller's current organization role. Creator Admin on the managed account does not make the account's tokens server-admin or grant them the creator's folder permissions. [Account/token routes and role ceiling](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/serviceaccounts/api/api.go#L57), [token handlers](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/serviceaccounts/api/token.go).

Proposed: keep management grants separate from `resource_acl` folder/dashboard grants, because the account entity belongs to the global identity database. Management reads use dynamic same-org human/team membership and account state. Token secrets remain one-time responses; metadata access never returns old tokens. Cross-store account deletion must revoke authentication first, then remove account-owned grants/references from the organization store before marking cleanup complete.

## 9. Query authorization is not dashboard authorization

Grafana OSS ordinarily permits Viewer/Editor/Admin to query organization datasources; restricting dashboards does not make the underlying datasource or SQL rows private. Enterprise per-datasource permission services are not the OSS substitute. The OSS datasource permission setter only preserves built-in Query grants for upgrade compatibility; its per-user/team/role management methods are no-ops. [OSS datasource service](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/accesscontrol/ossaccesscontrol/datasource.go), [pinned OSS/Enterprise distinction](https://github.com/grafana/grafana/blob/v13.2.3/docs/sources/administration/roles-and-permissions/_index.md).

Proposed enforcement pipeline:

1. Authenticate, reload current principal/membership, and dispatch to its registered physical organization instance.
2. Resolve the real datasource UID/type/config within that store; check Query before obtaining decrypted connection settings, a SQL pool or SDK PluginContext.
3. For a dashboard-scoped operation, independently check dashboard View/Write as appropriate. A client-provided `dashboardUID` is context, never an extra grant. Standalone Explore/query does not require access to an unrelated dashboard.
4. For expressions, mixed queries, query variables, annotations and comparison branches, walk **all real dependencies** and preauthorize the entire graph before executing any query. Neither `__expr__` nor `-100` grants its inputs. Cancellation and query errors must not expose credentials.
5. Check resource/proxy/health and chunked endpoints through the same datasource policy. Arbitrary plugin resource HTTP methods need endpoint-specific capability checks; datasource Query alone is not proof that a plugin-admin mutation is allowed.
6. Native metric query/series/label APIs use the built-in `metricspanel` datasource policy. Prometheus-compatible query, metadata and `/metrics` endpoints cannot remain an alternate unguarded path. OSS query access is broad; there is **no promised field/label/SQL-row ACL**.

None plus dashboard View does not automatically imply arbitrary datasource Query. A grant sufficient to view dashboard JSON can still leave its query execution forbidden. Any future narrower native datasource permissions must be identified as an additional product policy, not claimed as OSS datasource-permissions parity.

## 10. MetricsPanel233 backend hook inventory

The following is an implementation checklist against current source. All aliases require the same service-level check. Handlers currently establish token/trusted organization context and resource validation; they do not yet implement these ACL actions. UI hiding is not enforcement.

| Current source / route family | Required central hook |
| --- | --- |
| [server.go](../internal/server/server.go) `protect`; [instance registry](../internal/instance/registry.go) `ResolvePrincipal`; [SDK principal](../internal/plugins/organization.go) | Replace blanket Viewer/None rejection with validated active principal + per-operation authorization **only after** every route is covered. Reject mismatched/disabled/deleting orgs and stale membership first. Preserve explicit org1 local compatibility independently. |
| [server.go](../internal/server/server.go), [grafana.go](../internal/server/grafana.go): native dashboard list/create/PUT/delete, Grafana `/db`, UID GET/delete, `/search` | Filter lists before counts/pagination; View for current JSON; source Write, destination Create, Delete as appropriate. Create/default ACL/content/history atomic. |
| [layout](../internal/server/dashboard_layout.go), [queries](../internal/server/dashboard_queries.go), [panels](../internal/server/dashboard_panels.go), [variables](../internal/server/dashboard_variables.go), [annotation definitions](../internal/server/dashboard_annotations.go) PATCH | Dashboard Write on stored resource before raw-baseline merge. If preview or validation actually queries sources, also authorize dependencies. Existing If-Match/raw precision behavior stays. |
| [folder/library routes](../internal/server/dashboard_resources.go) | Folder View/Edit/Admin actions, old/new move checks, library ownership/connection filtering, dashboard insert/unlink and whole-subtree delete preflight. No body-controlled scope resolution. |
| [history](../internal/server/dashboard_history.go), [history resource API](../internal/server/dashboard_history_resources.go), [exports](../internal/server/dashboard_export.go) | Dashboard Write for legacy/history list/detail/diff/historical export/restore; View for current native export. Namespace checks remain. Restore preserves current ACL. |
| [catalog](../internal/server/dashboard_catalog.go), [imports](../internal/server/grafana_import.go) | Catalog preview/read separate from destination Create. Binding existing library/data sources checks their use rights. Overwrite requires existing Write. Import cannot overwrite ACL or provisioning ownership. |
| [annotations.go](../internal/server/annotations.go) local/Grafana aliases, tags, by-ID, Graphite, mass-delete | Owner-aware annotation read/create/write/delete, query predicate before list/tag counts, atomic authorized bulk deletion; no reassignment via body IDs. |
| [plugins.go](../internal/server/plugins.go) datasource CRUD/list/health/query/resources/proxy | Datasource Read/Query versus Admin create/write/delete; dependency authorization before secrets. Special source IDs (`grafana`, `metricspanel`, expressions) are explicit policy branches. |
| [expressions.go](../internal/server/expressions.go) `expressionSourceQuery`, `writeExpressionGroups`; [SQL](../internal/server/sql_datasources.go) | Central evaluator on every real input, including parallel/background comparison requests. Pool credentials cannot be resolved before authorization. Raw SQL bounds/readonly guards are separate from identity ACLs. |
| [chunked.go](../internal/server/chunked.go) connections/query | Same datasource/action graph as `/api/ds/query`, preserving namespace/type checks. Authorize before any response frame. Session context remains bound throughout stream/cancel. |
| [apps.go](../internal/server/apps.go), [SDK apps](../internal/plugins/apps.go), [SDK manager](../internal/plugins/manager.go), [SDK streams](../internal/plugins/stream.go) | Plugin-app access versus configuration/resource mutation; enforce action before decrypt/backend call. Pass trustworthy org/user/role, not a client-produced PluginContext. |
| [plugin install routes](../internal/server/native_plugin_routes.go), [plugins.go](../internal/server/plugins.go), [catalog](../internal/server/plugin_catalog.go), public asset session | Global package install/update/uninstall versus org app enablement. Source/assets require valid intended capability. Check all org dependencies before global package removal; expiring asset ticket must not become an admin/session ticket. |
| [live.go](../internal/server/live.go), [Live hub](../internal/live/hub.go) session/ws/channels/publish | Reauthorize each subscribe/publish and channel discovery, including connected client's revoked role/team/SA. Bind upstream stream cache to effective identity/context, not channel alone. Filter diagnostic subscriber metadata. |
| [alerts.go](../internal/server/alerts.go), [graphs](../internal/server/alert_graph.go), [groups](../internal/server/alert_groups.go) native/provisioning/manual preview/evaluate/history | Folder+rule actions, per-query dependencies, current/new folder checks, group whole-diff authorization and provisioning status. Mask unauthorized runtime/history; default General policy for folderless rules. |
| [silences.go](../internal/server/silences.go) list/get/save/expire/preview | General/rule silence evaluator and immutable exact rule matcher; no hidden-rule metadata in preview. |
| [notifications.go](../internal/server/notifications.go), [provisioning](../internal/server/notification_provisioning.go), [persisted groups](../internal/store/notification_groups.go) | Separate contact-point/policy/instance read, write, test, retry and secrets permissions; source rule metadata filtered independently. |
| [templates](../internal/server/notification_templates.go), [mute timings](../internal/server/mute_timings.go), [inhibition](../internal/server/inhibition.go) | Notification-specific read/write/delete/preview capabilities; source/target rule diagnostic visibility, export scope and provisioned constraints. Folder Admin alone is insufficient. |
| [patterns.go](../internal/server/patterns.go), native query/stats/metric catalogs/Prometheus in [server.go](../internal/server/server.go) | Built-in datasource Query for stored metric/pattern reads; native capture/delete need explicit product write capability. Targets/secrets/ingest/scrape need collector Admin/write policy; metrics/stat metadata is not automatically public to None. |
| [collector](../internal/collector), [alert engine](../internal/alerting/engine.go), [notification worker](../internal/alerting/notifications.go), [instance background lifecycle](../internal/instance/registry.go) | Trusted org System context, no session impersonation. Scheduling/outbox retries stay within physical owner; interactive callers cannot choose System. Background lookup callbacks must not manufacture user access. |

**Live acceptance blocker:** `Hub.acquire` currently caches upstream streams by channel and uses the first subscriber's PluginContext. Later identities in the same org share that upstream. Physical org isolation therefore does not prove user-specific data isolation. Split stream identity/cache where plugins depend on user/context; continue authorizing every subscriber and publisher. On revocation invalidate or terminate affected active streams, not only new requests.

## 11. Proposed org-local schema and evaluator interfaces

These tables belong to each physical organization SQLite store. Identity users, memberships, teams and service accounts remain in the identity catalog; never copy them as authoritative local rows. Stable org ID is still stored and checked to detect an incorrectly opened/copied database.

```sql
CREATE TABLE resource_acl_resources (
  org_id INTEGER NOT NULL CHECK (org_id > 0),
  resource_kind TEXT NOT NULL CHECK (resource_kind IN ('folder','dashboard')),
  resource_uid TEXT NOT NULL,
  version INTEGER NOT NULL CHECK (version > 0),
  defaults_initialized INTEGER NOT NULL CHECK (defaults_initialized IN (0,1)),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (org_id, resource_kind, resource_uid)
);
CREATE TABLE resource_acl_grants (
  org_id INTEGER NOT NULL,
  resource_kind TEXT NOT NULL,
  resource_uid TEXT NOT NULL,
  subject_kind TEXT NOT NULL CHECK (subject_kind IN ('user','service-account','team','role')),
  subject_key TEXT NOT NULL,
  permission INTEGER NOT NULL CHECK (permission IN (1,2,4)),
  identity_generation INTEGER NOT NULL CHECK (
    (subject_kind='role' AND identity_generation=0) OR
    (subject_kind!='role' AND identity_generation>0)),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (org_id, resource_kind, resource_uid, subject_kind, subject_key),
  FOREIGN KEY (org_id, resource_kind, resource_uid)
    REFERENCES resource_acl_resources(org_id, resource_kind, resource_uid) ON DELETE CASCADE
);
CREATE INDEX resource_acl_subject ON resource_acl_grants(org_id, subject_kind, subject_key);
CREATE TABLE resource_acl_cleanup_receipts (
  job_key TEXT PRIMARY KEY,
  org_id INTEGER NOT NULL,
  subject_kind TEXT NOT NULL,
  subject_key TEXT NOT NULL,
  identity_version INTEGER NOT NULL,
  identity_generation INTEGER NOT NULL CHECK (identity_generation>0),
  completed_at INTEGER NOT NULL
);
```

Proposed invariants:

- Canonical decimal IDs for user/SA/team; role keys limited to Viewer/Editor/Admin. None and server-admin are not role assignment subjects. Numeric user ID equal to SA ID is still a different subject. No wildcard resource UID and no cross-org references.
- A dashboard ACL key is the stable native `Dashboard.ID`. Grafana external UID routes first resolve the actual stored dashboard; changing imported raw UID must not create a second ACL identity. Real folders use their UID. General is not an editable inheritable ACL row: its fixed folder-read/create capabilities are separate, so they cannot accidentally make every root dashboard visible.
- `resource_acl_resources` has no fabricated folder relation. Resolve current folder/parents from actual `folders.parent_uid` and dashboard ownership metadata in the same transaction/snapshot. Library, rules and dashboard annotations derive actions through their own stored folder/dashboard and reuse the folder evaluator; they are not fake dashboard rows.
- Parent traversal has a bounded depth and visited UID set. The current native parent validator detects cycles but has no configured depth limit; adding a shared creation/move/authorization limit remains a required integration change. A pure evaluator may use a defensive 64-ancestor ceiling initially and deny deeper chains; that is a native safety bound, not Grafana's documented four-level default. Missing/cyclic parents deny and report a clear integrity error.
- A full replacement accepts direct grants only; inherited and virtual rows are read-only projections. Duplicate subjects, invalid permission, inconsistent key kind, inactive foreign identity or stale ACL version reject the entire mutation.
- ACL versions increment for direct mutations and cleanup. A request-local effective result also records ancestry revisions and fresh identity versions; moves/team changes invalidate cached results even if child ACL version did not change. Start without a process-wide authorization cache.
- Missing ACL registration is **fail closed**, except an explicitly recorded migration/default branch. No automatic “missing rows means Viewer/Editor all” fallback after initialization.
- Creator ownership is an ACL grant, not an immutable backdoor. Unknown principal types cannot get creator Admin. Org Admin baseline is computed from a current trusted principal, never stored as a deletable managed row. Every matching identity grant also requires the current membership/team/SA incarnation; a role change alone does not invalidate it.

Implemented pure typed entry points, before HTTP/CRUD integration, are in [the permission model](../internal/model/permissions.go) and [the detached permission store](../internal/store/resource_permissions.go):

```go
type PermissionResource struct { OrgID int64; Kind PermissionResourceKind; UID string }
type ResourcePermissionSubject struct { Kind PermissionSubjectKind; ID int64; Role string }
type ResourcePermissionGrant struct {
  Subject ResourcePermissionSubject
  Permission ResourcePermissionLevel
  IdentityGeneration int64
}

// Internal trusted code; caller context must belong to the physical owner.
InitializeResourcePermissionSchema(ctx) error
ResourcePermissions(ctx, ref) (model.ResourcePermissionDocument, error)
ReplaceResourcePermissions(ctx, ref, expectedVersion, grants, identities) (model.ResourcePermissionDocument, error)
InitializeResourcePermissions(ctx, ref, seed, identities) (model.ResourcePermissionDocument, error)
EffectiveResourcePermission(ctx, ref, principal, identities) (model.EffectiveResourcePermission, error)
CleanupResourcePermissionSubject(ctx, cleanupJob) error
```

These pure store functions establish representation, CAS and positive-grant evaluation. They do **not** establish route authorization merely by existing. Permission administration checks need the future action evaluator and same-transaction resource mutation helpers; `ReplaceResourcePermissions` must not be callable from an unguarded public endpoint.

### Dynamic identity boundary

Inject callbacks from the catalog instead of adding a store→HTTP/global-dispatch dependency:

```go
type PermissionIdentityResolver struct {
  CurrentPrincipal func(context.Context, identity.Principal) (identity.Principal, error)
  ValidateSubject func(context.Context, int64, model.ResourcePermissionSubject) (generation int64, err error)
  TeamsForPrincipal func(context.Context, int64, model.ResourcePermissionSubject) ([]identity.TeamGrant, error)
}
```

CurrentPrincipal reloads enabled user/SA, active org/membership and role. ValidateSubject rejects disabled/removing users, deleting teams and foreign SA/IDs, returning the membership's durable **Generation**, not its mutable Version. Nonreused team/SA IDs use stable generation 1. TeamsForPrincipal uses only currently active team/member grants; team Admin membership is not Org Admin or folder Admin. The implemented [catalog adapter](../internal/instance/permissions.go) calls [UserPrincipal/Membership](../internal/identity/accounts.go), the [membership-generation ledger](../internal/identity/membership_generation.go), [ServiceAccount state](../internal/identity/service_accounts.go) and [TeamsForPrincipal/PendingTeamRemovals](../internal/identity/teams.go). It requires a request-bound credential revalidator, or a separately configured exact canonical internal principal. The evaluator invokes CurrentPrincipal for every identity type, so a supplied System/Local type cannot skip this check. Do not trust stale `Principal.Role`, request `teamIds`, SDK role strings or a token's old role snapshot.

Resolve callbacks **outside a SQLite write lock**; never call another database or network while holding the organization store's single writer connection. The pure store records generation on grant writes and compares it to fresh catalog generation on **every** effective evaluation. Thus an old validated write arriving after cleanup is ineffective when the same user rejoins. Explicitly resubmitting a stale grant generation fails. Membership role/profile changes preserve generation and direct grants. Failed lookup denies; it must not restore last-known grants. A callback cannot change the principal's org/type/ID to satisfy a resource reference. Service dispatch/query execution still need revocation checks around long-running operations; this representation is not a cross-database atomic permission snapshot.

## 12. Resumable cross-database cleanup

The identity catalog and organization resource store cannot share a reliable SQLite transaction. Use durable state transitions and idempotent receipts:

1. Identity transaction changes membership/team/SA to removing/deleting/disabled, revokes sessions/tokens where appropriate and creates a versioned cleanup job. Authentication and dynamic lookup stop granting access immediately.
2. A trusted registry worker opens only the registered matching org store; it does not accept an HTTP path as a database name. Remove matching typed direct grants, delegated management links, team references and selected ownership metadata in one local transaction. Increment affected ACL revisions and write the cleanup receipt with job ID/target identity version.
3. After local commit, invalidate authorization/search/Live caches and active subscriptions. Retry this step safely after restart; a closed/unavailable org leaves the catalog job pending and access denied.
4. Complete the catalog job with its exact CAS version only after a receipt for that org exists. Existing [team tombstone APIs](../internal/identity/teams.go) and [membership removal completion](../internal/identity/accounts.go) are the starting points; service-account/global-user deletion needs equivalent durable jobs rather than immediate identity deletion followed by best-effort cleanup.
5. Replaying a completed job is a no-op. Cleanup includes both job CAS version and target identity generation, deletes only that generation or older and leaves newer legitimate grants intact. Re-add uses the ledger's next generation; old late writes never regain access. Obsolete inactive-generation rows can be removed by a bounded reconciliation job later. Organization deletion fences the whole instance, stops workers and completes cleanup according to registry lifecycle before deleting files.

Team membership removal needs no delete of the team's shared ACL grant: the dynamic team lookup ceases matching that person immediately. Team deletion removes team-scoped grants. Role demotion needs no rewriting of exact Role grants, but session permission caches and Live must refresh. Last-active-human-administrator checks run in the identity transaction, not after resource cleanup.

## 13. Implementation order and acceptance evidence

1. Pure org-local schema/models/default policy/CAS/evaluator and typed cleanup. Test None explicit user/SA/team grants, exact Role matching, Admin baseline, parent unions, invalid subjects, depth/cycles, no cross-org and concurrent replacement. Schema/default initialization owned by Store opening/migration, not every request.
2. Atomic existing resource create/import/move/delete/history hooks; migrate org1 and restart midway to prove defaults are seeded once. No external multiuser dispatcher yet.
3. Central action authorization on all native/Grafana aliases and list predicates. Add permissions HTTP APIs with the explicitly chosen legacy/native contracts.
4. Query/proxy/SDK/chunked/Live dependency checks; split user-sensitive stream cache. Only then replace Viewer gates and expose authenticated None principals through the registry.
5. Global identity dispatch, browser/SA workflows and revocation tests, with current-session role reload. Do not describe hidden controls or isolated databases as resource-ACL completion.

Mandatory tests before release:

- Viewer cannot read/list/search/export a private dashboard, discover its annotation tags/counts or fetch its history; an explicit View sees current JSON but not legacy history. Explicit Edit on one dashboard cannot mutate a sibling.
- Editor's direct View does not override exact Editor Edit or ancestor Edit. Removing the broader managed grant narrows access. Org Admin retains recovery access. None with an explicit grant works through HTTP/CLI/SDK only for authorized actions.
- Same UID/user-number/SA-number in separate org stores stays isolated; foreign subject binding and ciphertext swapping remain rejected. Namespace/header selection never supplies authorization.
- Root/legacy/nested creator defaults follow the selected branch; ACL CAS conflict and failed multi-item validation preserve all old grants. A permission mutation does not rewrite raw dashboard/plugin fields or wide numeric values.
- Source/destination moves, folder cycle/depth, destination escalation and connected library constraints are enforced before commit. Failed bulk delete/group replace performs no partial change.
- General versus rule-specific silences, matcher immutability and orphan rule ownership are checked. Folder Admin cannot administer unrelated notification resources or issue SA tokens without delegated management rights.
- Every datasource dependency in expressions/variables/annotations/comparisons is authorized before any execution. Native Prometheus/metric aliases, health/resources/proxy and chunked requests cannot bypass the same policy.
- Same-org two-user SDK Live tests prove user-specific streams do not reuse another user's context. Team/role/session/SA revocation terminates or refreshes active streams; expired asset/Live tickets do not keep admin access.
- Crash after identity tombstone, local cleanup commit and before catalog completion each resumes safely. Membership re-add cannot resurrect obsolete grants. Last active human Org Admin cannot be removed through any account/team cleanup path.

The store/model foundation has fifteen focused tests, including concurrent replacement, typed human/SA collisions, bounded/malformed ancestry, a real two-SQLite-database delayed-write/removal/rejoin test, content deletion/rollback and stale drafts after same-UID recreation. Content delete triggers remove direct ACLs and grants in the same transaction. A durable org/kind/UID revision ledger survives deletion; new ACLs advance past every prior version, so an old draft cannot update a recreated resource. Replacement and cleanup advance the same ledger, failed transactions consume no revision, and integer exhaustion is rejected. These monotonic native CAS revisions are an implementation safeguard, not a claim about Grafana's legacy POST contract.

Seven separate catalog-adapter tests verify actual session/token revocation, current roles/teams and membership generations, inactive organizations, fake internal identities and restart using separate SQLite catalogs/control stores. The central opt-in routing boundary is also tested separately. No production action policy or multi-user dispatcher is enabled. Atomic CRUD seeding/moves, complete migration, filtered list/action enforcement, last-effective-manager service checks, HTTP compatibility, query/Live revocation and dispatcher exposure remain unimplemented. The verified source differences above remain explicit compatibility decisions until those production paths and tests land.
