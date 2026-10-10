import { AppEvents, EventBusSrv, type BusEvent } from "@grafana/data";
import type * as Runtime from "@grafana/runtime";
import { resolveTimeRange, type TimeSelection } from "./time-range";

export type NoticeSeverity = "success" | "warning" | "error" | "info";
type Host = {
  refresh: () => void | Promise<void>;
  notify: (text: string, severity: NoticeSeverity) => void;
};
const appEvents = new EventBusSrv();
const hosts = new Set<Host>();
const refreshes = new Map<Host, { dirty: boolean }>();
const panels = new Set<EventBusSrv>();
const panelSubscriptions = new Map<EventBusSrv, { unsubscribe(): void }[]>();
const forwarding = new Set<BusEvent>();
const sources = new Map<BusEvent, EventBusSrv>();
let runtime: typeof Runtime | undefined;
let installed = false;

function notify(payload: unknown, severity: NoticeSeverity) {
  if (!Array.isArray(payload) || typeof payload[0] !== "string") return;
  const detail = payload[1] instanceof Error ? payload[1].message : payload[1];
  const text = [payload[0], typeof detail === "string" ? detail : ""]
    .filter(Boolean)
    .join(": ")
    .slice(0, 8192);
  if (text) hosts.forEach((host) => host.notify(text, severity));
}
function refresh() {
  hosts.forEach((host) => {
    const pending = refreshes.get(host);
    if (pending) {
      pending.dirty = true;
      return;
    }
    const state = { dirty: false };
    refreshes.set(host, state);
    void Promise.resolve()
      .then(async () => {
        do {
          state.dirty = false;
          if (!hosts.has(host)) return;
          await host.refresh();
        } while (state.dirty && hosts.has(host));
      })
      .catch((error: unknown) => notify(["Refresh failed", error], "error"))
      .finally(() => {
        refreshes.delete(host);
      });
  });
}
function forward(event: BusEvent) {
  forwarding.add(event);
  try {
    panels.forEach((panel) => {
      if (panel !== sources.get(event)) panel.publish(event);
    });
  } finally {
    forwarding.delete(event);
  }
}
function listenPanel(panel: EventBusSrv) {
  if (!runtime || panelSubscriptions.has(panel)) return;
  const relay = (event: BusEvent) => {
    if (forwarding.has(event)) return;
    sources.set(event, panel);
    try {
      appEvents.publish(event);
    } finally {
      sources.delete(event);
    }
  };
  panelSubscriptions.set(panel, [
    panel.subscribe(runtime.RefreshEvent, relay),
    panel.subscribe(runtime.TimeRangeUpdatedEvent, relay),
    panel.subscribe(runtime.ThemeChangedEvent, relay),
  ]);
}
export function installAppEvents(sdk: typeof Runtime) {
  runtime = sdk;
  sdk.setAppEvents(appEvents);
  if (installed) return;
  installed = true;
  appEvents.subscribe(sdk.RefreshEvent, (event) => {
    refresh();
    forward(event);
  });
  appEvents.subscribe(sdk.TimeRangeUpdatedEvent, (event) => {
    forward(event);
  });
  appEvents.subscribe(sdk.ThemeChangedEvent, (event) => {
    forward(event);
  });
  appEvents.on(AppEvents.alertSuccess, (payload) => notify(payload, "success"));
  appEvents.on(AppEvents.alertWarning, (payload) => notify(payload, "warning"));
  appEvents.on(AppEvents.alertError, (payload) => notify(payload, "error"));
  appEvents.on(AppEvents.alertInfo, (payload) => notify(payload, "info"));
  panels.forEach(listenPanel);
}
export function connectAppEvents(host: Host) {
  hosts.add(host);
  return () => {
    hosts.delete(host);
  };
}
export function connectPanelEvents(panel: EventBusSrv) {
  panels.add(panel);
  listenPanel(panel);
  return () => {
    panels.delete(panel);
    panelSubscriptions
      .get(panel)
      ?.forEach((subscription) => subscription.unsubscribe());
    panelSubscriptions.delete(panel);
    // A panel owns this bus; its teardown must never clear the application bus.
    panel.removeAllListeners();
  };
}
export function refreshWorkspace() {
  if (runtime) appEvents.publish(new runtime.RefreshEvent());
  else refresh();
}
export function updateWorkspaceTimeRange(range: TimeSelection) {
  if (!runtime) return;
  const payload = resolveTimeRange(range).sdk;
  appEvents.publish(new runtime.TimeRangeUpdatedEvent(payload));
}
