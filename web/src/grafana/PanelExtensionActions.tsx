import { useMemo, useState } from "react";
import { MoreHorizontal } from "lucide-react";
import {
  LoadingState,
  PluginExtensionPoints,
  dateTime,
  type DataFrame,
  type PluginExtensionPanelContext,
} from "@grafana/data";
import { type Panel, type InterpolationValues } from "../api";
import { t } from "../i18n";
import { rangeMilliseconds } from "../api";
import { ExtensionLink } from "./ExtensionHost";
import { useExtensionLinks } from "./extensions";

export function PanelExtensionActions({
  panel,
  values,
  range,
  dashboard,
  frames,
  state = LoadingState.Done,
}: {
  panel: Panel;
  values: InterpolationValues;
  range: string;
  dashboard: PluginExtensionPanelContext["dashboard"];
  frames: DataFrame[];
  state?: LoadingState;
}) {
  const [open, setOpen] = useState(false);
  const context = useMemo<PluginExtensionPanelContext>(() => {
    const end = Date.now();
    return {
      id: panel.config?.id || 0,
      title: panel.title,
      pluginId: panel.config?.type || panel.visualization || "timeseries",
      timeRange: { from: "now-" + range, to: "now" },
      timeZone: "browser",
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
        timeRange: {
          from: dateTime(end - rangeMilliseconds(range)),
          to: dateTime(end),
          raw: { from: "now-" + range, to: "now" },
        },
      },
    };
  }, [panel, values, range, dashboard, frames, state]);
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
