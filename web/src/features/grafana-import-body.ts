type ImportSettings = {
  name?: string;
  uid?: string;
  inputs?: Record<string, string>;
  folderUid?: string;
};

export function grafanaImportBody(
  dashboard: string,
  settings: ImportSettings = {},
): string {
  // Validate syntax without reserializing unknown numbers in the dashboard.
  JSON.parse(dashboard);
  const fields = JSON.stringify(settings).slice(1, -1);
  return `{"dashboard":${dashboard}${fields ? `,${fields}` : ""}}`;
}
