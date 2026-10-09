import * as React from "react";
import * as ReactDOM from "react-dom";
import * as JSXRuntime from "react/jsx-runtime";
import * as Data from "@grafana/data";
import * as EmotionCSS from "@emotion/css";
import * as EmotionReact from "@emotion/react";
import * as RxJS from "rxjs";
import * as RxOperators from "rxjs/operators";
import moment from "moment";
import i18next from "i18next";
import { registerOptionEditors } from "./option-editors";
import { createLiveService } from "./live-runtime";
import { api, interpolate, type InterpolationValues } from "../api";
import { getLocale } from "../i18n";
import type {
  BackendSrv,
  BackendSrvRequest,
  FetchResponse,
  DataSourceSrv,
} from "@grafana/runtime";

export type InstalledPlugin = {
  id: string;
  name: string;
  type: string;
  version: string;
  backend: boolean;
  enabled: boolean;
  signature: string;
  sha256: string;
  package_id: string;
  asset_path: string;
  metadata: Data.PluginMeta;
};
type Loader = {
  import: (url: string) => Promise<Record<string, unknown>>;
  set: (url: string, exports: Record<string, unknown>) => void;
  addImportMap: (map: { imports: Record<string, string> }) => void;
};
type DataSourceSettings = Data.DataSourceInstanceSettings & {
  version?: number;
  updated_at?: number;
};
type Runtime = typeof import("@grafana/runtime");
const moduleCache = new Map<string, Promise<Record<string, unknown>>>();
const panelCache = new Map<string, Data.PanelPlugin>();
const datasourceCache = new Map<
  string,
  { version: number; instance: Data.DataSourceApi }
>();
let initialized: Promise<Runtime> | undefined;
let variableValues: InterpolationValues = {},
  variableRange = "30m";
let sources: DataSourceSettings[] = [];

async function fetchRequest<T>(
  options: BackendSrvRequest,
  signal: AbortSignal,
): Promise<FetchResponse<T>> {
  const url = new URL(options.url, location.origin + "/");
  if (url.origin !== location.origin)
    throw new Error("External requests must use a configured datasource proxy");
  for (const [key, value] of Object.entries(options.params || {})) {
    if (Array.isArray(value))
      value.forEach((v) => url.searchParams.append(key, String(v)));
    else if (value !== undefined) url.searchParams.set(key, String(value));
  }
  const token = sessionStorage.getItem("metricspanel-token") || "";
  const body =
    options.data === undefined
      ? undefined
      : typeof options.data === "string" || options.data instanceof FormData
        ? options.data
        : JSON.stringify(options.data);
  const response = await fetch(url, {
    method: options.method || "GET",
    body,
    signal,
    credentials: "same-origin",
    headers: {
      ...(body && !(body instanceof FormData)
        ? { "Content-Type": "application/json" }
        : {}),
      ...options.headers,
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
  });
  const data = (
    options.responseType === "text"
      ? await response.text()
      : options.responseType === "blob"
        ? await response.blob()
        : options.responseType === "arraybuffer"
          ? await response.arrayBuffer()
          : response.status === 204
            ? undefined
            : await response.json()
  ) as T;
  if (!response.ok) {
    const detail = data as {
      error?: { message?: string } | string;
      message?: string;
    };
    throw {
      status: response.status,
      statusText: response.statusText,
      data,
      message:
        detail?.message ||
        (typeof detail?.error === "string"
          ? detail.error
          : detail?.error?.message) ||
        `HTTP ${response.status}`,
      config: options,
    };
  }
  return {
    data,
    status: response.status,
    statusText: response.statusText,
    ok: response.ok,
    headers: response.headers,
    redirected: response.redirected,
    type: response.type,
    url: response.url,
    config: options,
  };
}
function backendService(): BackendSrv {
  const fetchObservable = <T>(options: BackendSrvRequest) =>
    new RxJS.Observable<FetchResponse<T>>((subscriber) => {
      const controller = new AbortController();
      const abort = () => controller.abort();
      options.abortSignal?.addEventListener("abort", abort, { once: true });
      if (options.abortSignal?.aborted) controller.abort();
      void fetchRequest<T>(options, controller.signal)
        .then((response) => {
          subscriber.next(response);
          subscriber.complete();
        })
        .catch((error) => subscriber.error(error));
      return () => {
        controller.abort();
        options.abortSignal?.removeEventListener("abort", abort);
      };
    });
  const request = <T>(options: BackendSrvRequest) =>
    RxJS.lastValueFrom(fetchObservable<T>(options)).then((v) => v.data);
  return {
    fetch: fetchObservable,
    request,
    get: (url, params, _id, options) =>
      request({ ...options, url, params, method: "GET" }),
    post: (url, data, options) =>
      request({ ...options, url, data, method: "POST" }),
    put: (url, data, options) =>
      request({ ...options, url, data, method: "PUT" }),
    patch: (url, data, options) =>
      request({ ...options, url, data, method: "PATCH" }),
    delete: (url, data, options) =>
      request({ ...options, url, data, method: "DELETE" }),
    datasourceRequest: (options) =>
      RxJS.lastValueFrom(fetchObservable(options)),
    chunked: () =>
      new RxJS.Observable((subscriber) =>
        subscriber.error(
          new Error("Chunked datasource streaming is not available yet"),
        ),
      ),
  };
}
function pluginMeta(p: InstalledPlugin): Data.PluginMeta {
  return {
    ...p.metadata,
    id: p.id,
    name: p.name,
    type: p.type as Data.PluginType,
    baseUrl: `public/plugins/${p.id}`,
    module: `public/plugins/${p.id}/module.js`,
    info: { ...p.metadata.info, version: p.version },
  };
}
async function init(): Promise<Runtime> {
  if (initialized) return initialized;
  initialized = (async () => {
    const bootWindow = window as unknown as {
      grafanaBootData: unknown;
      System: Loader;
    };
    bootWindow.grafanaBootData = {
      assets: { dark: "", light: "" },
      settings: {
        buildInfo: { version: "13.2.3", env: "production" },
        appUrl: location.origin + "/",
        appSubUrl: "",
        featureToggles: {},
        liveEnabled: true,
        defaultDatasource: "metricspanel",
      },
      user: {
        id: 1,
        orgId: 1,
        login: "metricspanel",
        isSignedIn: true,
        theme: "dark",
        language: getLocale() === "zh" ? "zh-CN" : "en-US",
      },
      navTree: [],
    };
    const [runtime, ui] = await Promise.all([
      import("@grafana/runtime"),
      import("@grafana/ui"),
    ]);
    runtime.config.theme2 = Data.createTheme({ colors: { mode: "dark" } });
    runtime.config.buildInfo.version = "13.2.3";
    registerOptionEditors();
    runtime.setBackendSrv(backendService());
    runtime.setGrafanaLiveSrv(createLiveService());
    runtime.setTemplateSrv({
      getVariables: () =>
        Object.entries(variableValues).map(([name, value]) => ({
          name,
          type: "custom",
          current: { value },
        })) as Data.TypedVariableModel[],
      replace: (text = "", scoped, format) => {
        const values = { ...variableValues };
        for (const [name, v] of Object.entries(scoped || {}))
          if (v) values[name] = v.value;
        if (typeof format === "string")
          return interpolate(
            text.replace(/\$(\w+)(?!\w)/g, `\$\{$1:${format}\}`),
            values,
            variableRange,
          );
        return interpolate(text, values, variableRange);
      },
      containsTemplate: (text = "") => /\$(?:\w|\{)/.test(text),
      updateTimeRange: () => {},
    });
    await import("systemjs/dist/system.js");
    await import("systemjs/dist/extras/amd.js");
    await import("systemjs/dist/extras/module-types.js");
    const loader = bootWindow.System;
    const dependencies: Record<string, Record<string, unknown>> = {
      react: React,
      "react-dom": ReactDOM,
      "react/jsx-runtime": JSXRuntime,
      "@grafana/data": Data,
      "@grafana/ui": ui,
      "@grafana/runtime": runtime,
      "@emotion/css": EmotionCSS,
      "@emotion/react": EmotionReact,
      rxjs: RxJS,
      "rxjs/operators": RxOperators,
      moment: { default: moment, __useDefault: true },
      i18next: { default: i18next, ...i18next },
    };
    const imports: Record<string, string> = {};
    for (const [name, exports] of Object.entries(dependencies)) {
      const key = `metricspanel:${name}`;
      imports[name] = key;
      loader.set(key, exports);
    }
    loader.addImportMap({ imports });
    runtime.setPluginImportUtils({
      importPanelPlugin: loadPanelPlugin,
      getPanelPluginFromCache: (id) => panelCache.get(id),
    });
    const service: DataSourceSrv = {
      get: async (ref, scoped) => {
        await reloadSources();
        const id =
          typeof ref === "string"
            ? runtime.getTemplateSrv().replace(ref, scoped)
            : ref?.uid;
        const settings =
          sources.find((s) => s.uid === id || s.name === id) ||
          (id ? undefined : sources.find((s) => s.isDefault));
        if (!settings) throw new Error(`Datasource not found: ${id}`);
        const version = settings.version || 0;
        const cached = datasourceCache.get(settings.uid);
        if (cached?.version === version) return cached.instance;
        let instance: Data.DataSourceApi;
        if (settings.type === "prometheus") {
          instance = new LocalPrometheus(settings);
        } else {
          const module = await loadPlugin(settings.type);
          const plugin =
            module.plugin as Data.DataSourcePlugin<Data.DataSourceApi>;
          if (!plugin?.DataSourceClass)
            throw new Error(`Datasource class missing: ${settings.type}`);
          instance = new plugin.DataSourceClass(settings);
        }
        datasourceCache.set(settings.uid, { version, instance });
        return instance;
      },
      getList: (filters) =>
        sources.filter(
          (s) =>
            (!filters?.type ||
              (Array.isArray(filters.type)
                ? filters.type.includes(s.type)
                : filters.type === s.type)) &&
            (!filters?.pluginId || filters.pluginId === s.type) &&
            (!filters?.filter || filters.filter(s)),
        ),
      getInstanceSettings: (ref, scoped) => {
        const id =
          typeof ref === "string"
            ? runtime.getTemplateSrv().replace(ref, scoped)
            : ref?.uid;
        return (
          sources.find((s) => s.uid === id || s.name === id) ||
          (id ? undefined : sources.find((s) => s.isDefault))
        );
      },
      reload: reloadSources,
      registerRuntimeDataSource: (entry) => {
        datasourceCache.set(entry.dataSource.uid, {
          version: 0,
          instance: entry.dataSource,
        });
      },
    };
    runtime.setDataSourceSrv(service);
    await reloadSources();
    return runtime;
  })().catch((error) => {
    initialized = undefined;
    throw error;
  });
  return initialized;
}
async function reloadSources() {
  const [items, plugins] = await Promise.all([
    api<DataSourceSettings[]>("/api/datasources"),
    api<InstalledPlugin[]>("/plugins"),
  ]);
  sources = items.map((ds) => ({
    ...ds,
    meta:
      ds.type === "prometheus"
        ? ({
            id: "prometheus",
            name: "Prometheus",
            type: Data.PluginType.datasource,
            info: { version: "13.2.3" },
          } as Data.PluginMeta)
        : plugins.find((p) => p.id === ds.type)
          ? pluginMeta(plugins.find((p) => p.id === ds.type)!)
          : ds.meta,
    jsonData: ds.jsonData || {},
  }));
}
export function setPluginVariables(values: InterpolationValues, range: string) {
  variableValues = values;
  variableRange = range;
}
export async function loadPlugin(id: string): Promise<Record<string, unknown>> {
  const runtime = await init();
  const installed = await api<InstalledPlugin>(
    `/plugins/${encodeURIComponent(id)}`,
  );
  if (!installed.enabled) throw new Error(`Plugin is disabled: ${id}`);
  const key = id + ":" + installed.sha256;
  let pending = moduleCache.get(key);
  if (!pending) {
    pending = (async () => {
      await api("/plugins/assets-session", { method: "POST" });
      const loader = (window as unknown as { System: Loader }).System;
      let module = await loader.import(
        `${location.origin}/public/plugins/${id}/module.js?sha=${installed.sha256}`,
      );
      if (!module.plugin && module.default)
        module = (await module.default) as Record<string, unknown>;
      if (!module?.plugin) throw new Error(`Plugin export missing: ${id}`);
      const plugin = module.plugin as Data.GrafanaPlugin;
      plugin.meta = pluginMeta(installed);
      if (installed.type === "panel") {
        panelCache.set(id, plugin as unknown as Data.PanelPlugin);
        runtime.config.panels[id] = plugin.meta as Data.PanelPluginMeta;
      }
      return module;
    })().catch((error) => {
      moduleCache.delete(key);
      throw error;
    });
    moduleCache.set(key, pending);
  }
  return pending;
}
export async function loadPanelPlugin(id: string): Promise<Data.PanelPlugin> {
  const module = await loadPlugin(id);
  const plugin = module.plugin as unknown as Data.PanelPlugin;
  if (!plugin.panel || !["function", "object"].includes(typeof plugin.panel))
    throw new Error(`Plugin is not a React panel: ${id}`);
  return plugin;
}
export async function getPluginDatasource(uid: string) {
  const runtime = await init();
  return runtime.getDataSourceSrv().get(uid);
}
export async function sdkRuntime() {
  return init();
}

class LocalPrometheus extends Data.DataSourceApi {
  constructor(private settings: DataSourceSettings) {
    super(settings);
  }
  query(
    request: Data.DataQueryRequest,
  ): RxJS.Observable<Data.DataQueryResponse> {
    return RxJS.defer(async () => {
      const runtime = await init();
      return new runtime.DataSourceWithBackend(this.settings);
    }).pipe(
      RxOperators.switchMap((adapter) => {
        const values = { ...variableValues };
        for (const [name, entry] of Object.entries(request.scopedVars || {}))
          if (entry) values[name] = entry.value;
        const rawFrom = request.range.raw.from;
        const range =
          typeof rawFrom === "string" && rawFrom.startsWith("now-")
            ? rawFrom.slice(4)
            : variableRange;
        return adapter.query({
          ...request,
          targets: request.targets.map((target) => {
            const query = target as Data.DataQuery & {
              expr?: string;
              legendFormat?: string;
            };
            return {
              ...query,
              expr: interpolate(query.expr || "", values, range),
              legendFormat: query.legendFormat
                ? interpolate(query.legendFormat, values, range)
                : undefined,
            };
          }),
        });
      }),
    );
  }
  async testDatasource() {
    await api(`/api/datasources/uid/${encodeURIComponent(this.uid)}/health`);
    return { status: "success", message: "Data source is working" };
  }
}
