import { useEffect, useMemo, useState } from "react";
import { ScanLine, Save } from "lucide-react";
import {
  api,
  jsonBody,
  message,
  parseLabels,
  formatValue,
  type Labels,
  type QueryResult,
} from "../api";
import { Chart } from "../components/Chart";
import { t, getLocale } from "../i18n";

type Pattern = {
  id: string;
  metric: string;
  labels: Labels;
  start: number;
  end: number;
  aggregation: string;
  normalization: string;
  values?: number[];
  mean: number;
  stddev: number;
  coverage: number;
  created_at: number;
};
type SearchResult = {
  reference: Pattern;
  hits: { pattern: Pattern; distance: number }[];
  approximate: boolean;
  engine: string;
};
const describe = (p: Pattern) =>
  `${p.metric} · ${Object.entries(p.labels)
    .map(([k, v]) => `${k}=${v}`)
    .join(", ")} · ${new Date(p.start).toLocaleString()}`;

export function PatternAnalysis({
  metric,
  range,
  labels,
}: {
  metric: string;
  range: string;
  labels: string;
}) {
  const locale = getLocale();
  const [patterns, setPatterns] = useState<Pattern[]>([]),
    [reference, setReference] = useState(""),
    [normalization, setNormalization] = useState("shape"),
    [aggregation, setAggregation] = useState("last"),
    [exact, setExact] = useState(false),
    [sameMetric, setSameMetric] = useState(true);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [notice, setNotice] = useState(""),
    [result, setResult] = useState<SearchResult | null>(null),
    [preview, setPreview] = useState<Pattern[]>([]);
  async function reload(signal?: AbortSignal) {
    const data = await api<{ patterns: Pattern[] }>("/patterns?limit=1000", {
      signal,
    });
    setPatterns(data.patterns);
    setReference((old) => old || data.patterns[0]?.id || "");
  }
  useEffect(() => {
    const controller = new AbortController();
    void reload(controller.signal).catch((e) => {
      if (!controller.signal.aborted) setError(message(e));
    });
    return () => controller.abort();
  }, []);
  async function compareWindow(hit: Pattern) {
    try {
      const [source, neighbor] = await Promise.all([
        api<Pattern>(`/patterns/${reference}`),
        api<Pattern>(`/patterns/${hit.id}`),
      ]);
      setPreview([source, neighbor]);
    } catch (e) {
      setError(message(e));
    }
  }
  const chart = useMemo<QueryResult | undefined>(() => {
    if (!preview.length) return undefined;
    const [source] = preview;
    return {
      metric: "pattern",
      aggregation: "vector",
      start: source.start,
      end: source.end,
      step: (source.end - source.start) / 64,
      series: preview.map((p, i) => ({
        labels: { window: i === 0 ? t("Reference") : t("Match") },
        points: (p.values || []).map((value, index) => ({
          timestamp: source.start + (index * (source.end - source.start)) / 64,
          value,
        })),
      })),
    };
  }, [preview, locale]);
  return (
    <section className="pattern-analysis list-panel">
      <div className="section-heading">
        <div>
          <h2>
            <ScanLine size={19} />
            {t("Pattern analysis")}
          </h2>
          <p className="subtle">
            {t("Save metric windows and find similar behavior.")}
          </p>
        </div>
      </div>
      <div className="pattern-controls">
        <label>
          {t("Save as")}
          <select
            aria-label={t("Pattern normalization")}
            value={normalization}
            onChange={(e) => setNormalization(e.target.value)}
          >
            <option value="shape">{t("Shape")}</option>
            <option value="raw">{t("Raw values")}</option>
          </select>
        </label>
        <label>
          {t("Measure")}
          <select
            aria-label={t("Pattern measure")}
            value={aggregation}
            onChange={(e) => setAggregation(e.target.value)}
          >
            <option value="last">{t("Level")}</option>
            <option value="rate">{t("Counter rate")}</option>
          </select>
        </label>
        <button
          className="primary"
          disabled={busy || !metric}
          onClick={async () => {
            setBusy(true);
            setError("");
            setNotice("");
            try {
              const data = await api<{
                patterns: Pattern[];
                skipped: string[];
              }>("/patterns/capture", {
                method: "POST",
                body: jsonBody({
                  metric,
                  range,
                  labels: parseLabels(labels),
                  normalization,
                  aggregation,
                }),
              });
              await reload();
              setReference(data.patterns[0].id);
              setResult(null);
              setPreview([]);
              setNotice(
                `${t("Saved windows")}: ${data.patterns.length}${data.skipped.length ? ` · ${t("Skipped")}: ${data.skipped.length}` : ""}`,
              );
            } catch (e) {
              setError(message(e));
            } finally {
              setBusy(false);
            }
          }}
        >
          <Save size={16} />
          {t("Save current window")}
        </button>
      </div>
      <p className="pattern-help">
        {metric ? (
          <>
            {t("Capture source")}: <code>{metric}</code> · {range}
          </>
        ) : (
          t("Run a metric query to capture a window.")
        )}
      </p>
      <p className="pattern-help">
        {t(
          "Shape removes average and scale. Raw values preserve measurement levels.",
        )}{" "}
        {t(
          "Windows need 75% coverage; only small gaps are interpolated for analysis.",
        )}
      </p>
      <div className="pattern-search">
        <label>
          {t("Saved reference")}
          <select
            aria-label={t("Saved reference")}
            disabled={busy}
            value={reference}
            onChange={(e) => {
              setReference(e.target.value);
              setResult(null);
              setPreview([]);
            }}
          >
            <option value="">{t("Select a saved window")}</option>
            {patterns.map((p) => (
              <option key={p.id} value={p.id}>
                {describe(p)} ·{" "}
                {t(p.normalization === "shape" ? "Shape" : "Raw values")}
              </option>
            ))}
          </select>
        </label>
        <label className="check-label">
          <input
            type="checkbox"
            disabled={busy}
            checked={sameMetric}
            onChange={(e) => setSameMetric(e.target.checked)}
          />
          {t("Only this metric")}
        </label>
        <label className="check-label">
          <input
            type="checkbox"
            disabled={busy}
            checked={exact}
            onChange={(e) => setExact(e.target.checked)}
          />
          {t("Exact comparison")}
        </label>
        <button
          disabled={busy || !reference}
          onClick={async () => {
            setBusy(true);
            setError("");
            setNotice("");
            try {
              const source =
                patterns.find((p) => p.id === reference) ||
                (await api<Pattern>(`/patterns/${reference}`));
              const data = await api<SearchResult>("/patterns/search", {
                method: "POST",
                body: jsonBody({
                  id: reference,
                  metric: sameMetric ? source.metric : "",
                  limit: 10,
                  exact,
                }),
              });
              setResult(data);
              if (data.hits.length) await compareWindow(data.hits[0].pattern);
              else setPreview([]);
            } catch (e) {
              setError(message(e));
            } finally {
              setBusy(false);
            }
          }}
        >
          <ScanLine size={16} />
          {t("Find similar windows")}
        </button>
      </div>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      {notice && <p role="status">{notice}</p>}
      {!patterns.length && !error && (
        <p className="subtle">
          {t(
            "No saved windows yet. Collect enough samples, then save a window.",
          )}
        </p>
      )}
      {result && (
        <>
          <p className="pattern-help">
            {t(result.approximate ? "Approximate results" : "Exact results")} ·{" "}
            {t(
              "Lower distance means a closer match. This is not a probability.",
            )}
          </p>
          {!result.hits.length ? (
            <p className="subtle">
              {t("No other saved windows match these filters.")}
            </p>
          ) : (
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>{t("Metric")}</th>
                    <th>{t("Labels")}</th>
                    <th>{t("Window")}</th>
                    <th>{t("Distance")}</th>
                    <th>{t("Coverage")}</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {result.hits.map((hit) => (
                    <tr key={hit.pattern.id}>
                      <td className="mono">{hit.pattern.metric}</td>
                      <td>
                        {Object.entries(hit.pattern.labels)
                          .map(([k, v]) => `${k}=${v}`)
                          .join(", ")}
                      </td>
                      <td>
                        {new Date(hit.pattern.start).toLocaleString()}
                        <br />
                        {new Date(hit.pattern.end).toLocaleString()}
                      </td>
                      <td>{hit.distance.toFixed(4)}</td>
                      <td>{Math.round(hit.pattern.coverage * 100)}%</td>
                      <td>
                        <button
                          className="text-button"
                          onClick={() => void compareWindow(hit.pattern)}
                        >
                          {t("Compare")}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
      {chart && (
        <div className="pattern-preview">
          <p className="pattern-help">
            {t("Analysis vectors aligned to the reference window.")}
          </p>
          <Chart
            panel={{
              id: "pattern-preview",
              title: t("Window comparison"),
              metric: "pattern",
              aggregation: "last",
              unit: "",
            }}
            range={range}
            tick={0}
            result={chart}
            formatter={(value) => formatValue(value)}
          />
        </div>
      )}
    </section>
  );
}
