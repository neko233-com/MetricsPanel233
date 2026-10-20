import { lazy, Suspense, useEffect, useMemo, useState } from "react";
import type { DataFrame } from "@grafana/data";
import DOMPurify from "dompurify";
import { Pencil, Trash2 } from "lucide-react";
import { marked } from "marked";
import {
  message,
  dashboardUID,
  type Panel,
  type InterpolationValues,
  type Dashboard,
} from "../api";
import { useAnnotations } from "./annotations";
import { AnnotationEditor } from "../components/AnnotationEditor";
import { t } from "../i18n";
import { watchFrames } from "./engine";
import { Subject } from "rxjs";
import type { FrameUpdate } from "./engine";
import { PanelZoomControls } from "../components/PanelZoomControls";
import {
  panelZoomToDashboard,
  type PanelTimeRange,
  type TimeSelection,
} from "./time-range";
import { TableRenderer, TimeSeriesRenderer, getRenderer } from "../render";
const PluginPanel = lazy(() => import("./PluginPanel"));
const GrafanaTimeSeries = lazy(() => import("./GrafanaTimeSeries"));

function ownTimeSeriesPreferred() {
  if (typeof window === "undefined") return false;
  try {
    const requested = new URLSearchParams(window.location.search).get("renderer");
    if (requested === "compat") return false;
    if (requested === "own") return true;
    return window.localStorage.getItem("metricspanel-renderer") !== "compat";
  } catch {
    return true;
  }
}

export default function GrafanaPanel({
  panel,
  values,
  range,
  tick,
  onUpdate,
  onRange,
  dashboard,
  onEdit,
  onRemove,
}: {
  panel: Panel;
  values: InterpolationValues;
  range: TimeSelection;
  onRange: (range: TimeSelection) => void;
  tick: number;
  onUpdate?: (update: FrameUpdate) => void;
  dashboard?: Dashboard;
  onEdit?: () => void;
  onRemove?: () => void;
}) {
  const [frames, setFrames] = useState<DataFrame[]>([]),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true),
    [streaming, setStreaming] = useState(false);
  const [queryRange, setQueryRange] = useState<PanelTimeRange>();
  const config = panel.config,
    type = panel.visualization || "timeseries",
    key = JSON.stringify(values);
  const scopeKey = JSON.stringify([panel, values, range]);
  const [zoomScope, setZoomScope] = useState<"dashboard" | "panel">(
    "dashboard",
  );
  const [localZoom, setLocalZoom] = useState<{
    value: TimeSelection;
    key: string;
  }>();
  const local = localZoom?.key === scopeKey ? localZoom.value : undefined;
  const inputRange = local || config?.snapshotTimeRange || range;
  const queryDashboardUID = dashboard ? dashboardUID(dashboard) : undefined;
  useEffect(() => setLocalZoom(undefined), [scopeKey]);
  const refresh = useMemo(() => new Subject<void>(), []);
  useEffect(() => {
    if (type === "text" || type === "row") {
      setLoading(false);
      return;
    }
    setLoading(true);
    const listener = watchFrames(panel, values, inputRange, refresh, {
      dashboardUID: queryDashboardUID,
    }).subscribe({
      next: (update) => {
        onUpdate?.(update);
        setFrames(update.frames);
        setError(update.error);
        setLoading(update.loading);
        setStreaming(update.streaming);
        setQueryRange(update.timeRange);
      },
      error: (e) => {
        setFrames([]);
        setError(message(e));
        setLoading(false);
        setStreaming(false);
      },
    });
    return () => listener.unsubscribe();
  }, [JSON.stringify(panel), key, inputRange, refresh, queryDashboardUID]);
  useEffect(() => refresh.next(), [tick, refresh]);
  const effective = queryRange
    ? {
        from: queryRange.start,
        to: queryRange.end,
        timezone: queryRange.timezone,
      }
    : inputRange;
  const timeInfo = [
    queryRange?.info.timeFrom
      ? `${t("Relative time")}: ${queryRange.info.timeFrom}`
      : "",
    queryRange?.info.timeShift
      ? `${t("Time shift")}: ${queryRange.info.timeShift}`
      : "",
    queryRange?.info.compareWith
      ? `${t("Compare with")}: ${queryRange.info.compareWith === "__previousPeriod" ? t("Previous period") : queryRange.info.compareWith}`
      : "",
  ]
    .filter(Boolean)
    .join(" · ");
  const annotations = useAnnotations(dashboard, panel, effective, tick, values);
  const changePanelRange = (next: TimeSelection) => {
    try {
      const selected = panelZoomToDashboard(next, queryRange?.shift);
      if (zoomScope === "panel")
        setLocalZoom({ value: selected, key: scopeKey });
      else onRange(selected);
    } catch (error) {
      setError(message(error));
    }
  };
  const zoomControls = (
    <PanelZoomControls
      scope={zoomScope}
      active={Boolean(local)}
      onScope={(next) => {
        setZoomScope(next);
        if (next === "dashboard") setLocalZoom(undefined);
      }}
      onReset={() => setLocalZoom(undefined)}
    />
  );
  const editActions = (
    <div className="panel-actions">
      {onEdit ? (
        <button
          className="icon-button"
          aria-label={`${t("Edit")} ${t(panel.title)}`}
          onClick={onEdit}
        >
          <Pencil size={15} />
        </button>
      ) : null}
      {onRemove ? (
        <button
          className="icon-button"
          aria-label={`${t("Remove")} ${t(panel.title)}`}
          onClick={onRemove}
        >
          <Trash2 size={15} />
        </button>
      ) : null}
    </div>
  );
  if (type === "timeseries") {
    const timeSeriesProps = {
      panel,
      frames,
      values,
      range: effective,
      timeInfo,
      controls: zoomControls,
      annotations: annotations.events,
      annotationDashboard: dashboard,
      annotationError: annotations.error,
      loading,
      queryError: error,
      streaming,
      onRange: changePanelRange,
      onEdit,
      onRemove,
    };
    if (ownTimeSeriesPreferred()) return <TimeSeriesRenderer {...timeSeriesProps} />;
    return (
      <Suspense
        fallback={
          <section className="chart-panel" role="status">
            {t("Loading metrics…")}
          </section>
        }
      >
        <GrafanaTimeSeries {...timeSeriesProps} />
      </Suspense>
    );
  }
  if (type === "row")
    return (
      <div className="grafana-row">
        <h2>{panel.title}</h2>
      </div>
    );
  if (["stat", "gauge", "bargauge"].includes(type)) {
    const Stat = getRenderer(type)?.component;
    if (Stat)
      return (
        <Stat
          panel={panel}
          frames={frames}
          values={values}
          range={effective}
          timeInfo={timeInfo}
          loading={loading}
          streaming={streaming}
          queryError={error}
          onEdit={onEdit}
          onRemove={onRemove}
        />
      );
  }
  const supported = ["table", "text"].includes(type);
  if (!supported)
    return (
      <Suspense
        fallback={<div className="chart-panel">{t("Loading plugin…")}</div>}
      >
        <PluginPanel
          panel={panel}
          frames={frames}
          values={values}
          range={inputRange}
          queryRange={queryRange}
          timeInfo={timeInfo}
          controls={
            <>
              {zoomControls}
              {editActions}
              {dashboard && (
                <AnnotationEditor
                  dashboard={dashboard}
                  panelId={Number(panel.config?.id || 0)}
                  time={
                    queryRange
                      ? Math.round((queryRange.start + queryRange.end) / 2)
                      : Date.now()
                  }
                  events={annotations.events}
                />
              )}
            </>
          }
          annotations={annotations.frames}
          annotationError={annotations.error}
          tick={tick}
          loading={loading}
          streaming={streaming}
          queryError={error}
          onRange={changePanelRange}
        />
      </Suspense>
    );
  const content = config?.options?.content || "";
  const html =
    type === "text"
      ? DOMPurify.sanitize(
          config?.options?.mode === "html"
            ? content
            : marked.parse(content, { async: false }),
          {
            USE_PROFILES: { html: true },
            FORBID_TAGS: ["form", "input", "button", "iframe"],
          },
        )
      : "";
  return (
    <section
      className={`chart-panel grafana-panel grafana-${type}`}
      aria-label={panel.title}
    >
      <div className="panel-heading">
        <h2 title={config?.description}>{panel.title}</h2>
        {editActions}
        {streaming && (
          <span className="status healthy" role="status">
            <i />
            {t("Live")}
          </span>
        )}
      </div>
      {timeInfo && <p className="panel-time-info">{timeInfo}</p>}
      {error && (
        <p className="form-error" role="alert">
          {t(error)}
        </p>
      )}
      {!supported && (
        <p role="alert">
          {t("Renderer required")}: <code>{config?.type || type}</code>
        </p>
      )}
      {type === "text" &&
        (config?.options?.mode === "code" ? (
          <pre>{content}</pre>
        ) : (
          <div
            className="grafana-text"
            dangerouslySetInnerHTML={{ __html: html }}
          />
        ))}
      {type === "table" && (
        <TableRenderer
          panel={panel}
          frames={frames}
          values={values}
          range={effective}
        />
      )}
      {!["text", "row"].includes(type) && !error && !frames.length && (
        <p className="subtle">
          {t(loading ? "Loading metrics…" : "No samples in this time range")}
        </p>
      )}
    </section>
  );
}
