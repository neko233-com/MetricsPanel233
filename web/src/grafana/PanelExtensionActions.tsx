import { useMemo, useState } from "react";
import { MoreHorizontal } from "lucide-react";
import {
  LoadingState,
  PluginExtensionPoints,
  type DataFrame,
  type PluginExtensionPanelContext,
} from "@grafana/data";
import { type Panel, type InterpolationValues } from "../api";
import { t } from "../i18n";
import {
  resolveTimeRange,
  type ResolvedTimeRange,
  type TimeSelection,
} from "./time-range";
import { ExtensionLink } from "./ExtensionHost";
import { useExtensionLinks } from "./extensions";

export function PanelExtensionActions({
  panel,
  values,
  range,
  dashboard,
  frames,
  state = LoadingState.Done,
  queryRange,
}: {
  panel: Panel;
  values: InterpolationValues;
  range: TimeSelection;
  dashboard: PluginExtensionPanelContext["dashboard"];
  frames: DataFrame[];
  state?: LoadingState;
  queryRange?: ResolvedTimeRange;
}) {
  const [open, setOpen] = useState(false);
  const context = useMemo<PluginExtensionPanelContext>(() => {
    const resolved = queryRange || resolveTimeRange(range);
    return {
      id: panel.config?.id || 0,
      title: panel.title,
      pluginId: panel.config?.type || panel.visualization || "timeseries",
      timeRange: resolved.sdk.raw,
      timeZone: resolved.timezone,
      dashboard,
      targets: (panel.config?.targets || []).map((target, index) => ({
        ...target,
        refId: target.refId || String.fromCharCode(65 + index),
        datasource:
          typeof target.datasource === "string"
            ? { uid: target.datasource }
            : target.datasource,
      })),
      scopedVars: Object.fromEntries(
        Object.entries(values).map(([name, value]) => [
          name,
          { text: value, value },
        ]),
      ),
      data: {
        state,
        series: frames,
        timeRange: resolved.sdk,
      },
    };
  }, [panel, values, range, dashboard, frames, state, queryRange]);
  const { links } = useExtensionLinks({
    extensionPointId: PluginExtensionPoints.DashboardPanelMenu,
    context,
    limitPerPlugin: 5,
  });
  if (!links.length) return null;
  return (
    <div className="panel-extension-actions">
      <button
        className="icon-button"
        aria-label={`${t("Panel actions")} ${panel.title}`}
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        <MoreHorizontal size={18} />
      </button>
      {open && (
        <div className="extension-menu" role="menu" aria-label={panel.title}>
          {links.map((link) => (
            <ExtensionLink
              key={link.id}
              asMenuItem
              link={link}
              close={() => setOpen(false)}
            />
          ))}
        </div>
      )}
    </div>
  );
}
