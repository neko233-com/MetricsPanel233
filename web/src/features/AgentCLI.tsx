import { t as tr } from "../i18n";
import { useState } from "react";
import { Check, Copy, Terminal } from "lucide-react";
const examples = [
  {
    title: "Compare metric patterns",
    detail:
      "Save a time window and search persistent vectors. Explicit start/end make repeat capture reproducible.",
    command:
      "metricspanel patterns capture --metric metricspanel_memory_bytes --range 30m --normalization shape\nmetricspanel patterns list\nmetricspanel patterns search --id PATTERN_ID --limit 10\nmetricspanel patterns search --id PATTERN_ID --exact",
  },
  {
    title: "Discover the API",
    detail:
      "Machine-readable commands, endpoints, limits and payload examples.",
    command: "metricspanel schema",
  },
  {
    title: "Check your workspace",
    detail: "Health, collection rate, storage and active series.",
    command: "metricspanel health\nmetricspanel stats\nmetricspanel metrics",
  },
  {
    title: "Query a time series",
    detail:
      "Bucketed results with label equality filters. rate handles counter resets.",
    command:
      'metricspanel query --metric metricspanel_memory_bytes --range 30m\nmetricspanel query --metric app_requests_total --aggregation rate --range 1h --labels \'{"service":"api"}\'',
  },
  {
    title: "Connect an exporter",
    detail:
      "Collection begins automatically. The ID in the result can be used to scrape or delete.",
    command:
      "metricspanel targets add --name api --url http://localhost:8080/metrics --interval 15s\nmetricspanel targets list\nmetricspanel targets scrape --id 1",
  },
  {
    title: "Push a sample",
    detail:
      "Omit timestamp to use server time. Explicit timestamps use Unix milliseconds.",
    command:
      'echo \'{"samples":[{"name":"app_requests_total","labels":{"service":"api"},"value":42}]}\' | metricspanel ingest --file -',
  },
  {
    title: "Manage dashboards",
    detail:
      "Portable JSON configurations, ready for agents and version control.",
    command:
      "metricspanel dashboards list\nmetricspanel dashboards export --id system > dashboard.json\nmetricspanel dashboards save --file dashboard.json",
  },
];
export function AgentCLI() {
  const [copied, setCopied] = useState<number | null>(null),
    [error, setError] = useState("");
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>{tr("Agent CLI")}</h1>
          <p>{tr("The same workspace, without the clicks.")}</p>
        </div>
        <Terminal className="heading-symbol" size={36} />
      </div>
      <div className="cli-intro">
        <h2>{tr("JSON in. JSON out.")}</h2>
        <p>
          {tr(
            "Every client command returns structured JSON. Errors go to stderr with exit code 1. Use",
          )}
          <code>{tr("METRICSPANEL_URL")}</code>
          {tr("for a remote server and")}
          <code>{tr("METRICSPANEL_TOKEN")}</code>
          {tr("for authentication.")}
        </p>
        <p>
          {tr("Build the CLI from source with")}
          <code>
            {tr("go build -tags webui -o bin/metricspanel ./cmd/metricspanel")}
          </code>
          {tr("after building the web UI. Add")}
          <code>{tr("bin")}</code>
          {tr("to your PATH.")}
        </p>
      </div>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="cli-examples">
        {examples.map((example, i) => (
          <section key={tr(example.title)}>
            <div className="section-heading">
              <div>
                <h2>{tr(example.title)}</h2>
                <p>{tr(example.detail)}</p>
              </div>
              <button
                onClick={async () => {
                  try {
                    await navigator.clipboard.writeText(example.command);
                    setCopied(i);
                    setError("");
                    setTimeout(() => setCopied(null), 1800);
                  } catch {
                    setError(
                      tr(
                        "Clipboard unavailable. Select and copy the command manually.",
                      ),
                    );
                  }
                }}
                aria-label={`Copy ${tr(example.title)}`}
              >
                {copied === i ? <Check size={16} /> : <Copy size={16} />}
                {tr(copied === i ? "Copied" : "Copy")}
              </button>
            </div>
            <pre>
              <code>{example.command}</code>
            </pre>
          </section>
        ))}
      </div>
    </>
  );
}
