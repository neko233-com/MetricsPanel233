import { t as tr } from "./i18n";
import { getLocale, setLocale, type Locale } from "./i18n";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  Activity,
  ChartNoAxesColumn,
  CircleCheck,
  LayoutDashboard,
  LogOut,
  Menu,
  Monitor,
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
type View =
  | "overview"
  | "dashboards"
  | "dashboard"
  | "explore"
  | "collectors"
  | "cli";
const navigation = [
  { id: "overview", name: "Overview", icon: LayoutDashboard },
  { id: "dashboards", name: "Dashboards", icon: ChartNoAxesColumn },
  { id: "explore", name: "Explore", icon: Search },
  { id: "collectors", name: "Collectors", icon: Server },
  { id: "cli", name: "Agent CLI", icon: Terminal },
] as const;
export default function App() {
  const routeLoaded = useRef(false);
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
  const [view, setView] = useState<View>(() => {
    const hash=location.hash.slice(1);
    return navigation.some((n)=>n.id===hash) ? hash as View : "overview";
  }),
    [dashboardID, setDashboardID] = useState("system"),
    [sidebarOpen, setSidebarOpen] = useState(false);
  const [dashboards, setDashboards] = useState<Dashboard[]>([]),
    [targets, setTargets] = useState<Target[]>([]),
    [metrics, setMetrics] = useState<Metric[]>([]),
    [stats, setStats] = useState<Stats | null>(null);
  const [tick, setTick] = useState(0),
    [range, setRange] = useState("30m"),
    [auth, setAuth] = useState(false),
    [token, setToken] = useState(""),
    [connectionError, setConnectionError] = useState(""),
    [notice, setNotice] = useState<{
      text: string;
      error: boolean;
    } | null>(null);
  const notify = useCallback(
    (text: string, error = false) => setNotice({ text, error }),
    [],
  );
  const load = useCallback(async () => {
    try {
      const [ds, ts, ms, st] = await Promise.all([
        api<Dashboard[]>("/dashboards"),
        api<Target[]>("/targets"),
        api<Metric[]>("/metrics"),
        api<Stats>("/stats"),
      ]);
      setDashboards(ds);
      setTargets(ts);
      setMetrics(ms);
      setStats(st);
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
  useEffect(() => {
    void load();
    const timer = setInterval(() => {
      if (!document.hidden) void load();
    }, 5000);
    return () => clearInterval(timer);
  }, [load]);
  useEffect(() => {
    if (!notice) return;
    const timer = setTimeout(() => setNotice(null), notice.error ? 8000 : 3500);
    return () => clearTimeout(timer);
  }, [notice]);
  useEffect(() => {
    if (routeLoaded.current || !dashboards.length) return;
    routeLoaded.current = true;
    const uid = /^\/d\/([^/]+)/.exec(location.pathname)?.[1];
    const dashboard = uid ? dashboards.find((d) => dashboardUID(d) === decodeURIComponent(uid)) : undefined;
    if (dashboard) { setDashboardID(dashboard.id); setView("dashboard"); }
  }, [dashboards]);
  function navigate(next: View) {
    setView(next);
    setSidebarOpen(false);
    if (next !== "dashboard") history.replaceState({}, "", `/#${next}`);
  }
  const current = dashboards.find(
    (d) => d.id === (view === "overview" ? "system" : dashboardID),
  );
  const title =
    view === "dashboard"
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
                  onRange={setRange}
                  tick={tick}
                  refresh={() => void load()}
                />
              ) : (
                <DashboardView
                  dashboard={current}
                  stats={stats}
                  targets={targets}
                  metrics={metrics}
                  tick={tick}
                  range={range}
                  onRange={setRange}
                  refresh={() => void load()}
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
                setDashboardID(d.id);
                history.replaceState({}, "", `/d/${encodeURIComponent(dashboardUID(d))}/${encodeURIComponent(d.name)}`);
                navigate("dashboard");
              }}
            />
          )}
          {view === "explore" && <Explore metrics={metrics} tick={tick} />}
          {view === "collectors" && (
            <Collectors targets={targets} reload={load} notify={notify} />
          )}
          {view === "cli" && <AgentCLI />}
        </main>
      </div>
      {notice && (
        <div
          className={`toast ${notice.error ? "error" : ""}`}
          role={notice.error ? "alert" : "status"}
        >
          <CircleCheck size={18} />
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
