import { t as tr } from "../i18n";
import { useState } from "react";
import { Play, Search } from "lucide-react";
import {
  aggregations,
  parseLabels,
  ranges,
  type Metric,
  type Panel,
} from "../api";
import { Chart } from "../components/Chart";
import { PatternAnalysis } from "./PatternAnalysis";
export function Explore({
  metrics,
  tick,
}: {
  metrics: Metric[];
  tick: number;
}) {
  const [metric, setMetric] = useState("metricspanel_memory_bytes"),
    [aggregation, setAggregation] = useState("last"),
    [range, setRange] = useState("30m"),
    [labels, setLabels] = useState("{}"),
    [error, setError] = useState("");
  const [query, setQuery] = useState<{
    panel: Panel;
    range: string;
  }>({
    panel: {
      id: "explore",
      title: "Query result",
      metric: "metricspanel_memory_bytes",
      aggregation: "last",
      unit: "bytes",
    },
    range: "30m",
  });
  const [filter, setFilter] = useState("");
  const [expr, setExpr] = useState("");
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>{tr("Explore")}</h1>
          <p>{tr("Query stored metrics with label filters and PromQL.")}</p>
        </div>
      </div>
      <form
        className="query-builder"
        onSubmit={(e) => {
          e.preventDefault();
          try {
            setQuery({
              panel: {
                id: "explore",
                title: "Query result",
                expr,
                metric,
                aggregation,
                unit: metric.endsWith("_bytes")
                  ? "bytes"
                  : aggregation === "rate"
                    ? "ops"
                    : "",
                labels: parseLabels(labels),
              },
              range,
            });
            setError("");
          } catch (e) {
            setError(String(e));
          }
        }}
      >
        <div className="query-fields">
          <label>
            {tr("Metric")}
            <select value={metric} onChange={(e) => setMetric(e.target.value)}>
              {metrics.map((m) => (
                <option key={m.name}>{m.name}</option>
              ))}
            </select>
          </label>
          <label>
            {tr("Aggregation")}
            <select
              value={aggregation}
              onChange={(e) => setAggregation(e.target.value)}
            >
              {aggregations.map((a) => (
                <option key={a} value={a}>
                  {tr(a)}
                </option>
              ))}
            </select>
          </label>
          <label>
            {tr("Time range")}
            <select value={range} onChange={(e) => setRange(e.target.value)}>
              {ranges.map((r) => (
                <option key={r.value} value={r.value}>
                  {tr(r.label)}
                </option>
              ))}
            </select>
          </label>
          <button className="primary">
            <Play size={17} />
            {tr("Run query")}
          </button>
        </div>
        <label>
          {tr("PromQL expression")}
          <input
            className="mono"
            value={expr}
            onChange={(e) => setExpr(e.target.value)}
            placeholder="sum(rate(app_requests_total[5m]))"
          />
        </label>
        <label>
          {tr("Label filters")}
          <input
            className="mono"
            value={labels}
            onChange={(e) => setLabels(e.target.value)}
            placeholder={'{"service":"api"}'}
          />
        </label>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
      </form>
      <div className="explore-chart">
        <Chart panel={query.panel} range={query.range} tick={tick} />
      </div>
      <PatternAnalysis
        metric={query.panel.expr ? "" : query.panel.metric}
        range={query.range}
        labels={JSON.stringify(query.panel.labels || {})}
      />
      <section className="metric-browser">
        <div className="section-heading">
          <h2>
            {tr("Metric browser")}
            <span>{metrics.length}</span>
          </h2>
          <label className="search-input">
            <Search size={17} />
            <input
              aria-label={tr("Search metrics")}
              placeholder={tr("Search metrics")}
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
            />
          </label>
        </div>
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{tr("Metric name")}</th>
                <th>{tr("Series")}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {metrics
                .filter((m) =>
                  m.name.toLowerCase().includes(filter.toLowerCase()),
                )
                .map((m) => (
                  <tr key={m.name}>
                    <td className="mono">{m.name}</td>
                    <td>{m.series_count}</td>
                    <td>
                      <button
                        className="text-button"
                        onClick={() => {
                          setMetric(m.name);
                          setQuery({
                            panel: {
                              id: "explore",
                              title: "Query result",
                              metric: m.name,
                              aggregation,
                              unit: m.name.endsWith("_bytes") ? "bytes" : "",
                              labels: {},
                            },
                            range,
                          });
                        }}
                      >
                        {tr("Explore")}
                      </button>
                    </td>
                  </tr>
                ))}
            </tbody>
          </table>
        </div>
      </section>
    </>
  );
}
