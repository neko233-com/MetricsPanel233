import * as React from "react";
import {
  AppPlugin,
  PluginContextProvider,
  PluginExtensionTypes,
  ThemeContext,
  createTheme,
  dateTime,
  isDateTime,
  type PluginExtensionLink,
  type PluginExtensionComponent,
  type PluginExtensionFunction,
  type ComponentTypeWithExtensionMeta,
  type PluginExtensionEventHelpers,
  type PluginExtensionOpenModalOptions,
} from "@grafana/data";
import type * as Runtime from "@grafana/runtime";
import { Observable } from "rxjs";
import { api, message } from "../api";
import { t } from "../i18n";
import type { InstalledPlugin } from "./plugin-runtime";

type Provider = {
  hash: string;
  app: AppPlugin;
  metaJSON: string;
  components: Map<
    string,
    ComponentTypeWithExtensionMeta<Record<string, unknown>>
  >;
};
type Snapshot = {
  revision: number;
  discovering: boolean;
  pending: Set<string>;
};
type Options = {
  extensionPointId: string;
  limitPerPlugin?: number;
  context?: object;
};
type Overlay = {
  provider: string;
  hash: string;
  title: string;
  body: React.ComponentType<Record<string, unknown>>;
  props: Record<string, unknown>;
  width?: string | number;
  height?: string | number;
};
const providers = new Map<string, Provider>();
let installed = new Map<string, InstalledPlugin>();
const demands = new Set<string>();
const listeners = new Set<() => void>();
const overlayListeners = new Set<() => void>();
let snapshot: Snapshot = { revision: 0, discovering: true, pending: new Set() };
let modal: Overlay | null = null,
  sidebar: Overlay | null = null,
  failure = "";
let overlaySnapshot: {
  modal: Overlay | null;
  sidebar: Overlay | null;
  failure: string;
} = { modal, sidebar, failure };
let loader: ((id: string) => Promise<Record<string, unknown>>) | undefined;
let refreshPending: Promise<void> | undefined,
  timer: ReturnType<typeof setInterval> | undefined;
let hooksInstalled = false;
const theme = createTheme({ colors: { mode: "dark" } });

function publish() {
  snapshot = { ...snapshot, revision: snapshot.revision + 1 };
  listeners.forEach((listener) => listener());
}
function publishOverlays() {
  overlaySnapshot = { modal, sidebar, failure };
  overlayListeners.forEach((listener) => listener());
}
function active(id: string, hash: string) {
  const known = installed.get(id);
  return (
    providers.get(id)?.hash === hash &&
    (!known || (known.enabled && known.sha256 === hash))
  );
}
function listTargets(targets: string | string[]) {
  return Array.isArray(targets) ? targets : [targets];
}
function wants(plugin: InstalledPlugin, id: string) {
  const metadata = plugin.metadata.extensions;
  return (
    !!metadata &&
    (metadata.exposedComponents?.some((entry) => entry.id === id) ||
      [
        ...(metadata.addedLinks || []),
        ...(metadata.addedComponents || []),
        ...(metadata.addedFunctions || []),
      ].some((entry) => listTargets(entry.targets).includes(id)))
  );
}
function needed() {
  const ids = new Set<string>();
  for (const plugin of installed.values()) {
    if (
      plugin.enabled &&
      plugin.type === "app" &&
      [...demands].some((id) => wants(plugin, id))
    )
      ids.add(plugin.id);
  }
  // Exposed component dependencies may be used by a provider while initializing.
  for (const id of ids) {
    for (const component of installed.get(id)?.metadata.dependencies?.extensions
      ?.exposedComponents || []) {
      const owner = component.split("/")[0];
      if (installed.get(owner)?.enabled) ids.add(owner);
    }
  }
  return [...ids].sort();
}
function loading(id: string) {
  return (
    snapshot.discovering ||
    [...snapshot.pending].some((owner) => {
      const plugin = installed.get(owner);
      return plugin && wants(plugin, id);
    })
  );
}

export async function refreshExtensions() {
  if (refreshPending) return refreshPending;
  if (!loader) return;
  refreshPending = (async () => {
    try {
      const items = await api<InstalledPlugin[]>("/plugins");
      installed = new Map(items.map((plugin) => [plugin.id, plugin]));
      for (const [id, entry] of providers) {
        if (
          !installed.get(id)?.enabled ||
          installed.get(id)?.sha256 !== entry.hash
        )
          providers.delete(id);
      }
      if (modal && !active(modal.provider, modal.hash)) modal = null;
      if (sidebar && !active(sidebar.provider, sidebar.hash)) sidebar = null;
      publishOverlays();
      snapshot = {
        ...snapshot,
        discovering: false,
        pending: new Set(needed().filter((id) => !providers.has(id))),
      };
      publish();
      const queue = needed();
      await Promise.all(
        Array.from({ length: Math.min(4, queue.length) }, async () => {
          while (queue.length) {
            const id = queue.shift()!;
            try {
              let deadline: ReturnType<typeof setTimeout> | undefined;
              const module = await Promise.race([
                loader!(id),
                new Promise<never>((_, reject) => {
                  deadline = setTimeout(
                    () =>
                      reject(
                        new Error(
                          "Extension provider load exceeded 20 seconds",
                        ),
                      ),
                    20000,
                  );
                }),
              ]).finally(() => {
                if (deadline) clearTimeout(deadline);
              });
              const metadata = module.metricspanelInstalled as InstalledPlugin;
              if (
                installed.get(id)?.enabled &&
                installed.get(id)?.sha256 === metadata.sha256
              )
                registerAppExtensions(metadata, module.plugin as AppPlugin);
            } catch (error) {
              console.warn(`Extension provider ${id}: ${message(error)}`);
            } finally {
              snapshot.pending.delete(id);
              publish();
            }
          }
        }),
      );
    } catch (error) {
      installed.clear();
      providers.clear();
      snapshot = { ...snapshot, discovering: false, pending: new Set() };
      modal = null;
      sidebar = null;
      publishOverlays();
      publish();
      console.warn(`Extension discovery: ${message(error)}`);
    }
  })().finally(() => {
    refreshPending = undefined;
  });
  return refreshPending;
}
function subscribe(listener: () => void) {
  listeners.add(listener);
  if (!timer) {
    void refreshExtensions();
    timer = setInterval(() => {
      if (!document.hidden) void refreshExtensions();
    }, 5000);
  }
  return () => {
    listeners.delete(listener);
    if (!listeners.size && timer) {
      clearInterval(timer);
      timer = undefined;
    }
  };
}
function useRegistry(id: string) {
  React.useEffect(() => {
    if (!demands.has(id)) {
      if (demands.size >= 1024)
        throw new Error("Extension point limit exceeded");
      demands.add(id);
      void refreshExtensions();
    }
  }, [id]);
  return React.useSyncExternalStore(subscribe, () => snapshot);
}

// Protect shared panel/plugin data without freezing the host's own objects.
// Date and Grafana DateTime instances are cloned so their methods stay usable.
export function readOnlyContext<T extends object>(value: T): T {
  const cache = new WeakMap<object, object>();
  const wrap = (input: unknown): unknown => {
    if (!input || typeof input !== "object") return input;
    if (isDateTime(input)) return dateTime(input);
    if (input instanceof Date) return new Date(input.getTime());
    const cached = cache.get(input);
    if (cached) return cached;
    const target = Array.isArray(input)
      ? [...input]
      : Object.create(
          Object.getPrototypeOf(input),
          Object.fromEntries(
            Reflect.ownKeys(input).map((key) => [
              key,
              {
                ...Object.getOwnPropertyDescriptor(input, key),
                configurable: true,
              },
            ]),
          ),
        );
    const proxy = new Proxy(target, {
      get: (target, key, receiver) => wrap(Reflect.get(target, key, receiver)),
      set: () => false,
      deleteProperty: () => false,
      defineProperty: () => false,
      setPrototypeOf: () => false,
    });
    cache.set(input, proxy);
    return proxy;
  };
  return wrap(value) as T;
}
class ExtensionBoundary extends React.Component<
  { children: React.ReactNode },
  { error: string }
> {
  state = { error: "" };
  static getDerivedStateFromError(error: unknown) {
    return { error: message(error) };
  }
  render() {
    return this.state.error ? (
      <p role="alert" className="form-error">
        {t("Extension failed")}: {this.state.error}
      </p>
    ) : (
      this.props.children
    );
  }
}
function componentFor(
  entry: Provider,
  id: string,
  Component: React.ComponentType<Record<string, unknown>>,
  title: string,
  description: string,
  cache = true,
) {
  let wrapped = cache ? entry.components.get(id) : undefined;
  if (!wrapped) {
    wrapped = ((props: Record<string, unknown>) => {
      React.useSyncExternalStore(subscribe, () => snapshot);
      if (!active(entry.app.meta.id, entry.hash)) return null;
      return (
        <ThemeContext.Provider value={theme}>
          <PluginContextProvider meta={entry.app.meta}>
            <ExtensionBoundary>
              <Component {...props} />
            </ExtensionBoundary>
          </PluginContextProvider>
        </ThemeContext.Provider>
      );
    }) as ComponentTypeWithExtensionMeta<Record<string, unknown>>;
    wrapped.meta = {
      id,
      pluginId: entry.app.meta.id,
      type: PluginExtensionTypes.component,
      title,
      description,
    };
    if (cache) entry.components.set(id, wrapped);
  }
  return wrapped;
}
export function registerAppExtensions(plugin: InstalledPlugin, app: AppPlugin) {
  if (!plugin.enabled) return;
  const known = installed.get(plugin.id);
  if (known && (!known.enabled || known.sha256 !== plugin.sha256)) return;
  const metaJSON = JSON.stringify(app.meta);
  const previous = providers.get(plugin.id);
  if (previous?.hash === plugin.sha256 && previous.app === app) {
    if (previous.metaJSON !== metaJSON) {
      previous.metaJSON = metaJSON;
      publish();
    }
    return;
  }
  providers.set(plugin.id, {
    hash: plugin.sha256,
    app,
    metaJSON,
    components: new Map(),
  });
  publish();
}
function subset(_options?: Options) {
  return [...providers.entries()]
    .filter(([id, entry]) => active(id, entry.hash))
    .sort(([a], [b]) => a.localeCompare(b));
}
function limit(options: Options) {
  return options.limitPerPlugin && options.limitPerPlugin > 0
    ? options.limitPerPlugin
    : Infinity;
}
function validPath(path: unknown, owner: string): path is string {
  if (typeof path !== "string" || !path.startsWith("/a/")) return false;
  const url = new URL(path, location.origin);
  return (
    url.origin === location.origin &&
    (url.pathname === `/a/${owner}` || url.pathname.startsWith(`/a/${owner}/`))
  );
}
function overlays(
  entry: Provider,
  context: object,
  extensionPointId: string,
): PluginExtensionEventHelpers {
  const provider = entry.app.meta.id,
    hash = entry.hash;
  const openSidebar = (title: string, props: Record<string, unknown> = {}) => {
    if (!active(provider, hash)) return;
    const config = entry.app.addedComponentConfigs.slice(0, 1024).find(
      (item) =>
        item.title === title &&
        listTargets(item.targets).includes(
          "grafana/extension-sidebar/v0-alpha",
        ),
    );
    if (!config) throw new Error(`Sidebar component not found: ${title}`);
    sidebar = {
      provider,
      hash,
      title,
      body: componentFor(
        entry,
        `sidebar:${title}`,
        config.component,
        title,
        config.description,
      ),
      props,
    };
    publishOverlays();
  };
  return {
    context,
    extensionPointId,
    openModal: (options: PluginExtensionOpenModalOptions) => {
      if (!active(provider, hash)) return;
      modal = {
        provider,
        hash,
        title: options.title,
        body: componentFor(
          entry,
          `modal:${options.title}`,
          options.body,
          options.title,
          "",
          false,
        ),
        props: {},
        width: options.width,
        height: options.height,
      };
      publishOverlays();
    },
    openSidebar,
    closeSidebar: () => {
      sidebar = null;
      publishOverlays();
    },
    toggleSidebar: (title, props) => {
      if (sidebar?.provider === provider && sidebar.title === title) {
        sidebar = null;
        publishOverlays();
      } else openSidebar(title, props);
    },
  };
}
function selectLinks(options: Options): PluginExtensionLink[] {
  const result: PluginExtensionLink[] = [],
    context = readOnlyContext(options.context || {});
  for (const [pluginId, entry] of subset(options)) {
    let count = 0;
    for (const [index, config] of entry.app.addedLinkConfigs
      .slice(0, 1024)
      .entries()) {
      if (
        count >= limit(options) ||
        !listTargets(config.targets).includes(options.extensionPointId)
      )
        continue;
      try {
        if (
          !config.title ||
          (config.configure && typeof config.configure !== "function")
        )
          continue;
        const overrides = config.configure?.(context);
        if (config.configure && !overrides) continue;
        if (overrides && "then" in overrides) {
          // configure is synchronous; discard invalid asynchronous results safely.
          void Promise.resolve(overrides).catch(() => {});
          continue;
        }
        const effective = { ...config };
        for (const key of [
          "title",
          "description",
          "path",
          "icon",
          "category",
          "group",
          "openInNewTab",
        ] as const) {
          if (overrides && overrides[key] !== undefined)
            Object.assign(effective, { [key]: overrides[key] });
        }
        if (
          typeof effective.title !== "string" ||
          !effective.title ||
          (effective.path && !validPath(effective.path, pluginId))
        )
          continue;
        if (!effective.path && typeof config.onClick !== "function") continue;
        let path = effective.path;
        if (path) {
          const url = new URL(path, location.origin);
          url.searchParams.set("uel_pid", pluginId);
          url.searchParams.set("uel_epid", options.extensionPointId);
          path = url.pathname + url.search + url.hash;
        }
        const onClick = config.onClick
          ? (event?: React.MouseEvent) => {
              if (!active(pluginId, entry.hash)) return;
              try {
                const returned: unknown = config.onClick!(
                  event,
                  overlays(entry, context, options.extensionPointId),
                );
                Promise.resolve(returned).catch((error) => {
                  failure = message(error);
                  publishOverlays();
                });
              } catch (error) {
                failure = message(error);
                publishOverlays();
              }
            }
          : undefined;
        result.push({
          id: `${pluginId}:link:${index}:${options.extensionPointId}`,
          type: PluginExtensionTypes.link,
          pluginId,
          title: effective.title,
          description: effective.description || "",
          path,
          onClick,
          icon: effective.icon,
          group: effective.group,
          category: effective.category,
          openInNewTab: effective.openInNewTab,
        });
        count++;
      } catch (error) {
        console.warn(
          `Extension link ${pluginId}/${config.title}: ${message(error)}`,
        );
      }
    }
  }
  return result;
}
function selectComponents(options: Options): PluginExtensionComponent[] {
  const result: PluginExtensionComponent[] = [];
  for (const [pluginId, entry] of subset(options)) {
    let count = 0;
    for (const [index, config] of entry.app.addedComponentConfigs
      .slice(0, 1024)
      .entries()) {
      if (
        count >= limit(options) ||
        !config.title ||
        !config.component ||
        !listTargets(config.targets).includes(options.extensionPointId)
      )
        continue;
      const id = `${pluginId}:component:${index}:${options.extensionPointId}`;
      result.push({
        id,
        type: PluginExtensionTypes.component,
        pluginId,
        title: config.title,
        description: config.description,
        component: componentFor(
          entry,
          id,
          config.component,
          config.title,
          config.description,
        ),
      });
      count++;
    }
  }
  return result;
}
function useLinks(options: Options) {
  const state = useRegistry(options.extensionPointId);
  return React.useMemo(
    () => ({
      isLoading: loading(options.extensionPointId),
      links: selectLinks(options),
    }),
    [state, options.extensionPointId, options.limitPerPlugin, options.context],
  );
}
function useComponents<Props extends object>(options: Options) {
  const state = useRegistry(options.extensionPointId);
  return React.useMemo(
    () => ({
      isLoading: loading(options.extensionPointId),
      components: selectComponents(options).map(
        (item) =>
          item.component as unknown as ComponentTypeWithExtensionMeta<Props>,
      ),
    }),
    [state, options.extensionPointId, options.limitPerPlugin],
  );
}
function useComponent<Props extends object>(id: string) {
  const state = useRegistry(id);
  return React.useMemo(() => {
    for (const [, entry] of subset({ extensionPointId: id })) {
      const config = entry.app.exposedComponentConfigs.slice(0, 1024).find(
        (item) => item.id === id && item.id.startsWith(entry.app.meta.id + "/"),
      );
      if (config)
        return {
          isLoading: loading(id),
          component: componentFor(
            entry,
            id,
            config.component,
            config.title,
            config.description || "",
          ) as React.ComponentType<Props>,
        };
    }
    return { isLoading: loading(id), component: null };
  }, [state, id]);
}
function useFunctions<Signature>(options: Options) {
  const state = useRegistry(options.extensionPointId);
  return React.useMemo(() => {
    const functions: PluginExtensionFunction<Signature>[] = [];
    for (const [pluginId, entry] of subset(options)) {
      let count = 0;
      for (const [index, config] of entry.app.addedFunctionConfigs
        .slice(0, 1024)
        .entries()) {
        if (
          count >= limit(options) ||
          !config.title ||
          typeof config.fn !== "function" ||
          !listTargets(config.targets).includes(options.extensionPointId)
        )
          continue;
        const invoke = (...args: unknown[]) => {
          if (!active(pluginId, entry.hash))
            throw new Error("Extension provider is disabled or replaced");
          return (config.fn as (...args: unknown[]) => unknown)(...args);
        };
        functions.push({
          id: `${pluginId}:function:${index}:${options.extensionPointId}`,
          pluginId,
          type: PluginExtensionTypes.function,
          title: config.title,
          description: config.description || "",
          fn: invoke as Signature,
        });
        count++;
      }
    }
    return { isLoading: loading(options.extensionPointId), functions };
  }, [state, options.extensionPointId, options.limitPerPlugin]);
}
function observe<T>(
  options: Options,
  selector: (options: Options) => T,
): Observable<T> {
  return new Observable((subscriber) => {
    if (demands.size >= 1024 && !demands.has(options.extensionPointId)) {
      subscriber.error(new Error("Extension point limit exceeded"));
      return;
    }
    demands.add(options.extensionPointId);
    const emit = () => {
      if (!loading(options.extensionPointId))
        subscriber.next(selector(options));
    };
    const detach = subscribe(emit);
    void refreshExtensions();
    emit();
    return detach;
  });
}
export const observableExtensionLinks: typeof Runtime.getObservablePluginLinks =
  (options) => observe(options, selectLinks);
export const observableExtensionComponents: typeof Runtime.getObservablePluginComponents =
  (options) => observe(options, selectComponents);
export function installExtensionServices(
  runtime: typeof Runtime,
  load: NonNullable<typeof loader>,
) {
  loader = load;
  if (hooksInstalled) return;
  runtime.setPluginLinksHook(useLinks);
  runtime.setPluginComponentsHook(useComponents);
  runtime.setPluginComponentHook(useComponent);
  runtime.setPluginFunctionsHook(useFunctions);
  hooksInstalled = true;
  if (listeners.size) void refreshExtensions();
}
export function useExtensionOverlays() {
  return React.useSyncExternalStore(
    (listener) => {
      overlayListeners.add(listener);
      return () => {
        overlayListeners.delete(listener);
      };
    },
    () => overlaySnapshot,
  );
}
export function dismissExtensionModal() {
  modal = null;
  publishOverlays();
}
export function dismissExtensionSidebar() {
  sidebar = null;
  publishOverlays();
}
export function dismissExtensionFailure() {
  failure = "";
  publishOverlays();
}
export {
  useLinks as useExtensionLinks,
  useComponents as useExtensionComponents,
};
