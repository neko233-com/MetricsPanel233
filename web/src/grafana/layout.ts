import type { Panel } from "../api";
import type { VariableValues } from "./engine";

export function layoutPanels(
  panels: Panel[],
  values: VariableValues,
  options: Record<string, string[]>,
) {
  const rows = new Map<number, number>();
  return panels.flatMap((panel, index) => {
    const pos = panel.config?.gridPos || {
      x: panel.visualization === "row" ? 0 : (index % 2) * 12,
      y: Math.floor(index / 2) * 12,
      w: panel.visualization === "row" ? 24 : 12,
      h: panel.visualization === "row" ? 1 : 12,
    };
    const x = Math.max(0, Math.min(23, Number(pos.x) || 0)),
      w = Math.max(1, Math.min(24 - x, Number(pos.w) || 12)),
      h = Math.max(1, Math.min(100, Number(pos.h) || 12));
    const originalY = Math.max(0, Math.min(10000, Number(pos.y) || 0)),
      y =
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
      values: repeat ? { ...values, [repeat]: choice } : values,
      x: x + (i % perRow) * w,
      y: y + Math.floor(i / perRow) * h,
      w,
      h,
    }));
  });
}
