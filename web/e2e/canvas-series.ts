import { expect, type Locator, type Page } from "@playwright/test";

type Stroke = { color: string; dash: number[]; minX: number; maxX: number; minY: number; maxY: number };

/** Observe real paint commands; no SDK state or query fixtures are substituted. */
export async function recordCanvasSeries(page: Page) {
  await page.addInitScript(() => {
    const paths = new WeakMap<Path2D, { minX: number; maxX: number; minY: number; maxY: number }>();
    const point = (path: Path2D, x: number, y: number) => {
      if (!Number.isFinite(x) || !Number.isFinite(y)) return;
      const bounds = paths.get(path) || { minX: x, maxX: x, minY: y, maxY: y };
      bounds.minX = Math.min(bounds.minX, x); bounds.maxX = Math.max(bounds.maxX, x);
      bounds.minY = Math.min(bounds.minY, y); bounds.maxY = Math.max(bounds.maxY, y);
      paths.set(path, bounds);
    };
    for (const method of ["moveTo", "lineTo"] as const) {
      const original = Path2D.prototype[method];
      Path2D.prototype[method] = function (x, y) { point(this, x, y); return original.call(this, x, y); };
    }
    const stroke = CanvasRenderingContext2D.prototype.stroke;
    CanvasRenderingContext2D.prototype.stroke = function (...args) {
      const bounds = args[0] instanceof Path2D ? paths.get(args[0]) : undefined;
      const canvas = this.canvas as HTMLCanvasElement & { seriesPaints?: unknown[] };
      if (bounds) (canvas.seriesPaints ||= []).push({ color: String(this.strokeStyle), dash: this.getLineDash(), ...bounds });
      return Reflect.apply(stroke, this, args);
    };
    const clear = CanvasRenderingContext2D.prototype.clearRect;
    CanvasRenderingContext2D.prototype.clearRect = function (x, y, width, height) {
      const canvas = this.canvas as HTMLCanvasElement & { seriesPaints?: unknown[] };
      if (x <= 0 && y <= 0 && width >= canvas.width && height >= canvas.height) canvas.seriesPaints = [];
      return clear.call(this, x, y, width, height);
    };
  });
}

export async function canvasSeries(plot: Locator): Promise<Stroke[]> {
  return plot.evaluate((element) => {
    const canvas = element.querySelector(".uplot canvas") as HTMLCanvasElement & { seriesPaints?: Stroke[] };
    if (!canvas) return [];
    const normalize = document.createElement("canvas").getContext("2d")!;
    const colors = Array.from(element.querySelectorAll('[data-testid="series-icon"]')).map((icon) => {
      const style = getComputedStyle(icon);
      const color = style.backgroundImage.match(/rgba?\([^)]*\)|#[\da-f]{6}/i)?.[0] || style.backgroundColor;
      normalize.fillStyle = color;
      return String(normalize.fillStyle);
    });
    const unique = new Map<string, Stroke>();
    for (const stroke of canvas.seriesPaints || []) {
      if (colors.includes(stroke.color) && stroke.maxX > stroke.minX) unique.set(JSON.stringify(stroke), stroke);
    }
    return Array.from(unique.values());
  });
}

type Timeline = {
  from: number;
  to: number;
  current: number[];
  compared: number[];
  diffMs: number;
};

export async function expectCanvasSeries(plot: Locator, count: number, comparison = false, timeline?: Timeline) {
  await expect(plot.locator(".uplot canvas")).toBeVisible();
  await expect(plot).toHaveAttribute("data-series-count", String(count));
  await expect.poll(async () => (await canvasSeries(plot)).length).toBe(count);
  const strokes = await canvasSeries(plot);
  if (comparison) {
    const solid = strokes.find((stroke) => stroke.dash.length === 0);
    const dashed = strokes.find((stroke) => stroke.dash.join(",") === "1,5,4,5");
    expect(solid).toBeTruthy();
    expect(dashed).toBeTruthy();
    if (timeline) {
      // Lookback and missing samples can make the two first valid points differ.
      // Check each real path against the raw query timestamps projected onto the
      // plot's current time scale, rather than requiring identical coverage.
      const geometry = await plot.evaluate((element) => {
        const canvas = element.querySelector('.uplot canvas') as HTMLCanvasElement;
        const area = element.querySelector('.u-over')!.getBoundingClientRect();
        const rect = canvas.getBoundingClientRect();
        const ratio = canvas.width / rect.width;
        return { left: (area.left - rect.left) * ratio, width: area.width * ratio };
      });
      for (const [stroke, timestamps, diff] of [
        [solid!, timeline.current, 0], [dashed!, timeline.compared, timeline.diffMs],
      ] as const) {
        expect(timestamps.length).toBeGreaterThan(1);
        const position = (time: number) => geometry.left +
          (time - diff - timeline.from) / (timeline.to - timeline.from) * geometry.width;
        expect(Math.abs(stroke.minX - position(Math.min(...timestamps)))).toBeLessThanOrEqual(1.5);
        expect(Math.abs(stroke.maxX - position(Math.max(...timestamps)))).toBeLessThanOrEqual(1.5);
      }
    } else {
      expect(dashed!.minX).toBeCloseTo(solid!.minX, 0);
      expect(dashed!.maxX).toBeCloseTo(solid!.maxX, 0);
    }
  }
  await expect.poll(() => plot.locator(".uplot canvas").evaluate((canvas, colors) => {
    const sampling = document.createElement("canvas");
    sampling.width = canvas.width;
    sampling.height = canvas.height;
    const ctx = sampling.getContext("2d", { willReadFrequently: true })!;
    ctx.drawImage(canvas, 0, 0);
    const pixels = ctx.getImageData(0, 0, canvas.width, canvas.height).data;
    return colors.every((color) => {
      if (!/^#[\da-f]{6}$/i.test(color)) return false;
      const channels = [1, 3, 5].map((offset) => parseInt(color.slice(offset, offset + 2), 16));
      let count = 0;
      for (let offset = 0; offset < pixels.length; offset += 4) {
        if (pixels[offset + 3] > 245 && channels.every((channel, i) => Math.abs(pixels[offset + i] - channel) < 10)) count++;
        if (count > 2) return true;
      }
      return false;
    });
  }, [...new Set(strokes.map((stroke) => stroke.color))])).toBe(true);
}
