import { FieldType, dataFrameToJSON, type DataFrame } from "@grafana/data";
import {
  firstValueFrom,
  filter,
  timeout,
  takeUntil,
  fromEvent,
  EMPTY,
} from "rxjs";
import {
  dashboardUID,
  queryPanel,
  type Dashboard,
  type InterpolationValues,
  type Panel,
} from "../api";
import { resolveTimeRange, type TimeSelection } from "./time-range";
import { watchFrames, type GrafanaConfig } from "./engine";

export type SnapshotPanel = {
  panel: Panel;
  values: InterpolationValues;
  x: number;
  y: number;
  w: number;
  h: number;
};

// All panels use one fixed clock. Live queries capture their first real frame
// and release the subscription; no partial snapshot is saved on query failure.
export async function captureDashboardSnapshot(
  dashboard: Dashboard,
  panels: SnapshotPanel[],
  name: string,
  range: TimeSelection,
  signal: AbortSignal,
) {
  if (signal.aborted) throw new DOMException("Aborted", "AbortError");
  if (panels.length > 500)
    throw new Error("Snapshot exceeds 500 panels. Select fewer repeat values.");
  const sampledAt = Date.now();
  const resolved = resolveTimeRange(range, sampledAt);
  const fixed = {
    from: resolved.start,
    to: resolved.end,
    timezone: resolved.timezone,
  };
  const saved: Record<string, unknown>[] = new Array(panels.length);
  let next = 0;
  await Promise.all(
    Array.from({ length: Math.min(4, panels.length) }, async () => {
      for (;;) {
        const index = next++;
        if (index >= panels.length) return;
        const { panel, values, x, y, w, h } = panels[index];
        const unit =
          panel.unit === "seconds"
            ? "s"
            : panel.unit === "count"
              ? "short"
              : panel.unit || undefined;
        if (signal.aborted) throw new DOMException("Aborted", "AbortError");
        let frames: DataFrame[] = [];
        let panelRange = fixed;
        if (panel.visualization !== "text" && panel.visualization !== "row") {
          if (dashboard.grafana || panel.config?.targets?.length) {
            const result = await firstValueFrom(
              watchFrames(panel, values, range, EMPTY, {
                dashboardUID: dashboardUID(dashboard),
                sampledAt,
              }).pipe(
                filter(
                  (update) =>
                    !update.loading &&
                    (!update.streaming || update.frames.length > 0),
                ),
                timeout(30000),
                takeUntil(fromEvent(signal, "abort")),
              ),
            );
            if (result.error)
              throw new Error(`${panel.title}: ${result.error}`);
            frames = result.frames;
            if (result.timeRange)
              panelRange = {
                from: result.timeRange.start,
                to: result.timeRange.end,
                timezone: result.timeRange.timezone,
              };
          } else {
            const result = await queryPanel(panel, fixed, signal);
            frames =
              panel.visualization === "table"
                ? [
                    {
                      refId: "A",
                      length: result.series.length,
                      fields: [
                        {
                          name: "Labels",
                          type: FieldType.string,
                          config: {},
                          values: result.series.map((series) =>
                            Object.entries(series.labels)
                              .map(([key, value]) => `${key}=${value}`)
                              .join(", "),
                          ),
                        },
                        {
                          name: "Metric",
                          type: FieldType.number,
                          config: { unit },
                          values: result.series.map(
                            (series) => series.points.at(-1)?.value ?? null,
                          ),
                        },
                      ],
                    },
                  ]
                : result.series.map((series, i) => ({
                    refId: String.fromCharCode(65 + i),
                    length: series.points.length,
                    fields: [
                      {
                        name: "Time",
                        type: FieldType.time,
                        config: {},
                        values: series.points.map((point) => point.timestamp),
                      },
                      {
                        name: "Value",
                        type: FieldType.number,
                        labels: series.labels,
                        config: { unit },
                        values: series.points.map((point) => point.value),
                      },
                    ],
                  }));
          }
        }
        const config: Partial<GrafanaConfig> = structuredClone(
          panel.config || {},
        );
        // Frames already contain transformations, comparison alignment and field
        // overrides. Retaining those operators would apply them a second time.
        const {
          targets: _targets,
          datasource: _source,
          transformations: _transforms,
          snapshotData: _legacySnapshot,
          repeat: _repeat,
          repeatDirection: _direction,
          maxPerRow: _max,
          timeFrom: _from,
          timeShift: _shift,
          timeCompare: _compare,
          compareWith: _alias,
          ...display
        } = config;
        saved[index] = {
          ...display,
          id: index + 1,
          title: panel.title,
          type: panel.visualization || "timeseries",
          gridPos: { x, y, w, h },
          ...(panelRange.from !== fixed.from || panelRange.to !== fixed.to
            ? { snapshotTimeRange: panelRange }
            : {}),
          transformations: [],
          fieldConfig: { defaults: {}, overrides: [] },
          datasource: { uid: "grafana", type: "grafana" },
          targets: [
            {
              refId: "Snapshot",
              queryType: "snapshot",
              snapshot: frames.map((frame) => dataFrameToJSON(frame)),
            },
          ],
        };
        if (
          new TextEncoder().encode(JSON.stringify(saved[index])).length >
          512 * 1024
        )
          throw new Error(
            "Snapshot panel exceeds 512 KiB. Choose a shorter time range.",
          );
      }
    }),
  );
  const source = {
    uid: `snapshot-${crypto.randomUUID()}`,
    title: name,
    schemaVersion: 42,
    time: {
      from: new Date(fixed.from).toISOString(),
      to: new Date(fixed.to).toISOString(),
    },
    timezone: fixed.timezone,
    refresh: "",
    templating: { list: [] },
    annotations: { list: [] },
    snapshot: {
      created: Date.now(),
      sourceUID: dashboardUID(dashboard),
      sourceTitle: dashboard.name,
    },
    panels: saved,
  };
  if (new TextEncoder().encode(JSON.stringify(source)).length > 4 * 1024 * 1024)
    throw new Error(
      "Snapshot exceeds 4 MiB. Choose fewer panels or a shorter time range.",
    );
  return source;
}
