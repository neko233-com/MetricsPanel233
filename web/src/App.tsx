import { t as tr } from "./i18n";
import { getLocale, setLocale, type Locale } from "./i18n";
import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import {
  Activity,
  Bell,
  ChartNoAxesColumn,
  CircleCheck,
  CircleAlert,
  Info,
  TriangleAlert,
  LayoutDashboard,
  LogOut,
  Menu,
  Monitor,
  Puzzle,
  Search,
  Server,
  Terminal,
  X,
} from "lucide-react";
import {
  api,
  dashboardUID,
  ApiError,
  message,
  type Dashboard,
  type Metric,
  type Stats,
  type Target,
} from "./api";
import { DashboardView } from "./features/DashboardView";
import { Dashboards } from "./features/Dashboards";
import { Explore } from "./features/Explore";
import { Collectors } from "./features/Collectors";
import { AgentCLI } from "./features/AgentCLI";
import { TemplateView } from "./features/TemplateView";
import { Alerts } from "./features/Alerts";
import { Plugins } from "./features/Plugins";
import {
  connectAppEvents,
  refreshWorkspace,
  updateWorkspaceTimeRange,
  type NoticeSeverity,
} from "./grafana/app-events";
import {
  rangeFromURL,
  rangeFromDashboard,
  resolveTimeRange,
  writeRangeURL,
  type TimeSelection,
  refreshFromDashboard,
  refreshOptionsFromDashboard,
  refreshMilliseconds,
} from "./grafana/time-range";
const AppPluginView = lazy(() => import("./grafana/AppPluginView"));
const ExtensionHost = lazy(() => import("./grafana/ExtensionHost"));
type View =
  | "overview"
  | "dashboards"
  | "dashboard"
  | "explore"
  | "collectors"
  | "alerts"
  | "plugins"
  | "cli";
type AppView = View | "app";
const navigation = [
  { id: "overview", name: "Overview", icon: LayoutDashboard },
  { id: "dashboards", name: "Dashboards", icon: ChartNoAxesColumn },
  { id: "explore", name: "Explore", icon: Search },
  { id: "collectors", name: "Collectors", icon: Server },
  { id: "alerts", name: "Alerts", icon: Bell },
  { id: "plugins", name: "Plugins", icon: Puzzle },
  { id: "cli", name: "Agent CLI", icon: Terminal },
] as const;
export default function App() {
  const routeLoaded = useRef(false);
  const [initialRange] = useState(() => {
    try {
      return { value: rangeFromURL() || "30m", error: "" };
    } catch (error) {
      return { value: "30m", error: message(error) };
    }
  });
  const [rangeError, setRangeError] = useState(initialRange.error);
  const [refreshOverride, setRefreshOverride] = useState<string | null>(() =>
    new URLSearchParams(location.search).get("refresh"),
  );
  const [refreshError, setRefreshError] = useState("");
  const [refreshWidth, setRefreshWidth] = useState(window.innerWidth);
  const [locale, updateLocale] = useState<Locale>(getLocale());
  const languageButton = (
    <button
      className="language-button"
      aria-label="Switch language"
      onClick={() => {
        const next = locale === "en" ? "zh" : "en";
        setLocale(next);
        updateLocale(next);
      }}
    >
      {locale === "en" ? "中文" : "English"}
    </button>
  );
  const [view, setView] = useState<AppView>(() => {
      if (/^\/a\/[^/]+/.test(location.pathname)) return "app";
      const hash = location.hash.slice(1);
      return navigation.some((n) => n.id === hash)
        ? (hash as View)
        : "overview";
    }),
    [dashboardID, setDashboardID] = useState("system"),
    [sidebarOpen, setSidebarOpen] = useState(false);
  const [dashboards, setDashboards] = useState<Dashboard[]>([]),
    [apps, setApps] = useState<
      {
        id: string;
        name: string;
        pinned?: boolean;
        enabled?: boolean;
        type: string;
      }[]
    >([]),
    [targets, setTargets] = useState<Target[]>([]),
    [metrics, setMetrics] = useState<Metric[]>([]),
    [stats, setStats] = useState<Stats | null>(null);
  const [hasExtensions, setHasExtensions] = useState(false);
  const [tick, setTick] = useState(0),
    [range, setRange] = useState<TimeSelection>(initialRange.value),
    [auth, setAuth] = useState(false),
    [token, setToken] = useState(""),
    [connectionError, setConnectionError] = useState(""),
    [notice, setNotice] = useState<{
      text: string;
      severity: NoticeSeverity;
    } | null>(null);
  const notify = useCallback(
    (text: string, error = false) =>
      setNotice({ text, severity: error ? "error" : "success" }),
    [],
  );
  const current = dashboards.find(
    (d) => d.id === (view === "overview" ? "system" : dashboardID),
  );
  const refreshChoice =
    refreshOverride ??
    ((view === "dashboard" || view === "overview") && current?.grafana
      ? refreshFromDashboard(current.grafana)
      : "5s");
  const refreshResolution = refreshChoice === "auto" ? refreshWidth : 0;
  const changeRefresh = useCallback(
    (next: string) => {
      try {
        refreshMilliseconds(next, range);
        setRefreshOverride(next);
        setRefreshError("");
        const params = new URLSearchParams(location.search);
        params.set("refresh", next);
        history.replaceState(
          history.state,
          "",
          `${location.pathname}?${params}${location.hash}`,
        );
      } catch (error) {
        notify(tr(message(error)), true);
      }
    },
    [range, notify],
  );
  const load = useCallback(async () => {
    try {
      const [ds, ts, ms, st, ps] = await Promise.all([
        api<Dashboard[]>("/dashboards"),
        api<Target[]>("/targets"),
        api<Metric[]>("/metrics"),
        api<Stats>("/stats"),
        api<
          {
            id: string;
            name: string;
            pinned?: boolean;
            enabled?: boolean;
            type: string;
            extensions?: {
              addedLinks?: unknown[];
              addedComponents?: unknown[];
              addedFunctions?: unknown[];
              exposedComponents?: unknown[];
            };
          }[]
        >("/api/plugins"),
      ]);
      setDashboards(ds);
      setTargets(ts);
      setMetrics(ms);
      setStats(st);
      setHasExtensions(
        ps.some(
          (plugin) =>
            plugin.type === "app" &&
            plugin.enabled &&
            Object.values(plugin.extensions || {}).some(
              (entries) => entries?.length,
            ),
        ),
      );
      setApps(
        ps.filter(
          (plugin) => plugin.type === "app" && plugin.enabled && plugin.pinned,
        ),
      );
      setTick((t) => t + 1);
      setConnectionError("");
      setAuth(false);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        setAuth(true);
        setConnectionError(
          sessionStorage.getItem("metricspanel-token")
            ? "Invalid API token."
            : "",
        );
      } else setConnectionError(message(e));
    }
  }, []);
  useEffect(
    () =>
      connectAppEvents({
        refresh: load,
        notify: (text, severity) => setNotice({ text, severity }),
      }),
    [load],
  );
  const changeRange = useCallback(
    (next: TimeSelection) => {
      try {
        resolveTimeRange(next);
        setRange(next);
        setRangeError("");
        writeRangeURL(next);
        updateWorkspaceTimeRange(next);
      } catch (error) {
        notify(tr(message(error)), true);
      }
    },
    [notify],
  );
  useEffect(() => {
    void load();
  }, [load]);
  useEffect(() => {
    const resized = () => setRefreshWidth(window.innerWidth);
    window.addEventListener("resize", resized);
    return () => window.removeEventListener("resize", resized);
  }, []);
  useEffect(() => {
    let interval: number;
    try {
      interval = refreshMilliseconds(
        refreshChoice,
        range,
        refreshResolution || window.innerWidth,
      );
      setRefreshError("");
    } catch (error) {
      setRefreshError(message(error));
      return;
    }
    if (!interval) return;
    let timer: ReturnType<typeof setTimeout>,
      blocked = false;
    const schedule = (deadline = Date.now() + interval) => {
      timer = setTimeout(
        () => {
          if (Date.now() < deadline) {
            schedule(deadline);
            return;
          }
          if (document.hidden) blocked = true;
          else refreshWorkspace();
          schedule();
        },
        Math.min(2147483647, Math.max(1, deadline - Date.now())),
      );
    };
    const resume = () => {
      if (blocked && !document.hidden) {
        blocked = false;
        clearTimeout(timer);
        refreshWorkspace();
        schedule();
      }
    };
    schedule();
    document.addEventListener("visibilitychange", resume);
    return () => {
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", resume);
    };
  }, [refreshChoice, range, refreshResolution]);
  useEffect(() => {
    if (!notice) return;
    const timer = setTimeout(
      () => setNotice(null),
      notice.severity === "error" ? 8000 : 3500,
    );
    return () => clearTimeout(timer);
  }, [notice]);
  useEffect(() => {
    if (routeLoaded.current || !dashboards.length) return;
    routeLoaded.current = true;
    const uid = /^\/d\/([^/]+)/.exec(location.pathname)?.[1];
    const dashboard = uid
      ? dashboards.find((d) => dashboardUID(d) === decodeURIComponent(uid))
      : undefined;
    if (dashboard) {
      setDashboardID(dashboard.id);
      setView("dashboard");
      try {
        const next =
          rangeFromURL(
            new URLSearchParams(location.search),
            rangeFromDashboard(dashboard.grafana) || "30m",
          ) ||
          rangeFromDashboard(dashboard.grafana) ||
          "30m";
        setRange(next);
        setRangeError("");
      } catch (error) {
        setRangeError(message(error));
      }
    }
  }, [dashboards]);
  useEffect(() => {
    const restore = () => {
      setRefreshOverride(new URLSearchParams(location.search).get("refresh"));
      const uid = /^\/d\/([^/]+)/.exec(location.pathname)?.[1];
      const dashboard = uid
        ? dashboards.find(
            (item) => dashboardUID(item) === decodeURIComponent(uid),
          )
        : undefined;
      try {
        const fallback = rangeFromDashboard(dashboard?.grafana) || "30m";
        const next =
          rangeFromURL(new URLSearchParams(location.search), fallback) ||
          fallback;
        setRange(next);
        setRangeError("");
        updateWorkspaceTimeRange(next);
      } catch (error) {
        setRangeError(message(error));
      }
      if (dashboard) {
        setDashboardID(dashboard.id);
        setView("dashboard");
      } else if (/^\/a\//.test(location.pathname)) setView("app");
      else if (navigation.some((item) => item.id === location.hash.slice(1)))
        setView(location.hash.slice(1) as View);
    };
    window.addEventListener("popstate", restore);
    return () => window.removeEventListener("popstate", restore);
  }, [dashboards]);
  function navigate(next: View) {
    setView(next);
    setSidebarOpen(false);
    if (next !== "dashboard")
      history.replaceState({}, "", `/${location.search}#${next}`);
  }
  const title =
    view === "app"
      ? "Application"
      : view === "dashboard"
        ? current?.name || "Dashboard"
        : navigation.find((n) => n.id === view)?.name;
  if (auth)
    return (
      <main className="login">
        {languageButton}
        <Activity size={42} />
        <h1>{tr("MetricsPanel233")}</h1>
        <p>{tr("Enter your workspace API token.")}</p>
        <form
          onSubmit={async (e) => {
            e.preventDefault();
            sessionStorage.setItem("metricspanel-token", token);
            await load();
          }}
        >
          <label>
            {tr("API token")}
            <input
              autoFocus
              type="password"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              required
              autoComplete="current-password"
            />
          </label>
          {connectionError && <p className="form-error">{connectionError}</p>}
          <button className="primary">{tr("Connect workspace")}</button>
        </form>
        <small>{tr("The token is kept in this browser tab's session.")}</small>
      </main>
    );
  return (
    <div className="app-shell">
      {sidebarOpen && (
        <button
          className="sidebar-overlay"
          onClick={() => setSidebarOpen(false)}
          aria-label={tr("Close navigation")}
        />
      )}
      <aside className={`sidebar ${sidebarOpen ? "open" : ""}`}>
        <a
          href="#overview"
          onClick={(e) => {
            e.preventDefault();
            navigate("overview");
          }}
          className="brand"
        >
          <Activity size={38} strokeWidth={2} />
          <span>{tr("MetricsPanel233")}</span>
        </a>
        <nav aria-label={tr("Main navigation")}>
          {navigation.map((item) => (
            <button
              key={item.id}
              className={
                view === item.id ||
                (view === "dashboard" && item.id === "dashboards")
                  ? "active"
                  : ""
              }
              onClick={() => navigate(item.id)}
            >
              <item.icon size={23} strokeWidth={1.8} />
              <span>{tr(item.name)}</span>
            </button>
          ))}
          {apps.map((app) => (
            <a
              className="app-nav-link"
              key={app.id}
              href={`/a/${encodeURIComponent(app.id)}/`}
              aria-current={
                view === "app" && location.pathname.split("/")[2] === app.id
                  ? "page"
                  : undefined
              }
            >
              <Puzzle size={23} />
              <span>{app.name}</span>
            </a>
          ))}
        </nav>
        <div className="workspace">
          <Monitor size={22} />
          <div>
            <span>{tr("Local workspace")}</span>
            <small className="mono">{tr("SQLite \u00B7 self-hosted")}</small>
          </div>
          {sessionStorage.getItem("metricspanel-token") && (
            <button
              className="icon-button"
              title={tr("Disconnect")}
              aria-label={tr("Disconnect workspace")}
              onClick={() => {
                sessionStorage.removeItem("metricspanel-token");
                setAuth(true);
              }}
            >
              <LogOut size={17} />
            </button>
          )}
        </div>
      </aside>
      <div className="workspace-main">
        {hasExtensions && (
          <Suspense fallback={null}>
            <ExtensionHost />
          </Suspense>
        )}
        <header className="topbar">
          <div>
            <button
              className="icon-button mobile-menu"
              onClick={() => setSidebarOpen(true)}
              aria-label={tr("Open navigation")}
            >
              <Menu size={22} />
            </button>
            <span>{tr("Workspace")}</span>
            <span className="breadcrumb-slash">/</span>
            <span>{tr(title || "Dashboard")}</span>
          </div>
          <div className="topbar-status">
            {languageButton}
            <span className="status healthy">
              <i />
              {tr("Self-hosted")}
            </span>
            <span className="topbar-product">{tr("MetricsPanel233")}</span>
          </div>
        </header>
        <main>
          {rangeError && (
            <p className="form-error" role="alert">
              {tr(rangeError)}
            </p>
          )}
          {refreshError && (
            <p className="form-error" role="alert">
              {tr(refreshError)}
            </p>
          )}
          {connectionError && (
            <div className="connection-error" role="alert">
              <span>
                {tr("Connection failed:")}
                {connectionError}
              </span>
              <button onClick={() => void load()}>{tr("Retry")}</button>
            </div>
          )}
          {(view === "overview" || view === "dashboard") &&
            (current ? (
              current.grafana ? (
                <TemplateView
                  key={current.id}
                  dashboard={current}
                  range={range}
                  onRange={changeRange}
                  tick={tick}
                  refresh={refreshWorkspace}
                  refreshChoice={refreshChoice}
                  refreshOptions={refreshOptionsFromDashboard(current.grafana)}
                  onRefreshChoice={changeRefresh}
                />
              ) : (
                <DashboardView
                  dashboard={current}
                  stats={stats}
                  targets={targets}
                  metrics={metrics}
                  tick={tick}
                  range={range}
                  onRange={changeRange}
                  refresh={refreshWorkspace}
                  refreshChoice={refreshChoice}
                  onRefreshChoice={changeRefresh}
                  reload={load}
                  notify={notify}
                  cli={() => navigate("cli")}
                />
              )
            ) : (
              <div className="loading-state">
                <Activity size={28} />
                <p>
                  {tr(
                    connectionError
                      ? "Start metricspanel serve to connect this workspace."
                      : "Connecting to your workspace…",
                  )}
                </p>
              </div>
            ))}
          {view === "dashboards" && (
            <Dashboards
              dashboards={dashboards}
              reload={load}
              notify={notify}
              open={(d) => {
                setRefreshOverride(null);
                setDashboardID(d.id);
                history.replaceState(
                  {},
                  "",
                  `/d/${encodeURIComponent(dashboardUID(d))}/${encodeURIComponent(d.name)}`,
                );
                try {
                  const next = rangeFromDashboard(d.grafana) || "30m";
                  setRange(next);
                  setRangeError("");
                  updateWorkspaceTimeRange(next);
                } catch (error) {
                  setRangeError(message(error));
                }
                navigate("dashboard");
              }}
            />
          )}
          {view === "explore" && <Explore metrics={metrics} tick={tick} />}
          {view === "collectors" && (
            <Collectors targets={targets} reload={load} notify={notify} />
          )}
          {view === "cli" && <AgentCLI />}
          {view === "plugins" && <Plugins notify={notify} />}
          {view === "app" && (
            <Suspense fallback={<p>{tr("Loading plugin…")}</p>}>
              <AppPluginView
                id={decodeURIComponent(location.pathname.split("/")[2] || "")}
                tick={tick}
              />
            </Suspense>
          )}
          {view === "alerts" && (
            <Alerts tick={tick} metrics={metrics} notify={notify} />
          )}
        </main>
      </div>
      {notice && (
        <div
          className={`toast ${notice.severity}`}
          role={
            notice.severity === "error" || notice.severity === "warning"
              ? "alert"
              : "status"
          }
        >
          {notice.severity === "error" ? (
            <CircleAlert size={18} />
          ) : notice.severity === "warning" ? (
            <TriangleAlert size={18} />
          ) : notice.severity === "info" ? (
            <Info size={18} />
          ) : (
            <CircleCheck size={18} />
          )}
          <span>{notice.text}</span>
          <button
            className="icon-button"
            onClick={() => setNotice(null)}
            aria-label={tr("Dismiss notification")}
          >
            <X size={17} />
          </button>
        </div>
      )}
    </div>
  );
}
