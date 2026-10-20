// Signature caches let renderers reuse normalized models across React renders
// and incremental refreshes. A signature must be cheap to compute and must
// change whenever any value that affects the drawing changes.
export type CacheEntry<T> = { signature: string; value: T };

export function reuseIfSame<T>(
  cache: { current: CacheEntry<T> | undefined },
  signature: string,
  build: () => T,
): T {
  if (cache.current?.signature === signature) return cache.current.value;
  const value = build();
  cache.current = { signature, value };
  return value;
}

export function frameSignature(frames: readonly { refId?: string; length?: number; fields?: readonly { name?: string }[] }[]): string {
  return frames
    .map(
      (frame) =>
        `${frame.refId || ""}:${frame.length || 0}:${(frame.fields || [])
          .map((field) => field.name || "")
          .join(",")}`,
    )
    .join("|");
}

/** Track append-only growth between refreshes without rescanning old rows. */
export function appendedRows(previous: number, current: number): number {
  return current >= previous ? current - previous : current;
}
