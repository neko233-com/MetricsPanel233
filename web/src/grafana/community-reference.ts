export function communityReference(reference: string): {
  id: number;
  revision?: number;
} {
  let value = reference.trim();
  if (/^(?:www\.)?grafana\.com\//.test(value)) value = `https://${value}`;
  if (/^[1-9]\d*$/.test(value)) {
    const id = Number(value);
    if (Number.isSafeInteger(id) && id <= 2147483647) return { id };
  }
  try {
    const url = new URL(value);
    if (
      url.protocol !== "https:" ||
      !["grafana.com", "www.grafana.com"].includes(url.hostname) ||
      url.port ||
      url.username ||
      url.password
    )
      throw new Error();
    const match =
      /^(?:\/grafana)?\/dashboards\/([1-9]\d*)(?:-[^/]*)?\/?$/.exec(
        url.pathname,
      ) ||
      /^\/api\/dashboards\/([1-9]\d*)\/revisions\/([1-9]\d*)\/download\/?$/.exec(
        url.pathname,
      );
    if (!match) throw new Error();
    const id = Number(match[1]),
      revision = match[2] ? Number(match[2]) : undefined;
    if (
      !Number.isSafeInteger(id) ||
      id > 2147483647 ||
      (revision && (!Number.isSafeInteger(revision) || revision > 2147483647))
    )
      throw new Error();
    return { id, revision };
  } catch {
    throw new Error("Enter a Grafana.com dashboard ID or HTTPS URL");
  }
}
