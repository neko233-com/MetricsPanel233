import { useEffect, useRef, useState } from "react";
import { api, jsonBody, message, type Dashboard } from "../api";
import { Dialog } from "../components/Dialog";
import { communityReference } from "../grafana/community-reference";
import { t } from "../i18n";

type Input = {
  name: string;
  label: string;
  type: string;
  plugin_id?: string;
  value?: string;
  required?: boolean;
};
type Preview = {
  title: string;
  dashboard: unknown;
  inputs: Input[];
  id?: number;
  revision?: number;
  revisions?: number[];
  url?: string;
};
type Source = { uid: string; name: string; type: string; isDefault?: boolean };
export function DashboardImport({
  close,
  imported,
  initial,
}: {
  close: () => void;
  imported: (dashboard: Dashboard, warnings: string[]) => Promise<void>;
  initial?: unknown;
}) {
  const [mode, setMode] = useState(initial ? "json" : "community"),
    [reference, setReference] = useState("");
  const [json, setJSON] = useState(""),
    [preview, setPreview] = useState<Preview | null>(null),
    [sources, setSources] = useState<Source[]>([]);
  const [name, setName] = useState(""),
    [uid, setUID] = useState(""),
    [inputs, setInputs] = useState<Record<string, string>>({});
  const [error, setError] = useState(""),
    [loading, setLoading] = useState(false),
    [saving, setSaving] = useState(false);
  const read = useRef<AbortController | null>(null);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    const controller = new AbortController();
    api<Source[]>("/api/datasources", { signal: controller.signal })
      .then(setSources)
      .catch((e) => {
        if (!controller.signal.aborted) setError(message(e));
      });
    return () => {
      alive.current = false;
      controller.abort();
      read.current?.abort();
    };
  }, []);
  function changeMode(next: string) {
    read.current?.abort();
    setLoading(false);
    setMode(next);
    setPreview(null);
    setError("");
  }
  async function load(raw?: unknown, revision?: number) {
    read.current?.abort();
    const controller = new AbortController();
    read.current = controller;
    setLoading(true);
    setError("");
    try {
      let result: Preview;
      if (mode === "community" && !raw) {
        const source = communityReference(reference);
        result = await api<Preview>(
          `/dashboards/community/${source.id}${revision || source.revision ? `?revision=${revision || source.revision}` : ""}`,
          { signal: controller.signal },
        );
      } else {
        const source = raw ?? JSON.parse(json);
        if (
          new TextEncoder().encode(JSON.stringify(source)).length >
          4 * 1024 * 1024
        )
          throw new Error(t("Import must be under 4 MiB"));
        result = await api<Preview>("/import/grafana-preview", {
          method: "POST",
          body: jsonBody({ dashboard: source }),
          signal: controller.signal,
        });
      }
      if (controller.signal.aborted) return;
      setPreview(result);
      setName(result.title);
      setUID("");
      setInputs(
        Object.fromEntries(
          result.inputs
            .filter((input) => input.type === "constant" || input.value)
            .map((input) => [input.name, input.value || ""]),
        ),
      );
    } catch (e) {
      if (!controller.signal.aborted) {
        setError(t(message(e)));
        setPreview(null);
      }
    } finally {
      if (!controller.signal.aborted) setLoading(false);
    }
  }
  useEffect(() => {
    if (initial) void load(initial);
  }, []);
  function selection(input: Input): string {
    if (Object.hasOwn(inputs, input.name)) {
      const value = inputs[input.name];
      return input.type !== "datasource" ||
        sources.some(
          (source) =>
            source.uid === value &&
            (!input.plugin_id || source.type === input.plugin_id),
        )
        ? value
        : "";
    }
    if (input.type !== "datasource") return "";
    const options = sources.filter(
      (source) => !input.plugin_id || source.type === input.plugin_id,
    );
    return (
      (
        options.find((source) => source.isDefault) ||
        (options.length === 1 ? options[0] : undefined)
      )?.uid || ""
    );
  }
  return (
    <Dialog
      title={t("Import dashboard")}
      onClose={() => {
        if (!saving) close();
      }}
      style={{ width: 640 }}
    >
      {!initial && (
        <>
          <div
            className="plugin-tabs"
            role="tablist"
            aria-label={t("Import source")}
          >
            <button
              role="tab"
              aria-selected={mode === "community"}
              disabled={saving}
              onClick={() => changeMode("community")}
            >
              {t("Grafana.com")}
            </button>
            <button
              role="tab"
              aria-selected={mode === "json"}
              disabled={saving}
              onClick={() => changeMode("json")}
            >
              {t("JSON")}
            </button>
          </div>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void load();
            }}
          >
            {mode === "community" ? (
              <label>
                {t("Grafana.com URL or ID")}
                <input
                  autoFocus
                  value={reference}
                  onChange={(event) => {
                    read.current?.abort();
                    setLoading(false);
                    setReference(event.target.value);
                    setPreview(null);
                  }}
                  placeholder="1860"
                  required
                  disabled={saving}
                />
              </label>
            ) : (
              <>
                <label>
                  {t("Dashboard JSON")}
                  <textarea
                    value={json}
                    onChange={(event) => {
                      read.current?.abort();
                      setLoading(false);
                      setJSON(event.target.value);
                      setPreview(null);
                    }}
                    rows={6}
                    required
                    disabled={saving}
                    spellCheck={false}
                  />
                </label>
                <label>
                  {t("Upload JSON file")}
                  <input
                    type="file"
                    accept=".json,application/json"
                    disabled={saving || loading}
                    onChange={async (event) => {
                      const file = event.target.files?.[0];
                      if (!file) return;
                      try {
                        if (file.size > 4 * 1024 * 1024)
                          throw new Error(t("Import must be under 4 MiB"));
                        const text = await file.text();
                        if (!alive.current) return;
                        setJSON(text);
                        await load(JSON.parse(text));
                      } catch (e) {
                        setError(message(e));
                      }
                    }}
                  />
                </label>
              </>
            )}
            <button disabled={loading || saving}>
              {t(loading ? "Loading…" : "Load dashboard")}
            </button>
          </form>
        </>
      )}
      {preview && (
        <form
          className="dashboard-import-preview"
          onSubmit={async (event) => {
            event.preventDefault();
            setSaving(true);
            setError("");
            try {
              const bindings = Object.fromEntries(
                preview.inputs
                  .filter(
                    (input) =>
                      input.type === "constant" || selection(input) !== "",
                  )
                  .map((input) => [input.name, selection(input)]),
              );
              const result = await api<{
                dashboard: Dashboard;
                warnings: string[];
              }>(
                preview.id
                  ? "/import/grafana-community"
                  : "/import/grafana-bound",
                {
                  method: "POST",
                  body: jsonBody({
                    ...(preview.id
                      ? { id: preview.id, revision: preview.revision }
                      : { dashboard: preview.dashboard }),
                    name,
                    ...(uid ? { uid } : {}),
                    inputs: bindings,
                  }),
                },
              );
              await imported(result.dashboard, result.warnings);
            } catch (e) {
              if (alive.current) setError(message(e));
            } finally {
              if (alive.current) setSaving(false);
            }
          }}
        >
          {preview.id && (
            <div className="form-row">
              <p className="subtle">Grafana.com #{preview.id}</p>
              <label>
                {t("Community revision")}
                <select
                  value={preview.revision}
                  disabled={loading || saving}
                  onChange={(event) =>
                    void load(undefined, Number(event.target.value))
                  }
                >
                  {(preview.revisions || [preview.revision!]).map((value) => (
                    <option key={value} value={value}>
                      {value}
                    </option>
                  ))}
                </select>
              </label>
            </div>
          )}
          <label>
            {t("Dashboard name")}
            <input
              value={name}
              onChange={(event) => setName(event.target.value)}
              required
              maxLength={100}
              disabled={saving || loading}
            />
          </label>
          <label>
            {t("Dashboard UID (optional)")}
            <input
              value={uid}
              onChange={(event) => setUID(event.target.value)}
              maxLength={100}
              placeholder={t("Generate new UID")}
              disabled={saving || loading}
            />
          </label>
          {preview.inputs.map((input) => (
            <label key={input.name}>
              {input.label || input.name}
              {input.type === "datasource" ? (
                <select
                  value={selection(input)}
                  required
                  disabled={saving || loading}
                  onChange={(event) =>
                    setInputs({ ...inputs, [input.name]: event.target.value })
                  }
                >
                  <option value="">{t("Choose datasource")}</option>
                  {sources
                    .filter(
                      (source) =>
                        !input.plugin_id || source.type === input.plugin_id,
                    )
                    .map((source) => (
                      <option key={source.uid} value={source.uid}>
                        {source.name} · {source.uid}
                      </option>
                    ))}
                </select>
              ) : (
                <input
                  value={inputs[input.name] || ""}
                  onChange={(event) =>
                    setInputs({ ...inputs, [input.name]: event.target.value })
                  }
                  required={false}
                  disabled={saving || loading}
                />
              )}
            </label>
          ))}
          {preview.inputs.some(
            (input) =>
              input.type === "datasource" &&
              !sources.some(
                (source) => !input.plugin_id || source.type === input.plugin_id,
              ),
          ) && (
            <p className="form-error" role="alert">
              {t(
                "Add a matching datasource in Plugins & datasources before importing.",
              )}
            </p>
          )}
          <div className="form-actions">
            <button type="button" disabled={saving} onClick={close}>
              {t("Cancel")}
            </button>
            <button className="primary" disabled={saving || loading}>
              {t(saving ? "Importing…" : "Import dashboard")}
            </button>
          </div>
        </form>
      )}
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      {!preview && (
        <div className="form-actions">
          <button disabled={saving} onClick={close}>
            {t("Cancel")}
          </button>
        </div>
      )}
    </Dialog>
  );
}
