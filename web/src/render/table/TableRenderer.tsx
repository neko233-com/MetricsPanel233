import { useEffect, useMemo, useState } from "react";
import {
  createTheme,
  formattedValueToString,
  getDisplayProcessor,
  getFieldDisplayName,
  type DataFrame,
  type Field,
} from "@grafana/data";
import type { RendererProps } from "../types";
import { t } from "../../i18n";
import { resolveTimeRange } from "../../grafana/time-range";

const theme = createTheme({ colors: { mode: "dark" } });
const PAGE_SIZE = 100;

type SortState = { field: number; direction: 1 | -1 };
type FooterOptions = {
  show?: boolean;
  reducer?: string[];
  fields?: string[];
  enablePagination?: boolean;
};

function compareValues(left: unknown, right: unknown): number {
  if (left == null || right == null) return left == null ? (right == null ? 0 : -1) : 1;
  if (typeof left === "number" && typeof right === "number") return left - right;
  const a = left instanceof Date ? left.getTime() : String(left);
  const b = right instanceof Date ? right.getTime() : String(right);
  return a < b ? -1 : a > b ? 1 : 0;
}

function initialSort(
  options: Record<string, any>,
  fields: Field[],
  frame: DataFrame | undefined,
  frames: DataFrame[],
): SortState | undefined {
  const first = (options.sortBy as { displayName?: string; name?: string; desc?: boolean }[] | undefined)?.[0];
  if (!first || !frame) return undefined;
  const wanted = first.displayName || first.name;
  const field = fields.findIndex(
    (candidate) => getFieldDisplayName(candidate, frame, frames) === wanted || candidate.name === wanted,
  );
  return field >= 0 ? { field, direction: first.desc ? -1 : 1 } : undefined;
}

export function TableRenderer({ panel, frames, range }: RendererProps) {
  const [selectedFrame, setSelectedFrame] = useState(0);
  const [page, setPage] = useState(0);
  const [sort, setSort] = useState<SortState | undefined>();
  const frameIndex = Math.min(selectedFrame, Math.max(0, frames.length - 1));
  const frame = frames[frameIndex];
  const timeZone = resolveTimeRange(range).timezone;
  const options = (panel.config?.options || {}) as Record<string, any>;
  const footer = options.footer as FooterOptions | undefined;
  const fields = frame?.fields || [];
  const processors = useMemo(
    () =>
      fields.map(
        (field) =>
          field.display ||
          getDisplayProcessor({
            field: field as Field,
            timeZone,
            theme,
          }),
      ),
    [fields, timeZone],
  );
  const rows = useMemo(() => {
    const indices = Array.from({ length: frame?.length || 0 }, (_, index) => index);
    if (!sort || !fields[sort.field]) return indices;
    return indices.sort(
      (left, right) =>
        sort.direction *
        compareValues(fields[sort.field].values[left], fields[sort.field].values[right]),
    );
  }, [fields, frame?.length, sort]);
  const pagination =
    options.enablePagination === true || footer?.enablePagination === true || (frame?.length || 0) > PAGE_SIZE;
  const pageCount = Math.max(1, Math.ceil(rows.length / PAGE_SIZE));
  const defaultSort = useMemo(
    () => initialSort(options, fields, frame, frames),
    [options, fields, frame, frames],
  );

  useEffect(() => {
    setPage(0);
    setSort(defaultSort);
  }, [frameIndex, defaultSort]);
  useEffect(() => {
    setPage((current) => Math.min(current, pageCount - 1));
  }, [pageCount]);

  function cellText(fieldIndex: number, rowIndex: number) {
    const field = fields[fieldIndex];
    const value = field.values[rowIndex];
    if (value == null) return "—";
    const display = processors[fieldIndex]?.(value);
    return display ? formattedValueToString(display) : String(value);
  }

  function footerValue(fieldIndex: number) {
    const values = rows
      .map((rowIndex) => fields[fieldIndex]?.values[rowIndex])
      .filter((value): value is number => typeof value === "number" && Number.isFinite(value));
    if (!values.length) return "";
    const reducer = footer?.reducer?.[0] || "sum";
    switch (reducer) {
      case "mean":
      case "avg":
        return String(values.reduce((sum, value) => sum + value, 0) / values.length);
      case "min":
        return String(Math.min(...values));
      case "max":
        return String(Math.max(...values));
      case "count":
        return String(values.length);
      case "last":
        return String(values[values.length - 1]);
      default:
        return String(values.reduce((sum, value) => sum + value, 0));
    }
  }

  const footerFields = footer?.show === false
    ? []
    : footer
      ? fields
          .map((field, index) => ({ field, index }))
          .filter(({ field, index }) => {
            if (!footer.fields?.length) return field.type === "number";
            return (
              footer.fields.includes(getFieldDisplayName(field, frame!, frames)) ||
              footer.fields.includes(field.name || "") ||
              footer.fields.includes(String(index))
            );
          })
      : [];

  return (
    <section className="native-data-table">
      {frames.length > 1 ? (
        <label className="native-data-table-frame-picker">
          {t("Query result")}
          <select
            aria-label={t("Table frame")}
            value={frameIndex}
            onChange={(event) => {
              setSelectedFrame(Number(event.target.value));
              setPage(0);
            }}
          >
            {frames.map((item, index) => (
              <option key={index} value={index}>
                {item.name || item.refId || `${t("Query result")} ${index + 1}`} · {item.length} {t("rows")}
              </option>
            ))}
          </select>
        </label>
      ) : null}
      <div className="native-data-table-scroll">
        {frame ? (
          <table aria-label={t(panel.title)} className="native-data-table-grid">
            {options.showHeader === false ? null : (
              <thead>
                <tr>
                  {fields.map((field, index) => {
                    const name = getFieldDisplayName(field, frame, frames);
                    const active = sort?.field === index;
                    return (
                      <th
                        key={field.state?.displayName || field.name || index}
                        scope="col"
                        aria-sort={
                          active ? (sort.direction === 1 ? "ascending" : "descending") : "none"
                        }
                        style={{
                          minWidth:
                            typeof options.columnWidth === "number"
                              ? Math.max(40, options.columnWidth)
                              : undefined,
                        }}
                      >
                        <button
                          type="button"
                          className="native-data-table-sort"
                          aria-label={
                            active && sort.direction === -1
                              ? `${t("Sort by column")} ${name}, ${t("descending")}`
                              : `${t("Sort by column")} ${name}`
                          }
                          onClick={() =>
                            setSort((current) =>
                              current?.field === index
                                ? { field: index, direction: current.direction === 1 ? -1 : 1 }
                                : { field: index, direction: 1 },
                            )
                          }
                        >
                          {name}
                          {active ? (sort.direction === 1 ? " ↑" : " ↓") : null}
                        </button>
                      </th>
                    );
                  })}
                </tr>
              </thead>
            )}
            <tbody>
              {rows.map((rowIndex, position) => {
                const hidden =
                  pagination &&
                  (position < page * PAGE_SIZE || position >= (page + 1) * PAGE_SIZE);
                return (
                  <tr key={rowIndex} hidden={hidden}>
                    {fields.map((field, fieldIndex) => {
                      const formatted = processors[fieldIndex]?.(field.values[rowIndex]);
                      return (
                        <td
                          key={field.state?.displayName || field.name || fieldIndex}
                          title={cellText(fieldIndex, rowIndex)}
                          style={formatted?.color ? { color: formatted.color } : undefined}
                        >
                          {cellText(fieldIndex, rowIndex)}
                        </td>
                      );
                    })}
                  </tr>
                );
              })}
            </tbody>
            {footerFields.length ? (
              <tfoot>
                <tr>
                  {fields.map((field, index) => {
                    const footerField = footerFields.find((candidate) => candidate.index === index);
                    return (
                      <td key={field.state?.displayName || field.name || index}>
                        {footerField ? footerValue(index) : ""}
                      </td>
                    );
                  })}
                </tr>
              </tfoot>
            ) : null}
          </table>
        ) : (
          <p className="native-data-table-empty" role="status">
            {t("No samples in this time range")}
          </p>
        )}
      </div>
      {pagination && rows.length > PAGE_SIZE ? (
        <nav className="native-data-table-pagination" aria-label={t("Page")}>
          <span>
            {t("Page")} {page + 1} / {pageCount} · {rows.length} {t("rows")}
          </span>
          <button type="button" disabled={page === 0} onClick={() => setPage((current) => current - 1)}>
            {t("Previous")}
          </button>
          {Array.from({ length: pageCount }, (_, index) => (
            <button
              type="button"
              key={index}
              aria-current={index === page ? "page" : undefined}
              onClick={() => setPage(index)}
            >
              {index + 1}
            </button>
          ))}
          <button
            type="button"
            disabled={page + 1 >= pageCount}
            onClick={() => setPage((current) => Math.min(pageCount - 1, current + 1))}
          >
            {t("Next")}
          </button>
        </nav>
      ) : null}
    </section>
  );
}

export default TableRenderer;
