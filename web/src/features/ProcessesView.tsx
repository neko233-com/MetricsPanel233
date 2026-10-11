import { memo, useEffect, useRef, useState } from "react";
import { ArrowDownAZ, Cpu, RefreshCw, Search } from "lucide-react";
import {
  api,
  ApiError,
  formatProcessMemory,
  message,
  processIconBlob,
  stableProcessRows,
  type HostProcess,
  type HostProcessDetail,
  type HostProcesses,
  type ProcessFieldState,
} from "../api";
import { Dialog } from "../components/Dialog";
import { getLocale, t } from "../i18n";

const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ".split("");
const pageSize = 200;
const fieldStates: Record<ProcessFieldState, string> = {
  ok: "Available",
  warming_up: "Warming up",
  denied: "Permission denied",
  exited: "Exited",
  unsupported: "Unsupported",
  unavailable: "Unavailable",
};
const sampledTime = (value: number) =>
  new Date(value).toLocaleString(getLocale() === "zh" ? "zh-CN" : "en");
const cpuValue = (item: HostProcess) =>
  item.cpuPercent === null ? "—" : `${item.cpuPercent.toFixed(1)}%`;

const ProcessIcon = memo(function ProcessIcon({ iconKey }: { iconKey: string }) {
  const element = useRef<HTMLSpanElement>(null);
  const [visible, setVisible] = useState(false);
  const [url, setURL] = useState("");
  useEffect(() => {
    if (!iconKey || !element.current) return;
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) {
        setVisible(true);
        observer.disconnect();
      }
    }, { rootMargin: "80px" });
    observer.observe(element.current);
    return () => observer.disconnect();
  }, [iconKey]);
  useEffect(() => {
    if (!visible || !iconKey) return;
    const controller = new AbortController();
    const reader = new FileReader();
    setURL("");
    void processIconBlob(iconKey, controller.signal).then((blob) => {
      if (controller.signal.aborted) return;
      reader.onload = () => {
        if (!controller.signal.aborted && typeof reader.result === "string") setURL(reader.result);
      };
      reader.readAsDataURL(blob);
    }).catch(() => {
      // Icons are optional. Failed reads leave the same generic icon in place.
    });
    return () => {
      controller.abort();
      if (reader.readyState === FileReader.LOADING) reader.abort();
    };
  }, [iconKey, visible]);
  return (
    <span className="process-icon" ref={element} aria-hidden="true" data-icon={url ? "loaded" : "generic"}>
      {url ? <img src={url} alt="" onError={() => setURL("")} /> : <Cpu size={18} />}
    </span>
  );
});

export function ProcessesView() {
  const [prefix, setPrefix] = useState("");
  const [prefixError, setPrefixError] = useState("");
  const [query, setQuery] = useState({ prefix: "", revision: 0 });
  const [data, setData] = useState<(HostProcesses & { requestPrefix: string }) | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState("");
  const [refreshSeconds, setRefreshSeconds] = useState(5);
  const [pid, setPID] = useState("");
  const [pidError, setPIDError] = useState("");
  const [selected, setSelected] = useState<{ pid: number; startToken: string } | null>(null);
  const pagesLoaded = useRef(1);
  const moreController = useRef<AbortController | null>(null);
  const refresh = () => setQuery((old) => ({ ...old, revision: old.revision + 1 }));

  useEffect(() => {
    const controller = new AbortController();
    moreController.current?.abort();
    setLoadingMore(false);
    setLoading(true);
    setError("");
    void (async () => {
      const params = new URLSearchParams({ prefix: query.prefix, limit: String(pageSize) });
      let result = await api<HostProcesses>(`/host/processes?${params}`, { signal: controller.signal });
      const items = [...result.items];
      let count = 1;
      while (result.continueToken && count < pagesLoaded.current) {
        params.set("continue", result.continueToken);
        result = await api<HostProcesses>(`/host/processes?${params}`, { signal: controller.signal });
        items.push(...result.items);
        count++;
      }
      if (!controller.signal.aborted) {
        pagesLoaded.current = count;
        setData((old) => ({ ...result, requestPrefix: query.prefix, items: stableProcessRows(old?.requestPrefix === query.prefix ? old.items : [], items) }));
      }
    })().catch((cause) => {
      if (!controller.signal.aborted) setError(processError(cause));
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false);
    });
    return () => controller.abort();
  }, [query]);

  useEffect(() => {
    if (!refreshSeconds || loading || loadingMore) return;
    const timer = setInterval(() => {
      if (!document.hidden) refresh();
    }, refreshSeconds * 1000);
    return () => clearInterval(timer);
  }, [refreshSeconds, loading, loadingMore]);
  useEffect(() => () => moreController.current?.abort(), []);

  const filter = (value: string) => {
    if (new TextEncoder().encode(value).length > 128) { setPrefixError(t("Name prefix must be at most 128 UTF-8 bytes.")); return; }
    setPrefixError("");
    setPrefix(value);
    pagesLoaded.current = 1;
    setQuery((old) => ({ prefix: value, revision: old.revision + 1 }));
  };
  const loadMore = async () => {
    if (!data?.continueToken || loading || loadingMore) return;
    const controller = new AbortController();
    moreController.current = controller;
    setLoadingMore(true);
    setError("");
    try {
      const params = new URLSearchParams({ prefix: query.prefix, limit: String(pageSize), continue: data.continueToken });
      const result = await api<HostProcesses>(`/host/processes?${params}`, { signal: controller.signal });
      if (!controller.signal.aborted) {
        pagesLoaded.current++;
        setData((old) => old?.snapshotId === result.snapshotId ? { ...result, requestPrefix: query.prefix, items: [...old.items, ...result.items] } : old);
      }
    } catch (cause) {
      if (!controller.signal.aborted) setError(processError(cause));
    } finally {
      if (!controller.signal.aborted) setLoadingMore(false);
    }
  };
  const rowsMatch = data?.requestPrefix === query.prefix;
  const rows = rowsMatch ? data.items : [];
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>{t("Processes")}</h1>
          <p>{t("Processes visible to the server. A container shows only its PID namespace.")}</p>
        </div>
      </div>
      <section className="list-panel processes-panel" aria-label={t("Host processes")}>
        <div className="process-source">
          <span>{t("Server host")}: <strong>{data?.source.hostname || "—"}</strong></span>
          {data ? <span className="mono">{data.source.os} · {t(data.source.scope === "pid_namespace" ? "PID namespace" : "Host namespace")}</span> : null}
          <span>{t("Collected at")}: {data ? sampledTime(data.sampledAt) : "—"}</span>
        </div>
        <div className="process-controls">
          <form className="process-prefix-form" onSubmit={(event) => { event.preventDefault(); filter(prefix.trim()); }}>
            <label htmlFor="process-prefix">{t("Name prefix")}</label>
            <div>
              <input id="process-prefix" value={prefix} maxLength={128} onChange={(event) => { setPrefix(event.target.value); setPrefixError(""); }} autoComplete="off" aria-invalid={Boolean(prefixError)} aria-describedby={prefixError ? "process-prefix-error" : undefined} />
              <button type="submit"><Search size={16} />{t("Filter processes")}</button>
            </div>
          </form>
          <div className="process-refresh-controls">
            <label>{t("Auto refresh")}<select value={refreshSeconds} onChange={(event) => setRefreshSeconds(Number(event.target.value))}>
              <option value={0}>{t("Off")}</option><option value={5}>5s</option><option value={10}>10s</option><option value={30}>30s</option>
            </select></label>
            <button onClick={refresh} disabled={loading || loadingMore}><RefreshCw size={16} />{t("Refresh")}</button>
          </div>
        </div>
        {prefixError ? <p id="process-prefix-error" className="form-error process-message" role="alert">{prefixError}</p> : null}
        <div className="process-alphabet" role="group" aria-label={t("Prefix shortcuts")}>
          <button className={!query.prefix ? "active" : ""} onClick={() => filter("")} aria-pressed={!query.prefix}>{t("All names")}</button>
          {alphabet.map((letter) => <button key={letter} className={query.prefix.toUpperCase() === letter ? "active" : ""} aria-pressed={query.prefix.toUpperCase() === letter} onClick={() => filter(letter.toLowerCase())}>{letter}</button>)}
        </div>
        <div className="process-list-heading">
          <span><ArrowDownAZ size={16} />{t("Name A–Z")}<span className="muted">{rows.length} / {rowsMatch ? data.total : "—"}</span></span>
          <form className="process-pid-form" onSubmit={(event) => {
            event.preventDefault();
            if (!/^\d{1,10}$/.test(pid) || Number(pid) > 4294967295) { setPIDError(t("Enter a valid numeric PID.")); return; }
            setPIDError(""); setSelected({ pid: Number(pid), startToken: "" });
          }}>
            <label htmlFor="process-pid">PID</label>
            <input id="process-pid" inputMode="numeric" value={pid} onChange={(event) => { setPID(event.target.value); setPIDError(""); }} maxLength={10} />
            <button type="submit">{t("Find PID")}</button>
          </form>
        </div>
        {pidError ? <p className="form-error process-message" role="alert">{pidError}</p> : null}
        {error ? <div className="process-message form-error" role="alert">{t(error)} <button onClick={refresh} disabled={loading}>{t("Retry")}</button></div> : null}
        {rowsMatch && data.partial ? <div className="process-message process-warning" role="status">{t("Some processes could not be read.")}{data.warnings.map((warning, index) => <p key={index}>{t(warning)}</p>)}</div> : null}
        {loading && !rowsMatch ? <p className="process-message muted" role="status">{t("Loading processes…")}</p> : null}
        <div className="table-scroll process-table">
          <table>
            <thead><tr><th>{t("Name")}</th><th>PID</th><th className="process-parent">PPID</th><th>{t("CPU usage")}</th><th>{t("Resident memory")}</th></tr></thead>
            <tbody>{rows.map((item) => <tr key={item.id} data-process-id={item.id}>
              <td><button className="process-name" onClick={() => setSelected({ pid: item.pid, startToken: item.startToken })}><ProcessIcon iconKey={item.iconKey} /><span>{item.name}</span></button></td>
              <td className="mono">{item.pid}</td><td className="mono process-parent">{item.ppid}</td>
              <td className="mono" title={t(fieldStates[item.states.cpu])}>{cpuValue(item)}{item.states.cpu === "warming_up" ? <small>{t("Warming up")}</small> : null}</td>
              <td className="mono" title={t(fieldStates[item.states.memory])}>{formatProcessMemory(item.memoryRssBytes)}</td>
            </tr>)}</tbody>
          </table>
        </div>
        {!loading && !rows.length && !error ? <div className="process-empty">{t("No processes match this prefix")}</div> : null}
        <div className="process-list-footer">
          <span className="muted">{t("100% CPU is one logical core.")} {t("Unavailable fields require operating system permission.")}</span>
          {rowsMatch && data.continueToken ? <button onClick={() => void loadMore()} disabled={loading || loadingMore}>{t(loadingMore ? "Loading…" : "Load more")}</button> : null}
        </div>
      </section>
      {selected ? <ProcessDetails selected={selected} onClose={() => setSelected(null)} /> : null}
    </>
  );
}

function processError(cause: unknown): string {
  if (cause instanceof ApiError && cause.status === 410) return "Process snapshot expired. Refresh the list.";
  return message(cause);
}

function ProcessDetails({ selected, onClose }: { selected: { pid: number; startToken: string }; onClose: () => void }) {
  const identityToken = useRef(selected.startToken);
  const [data, setData] = useState<HostProcessDetail | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [revision, setRevision] = useState(0);
  const [identityChanged, setIdentityChanged] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true); setError("");
    const params = new URLSearchParams();
    if (identityToken.current) params.set("startToken", identityToken.current);
    void api<HostProcessDetail>(`/host/processes/${selected.pid}?${params}`, { signal: controller.signal }).then((result) => {
      if (!controller.signal.aborted) {
        if (!identityToken.current && result.process.startToken) identityToken.current = result.process.startToken;
        setData(result);
      }
    }).catch((cause) => {
      if (controller.signal.aborted) return;
      if (cause instanceof ApiError && cause.status === 409) { setIdentityChanged(true); setError("PID now belongs to another process. Close details and select it again."); }
      else if (cause instanceof ApiError && cause.status === 404) { setIdentityChanged(true); setError("Process is no longer available."); }
      else setError(message(cause));
    }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [selected.pid, selected.startToken, revision]);
  const item = data?.process;
  return <Dialog title={`${t("Process details")} · PID ${selected.pid}`} onClose={onClose} style={{ width: "min(620px, calc(100vw - 32px))" }}>
    <div className="process-detail">
      {loading ? <p role="status">{t("Loading process…")}</p> : null}
      {error ? <p className="form-error" role="alert">{t(error)}</p> : null}
      {item && data && !error ? <>
        <div className="process-detail-title"><ProcessIcon iconKey={item.iconKey} /><h3>{item.name}</h3></div>
        <dl>
          <dt>PID</dt><dd className="mono">{item.pid}</dd><dt>{t("Parent PID")}</dt><dd className="mono">{item.ppid}</dd>
          <dt>{t("Server host")}</dt><dd>{data.source.hostname} · {data.source.os}</dd>
          <dt>{t("PID namespace")}</dt><dd>{t(data.source.scope === "pid_namespace" ? "PID namespace" : "Host namespace")}</dd>
          <dt>{t("CPU usage")}</dt><dd>{cpuValue(item)} <small>{t(fieldStates[item.states.cpu])}</small></dd>
          <dt>{t("Resident memory")}</dt><dd>{formatProcessMemory(item.memoryRssBytes)} <small>{item.memoryRssBytes !== null ? `${item.memoryRssBytes} B · ` : ""}{t(fieldStates[item.states.memory])}</small></dd>
          <dt>{t("Threads")}</dt><dd>{item.threads ?? "—"}</dd>
          <dt>{t("Started at")}</dt><dd>{item.startedAt === null ? "—" : sampledTime(item.startedAt)} <small>{t(fieldStates[item.states.startTime])}</small></dd>
          <dt>{t("Collected at")}</dt><dd>{sampledTime(data.sampledAt)}</dd>
        </dl>
        <p className="muted">{t("100% CPU is one logical core.")}</p>
        {!item.startToken ? <p className="muted">{t("Start time unavailable; PID may have been reused.")}</p> : null}
      </> : null}
      <div className="form-actions"><button onClick={onClose}>{t("Close")}</button>{!identityChanged ? <button onClick={() => setRevision((value) => value + 1)} disabled={loading}><RefreshCw size={16} />{t("Refresh")}</button> : null}</div>
    </div>
  </Dialog>;
}
