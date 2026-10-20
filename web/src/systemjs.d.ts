declare module "systemjs/dist/system.js";
declare module "systemjs/dist/extras/amd.js";
declare module "systemjs/dist/extras/module-types.js";

interface Window {
  /** Set in index.html so @grafana/ui resolves icon assets from the site root. */
  __grafana_public_path__?: string;
}
