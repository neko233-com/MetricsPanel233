import {
  standardEditorsRegistry,
  type StandardEditorProps,
} from "@grafana/data";

// These editors are the host-side registry required by the public PanelPlugin
// options builder. Plugin-provided custom editors keep their own components.
function OptionEditor({ value, onChange, item }: StandardEditorProps) {
  const id = item.id;
  if (id === "boolean")
    return (
      <input
        type="checkbox"
        checked={Boolean(value)}
        onChange={(e) => onChange(e.target.checked)}
      />
    );
  if (id === "number" || id === "slider")
    return (
      <input
        type="number"
        value={value ?? ""}
        min={item.settings?.min}
        max={item.settings?.max}
        step={item.settings?.step}
        onChange={(e) =>
          onChange(e.target.value === "" ? undefined : e.target.valueAsNumber)
        }
      />
    );
  const options = item.settings?.options;
  if (
    (id === "select" || id === "radio" || id === "multi-select") &&
    Array.isArray(options)
  )
    return (
      <select
        value={value ?? ""}
        multiple={id === "multi-select"}
        onChange={(e) =>
          onChange(
            id === "multi-select"
              ? Array.from(
                  e.target.selectedOptions,
                  (o) =>
                    options.find((v) => String(v.value) === o.value)?.value,
                )
              : options.find((v) => String(v.value) === e.target.value)?.value,
          )
        }
      >
        {options.map((option: { value: unknown; label?: string }) => (
          <option key={String(option.value)} value={String(option.value)}>
            {option.label || String(option.value)}
          </option>
        ))}
      </select>
    );
  if (id === "strings")
    return (
      <textarea
        value={Array.isArray(value) ? value.join("\n") : ""}
        onChange={(e) => onChange(e.target.value.split("\n"))}
      />
    );
  return (
    <input
      value={
        typeof value === "string" ? value : value == null ? "" : String(value)
      }
      onChange={(e) => onChange(e.target.value)}
    />
  );
}

export function registerOptionEditors() {
  for (const id of [
    "number",
    "slider",
    "text",
    "strings",
    "select",
    "multi-select",
    "radio",
    "boolean",
    "color",
    "unit",
    "field-name",
    "stats-picker",
  ]) {
    if (!standardEditorsRegistry.getIfExists(id))
      standardEditorsRegistry.register({ id, name: id, editor: OptionEditor });
  }
}
