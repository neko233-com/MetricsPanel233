import { Component, useEffect, useState, type ReactNode } from "react";
import { BrowserRouter, useLocation, useNavigate } from "react-router";
import {
  createTheme,
  PluginContextProvider,
  ThemeContext,
  type AppPlugin,
  type AppRootProps,
} from "@grafana/data";
import { loadPlugin, type InstalledPlugin } from "./plugin-runtime";
import { message } from "../api";
import { t } from "../i18n";

class AppBoundary extends Component<
  { children: ReactNode },
  { error: string }
> {
  state = { error: "" };
  static getDerivedStateFromError(error: unknown) {
    return { error: message(error) };
  }
  render() {
    return this.state.error ? (
      <p className="form-error" role="alert">
        {this.state.error}
      </p>
    ) : (
      this.props.children
    );
  }
}
const theme = createTheme({ colors: { mode: "dark" } });
function AppShell({ plugin }: { plugin: AppPlugin }) {
  const location = useLocation(),
    navigate = useNavigate();
  const query = Object.fromEntries(new URLSearchParams(location.search));
  const configPage = plugin.configPages?.find(
    (page) => page.id === query.config,
  );
  const Root = plugin.root;
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>{plugin.meta.name}</h1>
          <p>{t("Grafana application")}</p>
        </div>
        <a href="/#plugins">{t("Plugins")}</a>
      </div>
      {Boolean(plugin.configPages?.length) && (
        <nav className="app-tabs" aria-label={t("Application pages")}>
          <button
            aria-current={!configPage ? "page" : undefined}
            onClick={() => navigate(`/a/${plugin.meta.id}/`)}
          >
            {t("Application")}
          </button>
          {plugin.configPages!.map((page) => (
            <button
              key={page.id}
              aria-current={configPage?.id === page.id ? "page" : undefined}
              onClick={() =>
                navigate(
                  `/a/${plugin.meta.id}/?config=${encodeURIComponent(page.id)}`,
                )
              }
            >
              {page.title}
            </button>
          ))}
        </nav>
      )}
      <section className="app-plugin-content">
        {configPage ? (
          <configPage.body plugin={plugin} query={query} />
        ) : Root ? (
          <Root
            {...({
              meta: plugin.meta,
              basename: `/a/${plugin.meta.id}`,
              path: location.pathname,
              query,
              onNavChanged: () => {},
            } satisfies AppRootProps)}
          />
        ) : (
          <p role="alert">{t("App root component missing")}</p>
        )}
      </section>
    </>
  );
}
export default function AppPluginView({
  id,
  tick,
}: {
  id: string;
  tick: number;
}) {
  const [loaded, setLoaded] = useState<{
      plugin: AppPlugin;
      meta: AppPlugin["meta"];
      hash: string;
    } | null>(null),
    [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    void loadPlugin(id)
      .then((module) => {
        const plugin = module.plugin as AppPlugin;
        if (plugin.meta.type !== "app")
          throw new Error("Plugin is not an application");
        if (active) {
          setLoaded({
            plugin,
            meta: { ...plugin.meta },
            hash: (module.metricspanelInstalled as InstalledPlugin).sha256,
          });
          setError("");
        }
      })
      .catch((error) => {
        if (active) {
          setLoaded(null);
          setError(message(error));
        }
      });
    return () => {
      active = false;
    };
  }, [id, tick]);
  if (error)
    return (
      <p className="form-error" role="alert">
        {error}
      </p>
    );
  if (!loaded) return <p>{t("Loading plugin…")}</p>;
  return (
    <ThemeContext.Provider value={theme}>
      <BrowserRouter>
        <PluginContextProvider meta={loaded.meta}>
          <AppBoundary key={id + loaded.hash}>
            <AppShell plugin={loaded.plugin} />
          </AppBoundary>
        </PluginContextProvider>
      </BrowserRouter>
    </ThemeContext.Provider>
  );
}
