import { t } from "../i18n";
export function RefreshPicker({
  value,
  options = [],
  onChange,
}: {
  value: string;
  options?: string[];
  onChange: (value: string) => void;
}) {
  const choices = Array.from(
    new Set([
      "",
      "auto",
      value,
      ...(options.length
        ? options
        : ["5s", "10s", "30s", "1m", "5m", "15m", "1h", "1d"]),
    ]),
  );
  return (
    <select
      aria-label={t("Auto refresh")}
      title={t("Auto refresh")}
      value={value}
      onChange={(event) => onChange(event.target.value)}
    >
      {choices.map((choice) => (
        <option key={choice} value={choice}>
          {choice === "" ? t("Off") : choice === "auto" ? t("Auto") : choice}
        </option>
      ))}
    </select>
  );
}
