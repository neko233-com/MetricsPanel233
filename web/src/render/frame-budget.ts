// Cooperative frame budgeting. Long normalization work is split into small
// slices so cursor/legend interactions stay responsive. Each slice yields to
// the browser; callers can cancel with an AbortSignal.
export type FrameBudgetOptions = {
  /** Work items processed per animation frame. */
  perFrame?: number;
  /** Hard upper bound for one slice in milliseconds. */
  sliceMs?: number;
  signal?: AbortSignal;
};

export async function runInFrameSlices<T>(
  items: readonly T[],
  work: (item: T, index: number) => void,
  options: FrameBudgetOptions = {},
): Promise<void> {
  const perFrame = Math.max(1, options.perFrame ?? 32);
  const sliceMs = Math.max(1, options.sliceMs ?? 8);
  let index = 0;
  while (index < items.length) {
    if (options.signal?.aborted) throw new DOMException("aborted", "AbortError");
    const started = performance.now();
    let processed = 0;
    while (
      index < items.length &&
      processed < perFrame &&
      performance.now() - started < sliceMs
    ) {
      work(items[index], index);
      index += 1;
      processed += 1;
    }
    if (index < items.length) {
      await nextFrame();
    }
  }
}

/** Yield once when the current task has consumed its budget. */
export function nextFrame(): Promise<void> {
  return new Promise((resolve) => requestAnimationFrame(() => resolve()));
}
