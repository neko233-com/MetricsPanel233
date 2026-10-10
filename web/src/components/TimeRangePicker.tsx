import { useState } from "react";
import { CalendarClock } from "lucide-react";
import { Dialog } from "./Dialog";
import { ranges, message } from "../api";
import { t } from "../i18n";
import {
  rawSelection,
  resolveTimeRange,
  type TimeSelection,
} from "../grafana/time-range";

export function TimeRangePicker({
  value,
  onChange,
}: {
  value: TimeSelection;
  onChange: (value: TimeSelection) => void;
}) {
  const raw = rawSelection(value);
  const preset =
    raw.to === "now" &&
    typeof raw.from === "string" &&
    raw.from.startsWith("now-")
      ? raw.from.slice(4)
      : "custom";
  const known = ranges.some((range) => range.value === preset);
  const resolved = resolveTimeRange(value);
  const [draft, setDraft] = useState<typeof raw | null>(null),
    [error, setError] = useState("");
  return (
    <>
      {!known && (
        <span
          className="time-range-summary"
          title={`${resolved.sdk.from.toISOString()} → ${resolved.sdk.to.toISOString()}`}
        >
          {resolved.sdk.from.format("MM-DD HH:mm:ss")} →{" "}
          {resolved.sdk.to.format("MM-DD HH:mm:ss")} ·{" "}
          {resolved.timezone === "browser"
            ? t("Browser time")
            : resolved.timezone}
        </span>
      )}
      <select
        aria-label={t("Time range")}
        value={known ? preset : "custom"}
        onChange={(event) => {
          if (event.target.value === "custom") {
            setError("");
            setDraft(raw);
          } else
            onChange({
              from: "now-" + event.target.value,
              to: "now",
              timezone: raw.timezone,
            });
        }}
      >
        {ranges.map((range) => (
          <option key={range.value} value={range.value}>
            {t(range.label)}
          </option>
        ))}
        <option value="custom">{t("Custom time range")}</option>
      </select>
      <button
        className="icon-button outlined"
        aria-label={t("Choose time range")}
        title={t("Choose time range")}
        onClick={() => {
          setError("");
          setDraft(raw);
        }}
      >
        <CalendarClock size={18} />
      </button>
      {draft && (
        <Dialog title={t("Time range")} onClose={() => setDraft(null)}>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              try {
                resolveTimeRange(draft);
                onChange(draft);
                setDraft(null);
              } catch (error) {
                setError(t(message(error)));
              }
            }}
          >
            <label>
              {t("From")}
              <input
                value={String(draft.from)}
                onChange={(event) =>
                  setDraft({ ...draft, from: event.target.value })
                }
                required
              />
            </label>
            <label>
              {t("To")}
              <input
                value={String(draft.to)}
                onChange={(event) =>
                  setDraft({ ...draft, to: event.target.value })
                }
                required
              />
            </label>
            <label>
              {t("Time zone")}
              <input
                value={draft.timezone}
                onChange={(event) =>
                  setDraft({ ...draft, timezone: event.target.value })
                }
                placeholder="browser / utc / Asia/Shanghai"
                required
              />
            </label>
            <p className="subtle">
              {t(
                "Use date math, ISO dates or Unix milliseconds. Maximum range: 31 days.",
              )}
            </p>
            {error && (
              <p className="form-error" role="alert">
                {error}
              </p>
            )}
            <div className="form-actions">
              <button type="button" onClick={() => setDraft(null)}>
                {t("Cancel")}
              </button>
              <button className="primary">{t("Apply time range")}</button>
            </div>
          </form>
        </Dialog>
      )}
    </>
  );
}
