import { useLayoutEffect, useRef, useState, type ComponentProps } from "react";
import { createTheme, ThemeContext, type DataFrame } from "@grafana/data";
import { Table } from "@grafana/ui";
import { interpolate, type InterpolationValues, type Panel } from "../api";
import { t } from "../i18n";
import { resolveTimeRange, type TimeSelection } from "./time-range";

const theme = createTheme({ colors: { mode: "dark" } });
type TableProps = ComponentProps<typeof Table>;

export default function GrafanaTable({
  panel,
  frames,
  values,
  range,
}: {
  panel: Panel;
  frames: DataFrame[];
  values: InterpolationValues;
  range: TimeSelection;
}) {
  const [selected, setSelected] = useState(0);
  const frameIndex = Math.min(selected, Math.max(0, frames.length - 1));
  const frame = frames[frameIndex];
  const host = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ width: 0, height: 0 });
  useLayoutEffect(() => {
    const element = host.current;
    if (!element) return;
    const measure = () => {
      const rect = element.getBoundingClientRect();
      setSize({
        width: Math.floor(rect.width),
        height: Math.floor(rect.height),
      });
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  const options = panel.config?.options || {};
  const footer = options.footer as TableProps["footerOptions"] | undefined;
  const pagination = options.enablePagination ?? footer?.enablePagination;
  return (
    <div className="grafana-sdk-table">
      {frames.length > 1 ? (
        <label className="grafana-table-frame-picker">
          {t("Query result")}
          <select
            aria-label={t("Table frame")}
            value={frameIndex}
            onChange={(event) => setSelected(Number(event.target.value))}
          >
            {frames.map((frame, index) => (
              <option key={index} value={index}>
                {frame.name ||
                  frame.refId ||
                  `${t("Query result")} ${index + 1}`}{" "}
                · {frame.length} {t("rows")}
              </option>
            ))}
          </select>
        </label>
      ) : null}
      <div className="grafana-table-viewport" ref={host}>
        {frame && size.width > 0 && size.height > 0 ? (
          <ThemeContext.Provider value={theme}>
            <Table
              key={`${frameIndex}:${frame.refId || ""}:${frame.name || ""}`}
              ariaLabel={panel.title}
              data={frame}
              width={size.width}
              height={size.height}
              columnMinWidth={
                typeof options.columnWidth === "number"
                  ? Math.max(40, options.columnWidth)
                  : undefined
              }
              noHeader={options.showHeader === false}
              showTypeIcons={options.showTypeIcons === true}
              initialSortBy={options.sortBy as TableProps["initialSortBy"]}
              footerOptions={footer}
              enablePagination={pagination === true}
              cellHeight={options.cellHeight as TableProps["cellHeight"]}
              fieldConfig={panel.config?.fieldConfig ?? undefined}
              timeRange={resolveTimeRange(range).sdk}
              replaceVariables={(text) => interpolate(text, values, range)}
            />
          </ThemeContext.Provider>
        ) : null}
      </div>
    </div>
  );
}
