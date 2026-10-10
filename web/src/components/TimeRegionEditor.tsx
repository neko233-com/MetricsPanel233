import { t } from "../i18n";
import {
  simpleTimeRegionPlan,
  type TimeRegionConfig,
} from "../grafana/time-regions";

const weekdays = [
  "Monday",
  "Tuesday",
  "Wednesday",
  "Thursday",
  "Friday",
  "Saturday",
  "Sunday",
];
export function TimeRegionEditor({
  value,
  onChange,
}: {
  value: TimeRegionConfig;
  onChange: (config: TimeRegionConfig) => void;
}) {
  const patch = (fields: Partial<TimeRegionConfig>) =>
    onChange({ ...value, ...fields });
  return (
    <div className="time-region-editor">
      <label>
        {t("Region time zone")}
        <input
          aria-label={t("Region time zone")}
          value={value.timezone ?? ""}
          placeholder="browser / utc / Asia/Shanghai"
          list="region-time-zones"
          onChange={(event) =>
            patch({ timezone: event.target.value || undefined })
          }
        />
        <datalist id="region-time-zones">
          {[
            "browser",
            "utc",
            "Asia/Shanghai",
            "America/New_York",
            "Europe/Berlin",
          ].map((zone) => (
            <option key={zone} value={zone} />
          ))}
        </datalist>
      </label>
      <label className="checkbox-label">
        <input
          type="checkbox"
          checked={value.mode === "cron"}
          onChange={(event) => {
            let defaults: Partial<TimeRegionConfig> = {};
            if (event.target.checked && (!value.cronExpr || !value.duration)) {
              try {
                const plan = simpleTimeRegionPlan(value);
                defaults = {
                  cronExpr: value.cronExpr || plan?.cronExpr || "0 9 * * 1-5",
                  duration:
                    value.duration ||
                    `${(plan?.durationMs ?? 8 * 3600000) / 1000}s`,
                };
              } catch {
                defaults = {
                  cronExpr: value.cronExpr || "0 9 * * 1-5",
                  duration: value.duration || "8h",
                };
              }
            }
            patch({ ...defaults, mode: event.target.checked ? "cron" : null });
          }}
        />
        {t("Advanced Cron")}
      </label>
      {value.mode === "cron" ? (
        <>
          <label>
            {t("Cron expression")}
            <input
              value={value.cronExpr || ""}
              placeholder="0 9 * * 1-5"
              onChange={(event) => patch({ cronExpr: event.target.value })}
            />
          </label>
          <label>
            {t("Region duration")}
            <input
              value={value.duration || ""}
              placeholder="8h"
              onChange={(event) => patch({ duration: event.target.value })}
            />
          </label>
        </>
      ) : (
        (["from", "to"] as const).map((bound) => (
          <div className="form-row" key={bound}>
            <label>
              {t(bound === "from" ? "Start weekday" : "End weekday")}
              <select
                aria-label={t(
                  bound === "from" ? "Start weekday" : "End weekday",
                )}
                value={value[`${bound}DayOfWeek`] ?? ""}
                disabled={bound === "to" && value.fromDayOfWeek == null}
                onChange={(event) => {
                  const day = event.target.value
                    ? Number(event.target.value)
                    : undefined;
                  patch({
                    [`${bound}DayOfWeek`]: day,
                    ...(bound === "from" && day == null
                      ? { toDayOfWeek: undefined }
                      : {}),
                  });
                }}
              >
                <option value="">
                  {t(bound === "from" ? "Every day" : "Same as start")}
                </option>
                {weekdays.map((day, index) => (
                  <option key={day} value={index + 1}>
                    {t(day)}
                  </option>
                ))}
              </select>
            </label>
            <label>
              {t(bound === "from" ? "Region start time" : "Region end time")}
              <input
                type="time"
                value={value[bound] || ""}
                onChange={(event) =>
                  patch({ [bound]: event.target.value || undefined })
                }
              />
            </label>
          </div>
        ))
      )}
      <p className="subtle">
        {t(
          "Time regions are generated for the selected window and are read-only. Duration uses elapsed time.",
        )}
      </p>
    </div>
  );
}
