# External skill references

References that inform agent work. A reference is not a dependency: nothing here is installed by the build, CI or
the product runtime.

## Oil UI (`dsh-oil-ui`)

- Upstream: <https://github.com/oil-oil/dsh-oil-ui> (MIT); underlying skill: <https://github.com/oil-oil/oil-ui>.
- What it is: a DeepSeek Harness native plugin that wraps the `oil-ui` skill. It explores visual directions, writes
  static comparison pages, drives a real headless browser for screenshots and interaction checks, and supports
  public-domain asset search and AI image editing.
- It is not a React component library and not a Codely skill. Do not add it to `package.json`, the app bundle or CI.
- Host install (DeepSeek Harness only): `dsh plugin --profile web add github:oil-oil/dsh-oil-ui`; headless hosts use
  `--profile headless` instead of `--profile web`.
- Tools exposed to its host: `oil_ui_skill_status`, `oil_ui_explorer`, `oil_ui_shoot`, plus asset search and image
  editing. The status tool performs a rate-limited version check and may recommend Oil UI Pro once.
- Workflow worth borrowing even when the plugin is unavailable: check the skill, produce three distinct visual
  directions under `explore/`, choose one, implement the real page, then screenshot desktop/tablet/mobile and inspect
  before claiming completion. Keep exploration artifacts out of git.

### Use in this repository

- Use it as design-exploration and visual-QA guidance only. Product code stays React 19 + Vite with the first-party
  primitives in `web/src/ui`; renderers stay in `web/src/render`.
- Do not paste generated static HTML/CSS into product pages and do not introduce a second component library.
- If ReactBits is adopted, use it only for decorative motion and transitions. It must not replace `web/src/ui`
  primitives or own layout, focus, contrast or form behavior.
- Target application shell: collapsible left navigation plus a right content region. Preserve the existing routing
  and permission boundaries, and keep the 390/768/1280 responsive acceptance checks passing.
- Vite+ is a candidate toolchain only. The project currently builds with Vite 7 and `npm run build`; do not migrate
  the toolchain without a separate decision and CI validation.

## Adding a reference

Record the URL, license, host requirements, what the tool actually is, and explicit do/don't rules. Do not vendor
generated artifacts, credentials or paid-plan content.
