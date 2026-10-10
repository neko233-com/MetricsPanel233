import { RotateCcw } from "lucide-react";
import { t } from "../i18n";
export function PanelZoomControls({
  scope,
  active,
  onScope,
  onReset,
}: {
  scope: "dashboard" | "panel";
  active: boolean;
  onScope: (scope: "dashboard" | "panel") => void;
  onReset: () => void;
}) {
  return (
    <div className="panel-zoom-controls">
      <select
        aria-label={t("Zoom scope")}
        title={t("Zoom scope")}
        value={scope}
        onChange={(event) =>
          onScope(event.target.value as "dashboard" | "panel")
        }
      >
        <option value="dashboard">{t("Dashboard")}</option>
        <option value="panel">{t("This panel")}</option>
      </select>
      {active && (
        <button
          className="icon-button"
          aria-label={t("Reset panel zoom")}
          title={t("Reset panel zoom")}
          onClick={onReset}
        >
          <RotateCcw size={15} />
        </button>
      )}
    </div>
  );
}
