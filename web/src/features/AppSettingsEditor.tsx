import { useEffect, useState } from "react";
import { api, jsonBody, message } from "../api";
import { Dialog } from "../components/Dialog";
import { t } from "../i18n";

type Settings = {
  enabled: boolean;
  pinned: boolean;
  jsonData: Record<string, unknown>;
  secureJsonFields: Record<string, boolean>;
  version: number;
};
export function AppSettingsEditor({
  id,
  name,
  close,
  saved,
}: {
  id: string;
  name: string;
  close: () => void;
  saved: () => void;
}) {
  const [settings, setSettings] = useState<Settings | null>(null),
    [data, setData] = useState("{}"),
    [secrets, setSecrets] = useState("{}"),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  useEffect(() => {
    let active = true;
    void api<Settings>(`/api/v1/plugins/${encodeURIComponent(id)}/app-settings`)
      .then((value) => {
        if (active) {
          setSettings(value);
          setData(JSON.stringify(value.jsonData, null, 2));
        }
      })
      .catch((error) => {
        if (active) setError(message(error));
      });
    return () => {
      active = false;
    };
  }, [id]);
  return (
    <Dialog title={`${t("Configure application")}: ${name}`} onClose={close}>
      <form
        onSubmit={async (event) => {
          event.preventDefault();
          if (!settings) return;
          setBusy(true);
          setError("");
          try {
            await api(
              `/api/v1/plugins/${encodeURIComponent(id)}/app-settings`,
              {
                method: "PUT",
                body: jsonBody({
                  enabled: settings.enabled,
                  pinned: settings.pinned,
                  version: settings.version,
                  secureJsonFields: settings.secureJsonFields,
                  jsonData: JSON.parse(data),
                  secureJsonData: JSON.parse(secrets),
                }),
              },
            );
            saved();
          } catch (error) {
            setError(message(error));
          } finally {
            setBusy(false);
          }
        }}
      >
        {settings && (
          <>
            <label className="checkbox-label">
              <input
                type="checkbox"
                checked={settings.enabled}
                onChange={(event) =>
                  setSettings({ ...settings, enabled: event.target.checked })
                }
              />
              {t("Enabled")}
            </label>
            <label className="checkbox-label">
              <input
                type="checkbox"
                checked={settings.pinned}
                onChange={(event) =>
                  setSettings({ ...settings, pinned: event.target.checked })
                }
              />
              {t("Pin application in navigation")}
            </label>
            <label>
              {t("Application JSON settings")}
              <textarea
                aria-label={t("Application JSON settings")}
                rows={7}
                value={data}
                onChange={(event) => setData(event.target.value)}
                spellCheck={false}
              />
            </label>
            <label>
              {t("Application secret settings")}
              <textarea
                aria-label={t("Application secret settings")}
                rows={3}
                value={secrets}
                onChange={(event) => setSecrets(event.target.value)}
                spellCheck={false}
              />
            </label>
            <p className="subtle">
              {t(
                "Omitted secrets are preserved. Saved values are encrypted and never returned here.",
              )}
            </p>
            {Object.entries(settings.secureJsonFields)
              .filter(([, present]) => present)
              .map(([key]) => (
                <div className="app-secret-field" key={key}>
                  <code>{key}</code>
                  <span>{t("Configured")}</span>
                  <button
                    type="button"
                    onClick={() =>
                      setSettings({
                        ...settings,
                        secureJsonFields: {
                          ...settings.secureJsonFields,
                          [key]: false,
                        },
                      })
                    }
                  >
                    {t("Clear secret")}
                  </button>
                </div>
              ))}
          </>
        )}
        {error && (
          <p role="alert" className="form-error">
            {error}
          </p>
        )}
        <div className="dialog-actions">
          <button type="button" onClick={close}>
            {t("Cancel")}
          </button>
          <button className="primary" disabled={!settings || busy}>
            {t("Save application")}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
