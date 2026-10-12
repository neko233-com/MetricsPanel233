import { useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import { Grip, Maximize2, LayoutGrid } from "lucide-react";
import { api, ApiError, jsonBody, message, type Dashboard } from "../api";
import { t } from "../i18n";
import { Dialog } from "../components/Dialog";
import {
  changePanelPosition,
  panelPosition,
  type PanelPosition,
} from "../grafana/layout";
import "./dashboard-layout.css";

export function DashboardLayoutButton({
  onClick,
  disabled = false,
}: {
  onClick: () => void;
  disabled?: boolean;
}) {
  return (
    <button onClick={onClick} disabled={disabled}>
      <LayoutGrid size={17} />
      {t("Edit layout")}
    </button>
  );
}

type Gesture = {
  id: string;
  mode: "move" | "resize";
  x: number;
  y: number;
  column: number;
  row: number;
  positions: PanelPosition[];
  pointer: number;
  target: HTMLElement;
};

export function DashboardLayoutEditor({
  dashboard,
  reload,
  onClose,
}: {
  dashboard: Dashboard;
  reload: () => Promise<void>;
  onClose: () => void;
}) {
  const baseline = useRef({
    id: dashboard.id,
    revision: dashboard.updated_at,
    panels: dashboard.panels,
  });
  const initial = useRef(baseline.current.panels.map(panelPosition));
  const [positions, setPositions] = useState(initial.current);
  const [selected, setSelected] = useState(positions[0]?.id || "");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const canvas = useRef<HTMLDivElement>(null);
  const gesture = useRef<Gesture | null>(null);
  const current = positions.find((position) => position.id === selected);
  const selectedPanel = baseline.current.panels.find(
    (panel) => panel.id === selected,
  );
  const source = dashboard.grafana as
    | { dashboard?: unknown; spec?: { layout?: { kind?: string } } }
    | undefined;
  const layoutKind = ((source?.dashboard || source) as typeof source)?.spec
    ?.layout?.kind;
  const dirty = JSON.stringify(positions) !== JSON.stringify(initial.current);
  const outsideGrid = positions.some((position) => position.y > 10000);
  const panelsByID = new Map(
    baseline.current.panels.map((panel) => [panel.id, panel]),
  );
  const change = (id: string, patch: Partial<Omit<PanelPosition, "id">>) => {
    setError("");
    setPositions((old) => changePanelPosition(old, id, patch));
  };
  const finishGesture = () => {
    const active = gesture.current;
    gesture.current = null;
    if (active?.target.hasPointerCapture(active.pointer))
      active.target.releasePointerCapture(active.pointer);
  };
  const beginGesture = (
    event: PointerEvent<HTMLButtonElement>,
    id: string,
    mode: Gesture["mode"],
  ) => {
    if (saving || event.button !== 0 || !canvas.current) return;
    setSelected(id);
    setError("");
    const css = getComputedStyle(canvas.current);
    const gap = parseFloat(css.columnGap) || 0;
    gesture.current = {
      id,
      mode,
      x: event.clientX,
      y: event.clientY,
      column: (canvas.current.getBoundingClientRect().width + gap) / 24,
      row: (parseFloat(css.gridAutoRows) || 22) + (parseFloat(css.rowGap) || 0),
      positions,
      pointer: event.pointerId,
      target: event.currentTarget,
    };
    event.currentTarget.setPointerCapture(event.pointerId);
    event.preventDefault();
  };
  const moveGesture = (event: PointerEvent<HTMLButtonElement>) => {
    const active = gesture.current;
    if (!active || active.pointer !== event.pointerId) return;
    const position = active.positions.find((item) => item.id === active.id)!;
    const dx = Math.round((event.clientX - active.x) / active.column);
    const dy = Math.round((event.clientY - active.y) / active.row);
    setPositions(
      changePanelPosition(
        active.positions,
        active.id,
        active.mode === "move"
          ? { x: position.x + dx, y: position.y + dy }
          : {
              w: Math.min(24 - position.x, position.w + dx),
              h: position.h + dy,
            },
      ),
    );
  };
  const keyChange = (
    event: KeyboardEvent<HTMLButtonElement>,
    id: string,
    mode: Gesture["mode"],
  ) => {
    if (saving) return;
    const position = positions.find((item) => item.id === id)!;
    const delta =
      event.key === "ArrowLeft"
        ? [-1, 0]
        : event.key === "ArrowRight"
          ? [1, 0]
          : event.key === "ArrowUp"
            ? [0, -1]
            : event.key === "ArrowDown"
              ? [0, 1]
              : null;
    if (!delta) return;
    event.preventDefault();
    setSelected(id);
    const amount = event.ctrlKey ? 4 : 1;
    change(
      id,
      mode === "resize" || event.shiftKey
        ? {
            w: Math.min(24 - position.x, position.w + delta[0] * amount),
            h: position.h + delta[1] * amount,
          }
        : {
            x: position.x + delta[0] * amount,
            y: position.y + delta[1] * amount,
          },
    );
  };
  return (
    <Dialog
      title={t("Edit layout")}
      onClose={saving ? () => {} : onClose}
      style={{ width: "min(1280px, calc(100vw - 64px))" }}
    >
      <div
        className="layout-editor"
        onKeyDown={(event) => {
          if (event.key === "Escape" && gesture.current) {
            event.preventDefault();
            event.stopPropagation();
            setPositions(gesture.current.positions);
            finishGesture();
          }
        }}
      >
        <p id="layout-instructions" className="layout-help">
          {t(
            "Drag a panel to move it. Drag its lower right corner to resize it, or use the position fields. Arrow keys move; Shift + arrows resize.",
          )}
        </p>
        <div className="layout-editor-body">
          <div className="layout-canvas-scroll">
            <div
              ref={canvas}
              className="layout-canvas"
              role="group"
              aria-label={t("Panel layout")}
              aria-describedby="layout-instructions"
              style={{
                minHeight:
                  Math.max(
                    14,
                    ...positions.map((position) => position.y + position.h + 2),
                  ) * 28,
              }}
            >
              {positions.map((position) => {
                const panel = panelsByID.get(position.id)!;
                return (
                  <section
                    key={position.id}
                    className={`layout-tile${selected === position.id ? " is-selected" : ""}`}
                    aria-label={t(panel.title)}
                    onClick={() => setSelected(position.id)}
                    data-panel-id={position.id}
                    style={{
                      gridColumn: `${position.x + 1} / span ${position.w}`,
                      gridRow: `${position.y + 1} / span ${position.h}`,
                    }}
                  >
                    <button
                      className="layout-move"
                      aria-label={`${t("Move panel")}: ${t(panel.title)}`}
                      disabled={saving}
                      onPointerDown={(event) =>
                        beginGesture(event, position.id, "move")
                      }
                      onPointerMove={moveGesture}
                      onPointerUp={finishGesture}
                      onPointerCancel={() => {
                        if (gesture.current)
                          setPositions(gesture.current.positions);
                        finishGesture();
                      }}
                      onLostPointerCapture={finishGesture}
                      onFocus={() => setSelected(position.id)}
                      onKeyDown={(event) =>
                        keyChange(event, position.id, "move")
                      }
                    >
                      <Grip size={16} />
                      <span>{t(panel.title)}</span>
                    </button>
                    <span className="layout-coordinates">
                      {position.x}, {position.y} · {position.w} × {position.h}
                    </span>
                    <button
                      className="layout-resize"
                      aria-label={`${t("Resize panel")}: ${t(panel.title)}`}
                      disabled={saving}
                      onPointerDown={(event) =>
                        beginGesture(event, position.id, "resize")
                      }
                      onPointerMove={moveGesture}
                      onPointerUp={finishGesture}
                      onPointerCancel={() => {
                        if (gesture.current)
                          setPositions(gesture.current.positions);
                        finishGesture();
                      }}
                      onLostPointerCapture={finishGesture}
                      onFocus={() => setSelected(position.id)}
                      onKeyDown={(event) =>
                        keyChange(event, position.id, "resize")
                      }
                    >
                      <Maximize2 size={14} />
                    </button>
                  </section>
                );
              })}
            </div>
          </div>
          <aside className="layout-fields">
            <label>
              {t("Panel")}
              <select
                aria-label={t("Layout panel")}
                value={selected}
                disabled={saving || !positions.length}
                onChange={(event) => setSelected(event.target.value)}
              >
                {baseline.current.panels.map((panel) => (
                  <option key={panel.id} value={panel.id}>
                    {t(panel.title)}
                  </option>
                ))}
              </select>
            </label>
            {current ? (
              <>
                <div className="layout-position-fields">
                  {(
                    [
                      ["x", "Column (0–23)", 0, 24 - current.w],
                      ["y", "Row (0–10000)", 0, 10000],
                      ["w", "Width (columns)", 1, 24],
                      ["h", "Height (rows)", 1, 100],
                    ] as const
                  ).map(([key, label, min, max]) => (
                    <label key={key}>
                      {t(label)}
                      <input
                        type="number"
                        min={min}
                        max={max}
                        step="1"
                        value={current[key]}
                        disabled={saving}
                        onChange={(event) => {
                          if (
                            event.currentTarget.value !== "" &&
                            Number.isFinite(event.currentTarget.valueAsNumber)
                          )
                            change(current.id, {
                              [key]: event.currentTarget.valueAsNumber,
                            });
                        }}
                      />
                    </label>
                  ))}
                </div>
                <p className="layout-help">
                  {t(
                    "The grid has 24 columns. Panels that overlap are moved down.",
                  )}
                </p>
                {layoutKind === "RowsLayout" || layoutKind === "TabsLayout" ? (
                  <p className="layout-help">
                    {t("Rows and tabs keep their section membership.")}
                  </p>
                ) : null}
                {selectedPanel?.config?.repeat ? (
                  <p className="layout-help">
                    {t("Repeated panels use this base layout.")}
                  </p>
                ) : null}
              </>
            ) : (
              <p className="layout-help">
                {t("Add a panel before editing its layout.")}
              </p>
            )}
          </aside>
        </div>
        {error || outsideGrid ? (
          <p className="form-error" role="alert">
            {error ||
              t(
                "This layout exceeds row 10000. Move the panels up before saving.",
              )}
          </p>
        ) : null}
        <div className="form-actions">
          {conflict ? (
            <button
              disabled={saving}
              onClick={async () => {
                setSaving(true);
                try {
                  await reload();
                  onClose();
                } catch (e) {
                  setError(message(e));
                  setSaving(false);
                }
              }}
            >
              {t("Reload dashboard")}
            </button>
          ) : null}
          <button disabled={saving} onClick={onClose}>
            {t("Cancel")}
          </button>
          <button
            className="primary"
            disabled={saving || !dirty || outsideGrid || conflict}
            onClick={async () => {
              finishGesture();
              setSaving(true);
              setError("");
              try {
                await api<Dashboard>(
                  `/dashboards/${encodeURIComponent(baseline.current.id)}/layout`,
                  {
                    method: "PATCH",
                    headers: { "If-Match": String(baseline.current.revision) },
                    body: jsonBody({ panels: positions }),
                  },
                );
                await reload();
                onClose();
              } catch (e) {
                if (
                  e instanceof ApiError &&
                  (e.status === 409 || e.status === 412)
                ) {
                  setConflict(true);
                  setError(
                    t(
                      "Dashboard changed. Reload it before editing the layout again.",
                    ),
                  );
                } else setError(message(e));
              } finally {
                setSaving(false);
              }
            }}
          >
            {t(saving ? "Saving…" : "Save layout")}
          </button>
        </div>
      </div>
    </Dialog>
  );
}
