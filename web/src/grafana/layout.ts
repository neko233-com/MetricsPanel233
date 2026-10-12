import type { Panel } from "../api";
import type { VariableValues } from "./engine";

export type PanelPosition = {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
};

function integer(value: unknown, fallback: number, min: number, max: number) {
  const number = Number(value);
  return Math.max(
    min,
    Math.min(max, Number.isFinite(number) ? Math.floor(number) : fallback),
  );
}

export function panelPosition(panel: Panel, index: number): PanelPosition {
  const row = panel.visualization === "row";
  const pos = panel.config?.gridPos;
  const x = integer(pos?.x ?? (row ? 0 : (index % 2) * 12), 0, 0, 23);
  return {
    id: panel.id,
    x,
    y: integer(pos?.y ?? Math.floor(index / 2) * 12, 0, 0, 10000),
    w: integer(pos?.w ?? (row ? 24 : 12), 12, 1, 24 - x),
    h: integer(pos?.h ?? (row ? 1 : 12), row ? 1 : 12, 1, 100),
  };
}

export function panelsOverlap(a: PanelPosition, b: PanelPosition) {
  return (
    a.x < b.x + b.w && a.x + a.w > b.x && a.y < b.y + b.h && a.y + a.h > b.y
  );
}

export function placeAddedPanel(panel: Panel, existing: Panel[]): Panel {
  const size = panelPosition(panel, existing.length);
  const positions = existing.map(panelPosition);
  const position = {
    ...size,
    x: size.w <= 12 ? (existing.length % 2) * 12 : 0,
    y: Math.floor(existing.length / 2) * 12,
  };
  if (positions.some((item) => panelsOverlap(position, item))) {
    position.x = 0;
    position.y = Math.max(0, ...positions.map((item) => item.y + item.h));
  }
  const { x, y, w, h } = position;
  return {
    ...panel,
    config: {
      type: panel.visualization || "timeseries",
      ...panel.config,
      gridPos: { x, y, w, h },
    },
  };
}

// Keep the selected panel at its requested position and move colliding panels
// down. Work from the pointer gesture's baseline to avoid cumulative drift.
export function changePanelPosition(
  positions: PanelPosition[],
  id: string,
  patch: Partial<Omit<PanelPosition, "id">>,
): PanelPosition[] {
  const original = positions.find((position) => position.id === id);
  if (!original) return positions;
  const w = integer(patch.w ?? original.w, original.w, 1, 24);
  const changed: PanelPosition = {
    id,
    w,
    x: integer(patch.x ?? original.x, original.x, 0, 24 - w),
    y: integer(patch.y ?? original.y, original.y, 0, 10000),
    h: integer(patch.h ?? original.h, original.h, 1, 100),
  };
  const placed = [changed];
  const others = positions
    .filter((position) => position.id !== id)
    .map((position) => ({ ...position }))
    .sort((a, b) => a.y - b.y || a.x - b.x);
  for (const position of others) {
    let collision = placed.find((item) => panelsOverlap(position, item));
    while (collision) {
      position.y = collision.y + collision.h;
      collision = placed.find((item) => panelsOverlap(position, item));
    }
    placed.push(position);
  }
  const byID = new Map(placed.map((position) => [position.id, position]));
  return positions.map((position) => byID.get(position.id)!);
}

export function layoutPanels(
  panels: Panel[],
  values: VariableValues,
  options: Record<string, string[]>,
) {
  const rows = new Map<number, number>();
  return panels.flatMap((panel, index) => {
    const { x, w, h, y: originalY } = panelPosition(panel, index);
    const y =
      originalY +
      [...rows]
        .filter(([row]) => row < originalY)
        .reduce((sum, [, shift]) => sum + shift, 0);
    const repeat = panel.config?.repeat,
      value = repeat ? values[repeat] : undefined;
    const choices = repeat
      ? value === "$__all" || (Array.isArray(value) && value.includes("$__all"))
        ? options[repeat] || []
        : Array.isArray(value)
          ? value
          : [value || ""]
      : [""];
    const copies = choices.slice(0, 64),
      horizontal = panel.config?.repeatDirection !== "v",
      perRow = horizontal
        ? Math.max(
            1,
            Math.min(
              panel.config?.maxPerRow || 4,
              copies.length,
              Math.floor((24 - x) / w),
            ),
          )
        : 1;
    const extra = Math.max(0, Math.ceil(copies.length / perRow) - 1) * h;
    rows.set(originalY, Math.max(rows.get(originalY) || 0, extra));
    return copies.map((choice, i) => ({
      panel: { ...panel, id: `${panel.id}-${i}` },
      sourcePanel: panel,
      values: repeat ? { ...values, [repeat]: choice } : values,
      x: x + (i % perRow) * w,
      y: y + Math.floor(i / perRow) * h,
      w,
      h,
    }));
  });
}
