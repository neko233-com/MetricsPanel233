import { useState, type ReactNode } from "react";
import {
  ChevronRight,
  Folder as FolderIcon,
  FolderPlus,
  Move,
  Pencil,
  Trash2,
} from "lucide-react";
import { api, jsonBody, message, type Folder } from "../api";
import { Dialog } from "../components/Dialog";
import { t } from "../i18n";

export function folderTrail(uid: string, folders: Folder[]): Folder[] {
  const trail: Folder[] = [],
    seen = new Set<string>();
  let current = uid;
  while (current && !seen.has(current)) {
    seen.add(current);
    const folder = folders.find((item) => item.uid === current);
    if (!folder) break;
    trail.unshift(folder);
    current = folder.parentUid || "";
  }
  return trail;
}

export function FolderSelect({
  folders,
  value,
  onChange,
  disabled,
  excluded = [],
}: {
  folders: Folder[];
  value: string;
  onChange: (uid: string) => void;
  disabled?: boolean;
  excluded?: string[];
}) {
  return (
    <select
      value={value}
      disabled={disabled}
      onChange={(event) => onChange(event.target.value)}
    >
      <option value="">{t("General")}</option>
      {folders
        .filter((folder) => !excluded.includes(folder.uid))
        .map((folder) => (
          <option key={folder.uid} value={folder.uid}>
            {folderTrail(folder.uid, folders)
              .map((item) => item.title)
              .join(" / ")}
          </option>
        ))}
    </select>
  );
}

type Action = {
  kind: "create" | "rename" | "move" | "delete";
  folder?: Folder;
  parent: string;
};
export function Folders({
  folders,
  selected,
  select,
  changed,
  notify,
}: {
  folders: Folder[];
  selected: string | null;
  select: (uid: string | null) => void;
  changed: () => Promise<void>;
  notify: (text: string, error?: boolean) => void;
}) {
  const [action, setAction] = useState<Action | null>(null);
  const tree = (parent: string, ancestors: Set<string>): ReactNode =>
    folders
      .filter(
        (folder) =>
          (folder.parentUid || "") === parent && !ancestors.has(folder.uid),
      )
      .map((folder) => (
        <li key={folder.uid}>
          <div className="folder-tree-row">
            <button
              className="folder-tree-title"
              aria-current={selected === folder.uid ? "page" : undefined}
              onClick={() => select(folder.uid)}
            >
              <FolderIcon size={15} />
              <span>{folder.title}</span>
            </button>
            <div className="folder-tree-actions">
              <button
                className="icon-button"
                disabled={folder.canEdit === false}
                aria-label={`${t("Rename folder")} ${folder.title}`}
                onClick={() => setAction({ kind: "rename", folder, parent })}
              >
                <Pencil size={13} />
              </button>
              <button
                className="icon-button"
                disabled={folder.canEdit === false}
                aria-label={`${t("Move folder")} ${folder.title}`}
                onClick={() => setAction({ kind: "move", folder, parent })}
              >
                <Move size={13} />
              </button>
              <button
                className="icon-button danger-hover"
                disabled={folder.canAdmin === false}
                aria-label={`${t("Delete folder")} ${folder.title}`}
                onClick={() => setAction({ kind: "delete", folder, parent })}
              >
                <Trash2 size={13} />
              </button>
            </div>
          </div>
          <ul>{tree(folder.uid, new Set([...ancestors, folder.uid]))}</ul>
        </li>
      ));
  return (
    <>
      <nav className="folder-tree" aria-label={t("Folder navigation")}>
        <div className="folder-tree-heading">
          <strong>{t("Folders")}</strong>
          <button
            className="icon-button"
            aria-label={t("New folder")}
            onClick={() =>
              setAction({ kind: "create", parent: selected || "" })
            }
          >
            <FolderPlus size={17} />
          </button>
        </div>
        <button
          className="folder-tree-root"
          aria-current={selected === null ? "page" : undefined}
          onClick={() => select(null)}
        >
          {t("All dashboards")}
        </button>
        <button
          className="folder-tree-root"
          aria-current={selected === "" ? "page" : undefined}
          onClick={() => select("")}
        >
          <FolderIcon size={15} />
          {t("General")}
        </button>
        <ul>{tree("", new Set())}</ul>
      </nav>
      {action && (
        <FolderDialog
          key={`${action.kind}-${action.folder?.uid || action.parent}`}
          action={action}
          folders={folders}
          close={() => setAction(null)}
          saved={async () => {
            await changed();
            if (action.kind === "delete") select(action.parent);
            setAction(null);
            notify(
              t(action.kind === "delete" ? "Folder deleted" : "Folder saved"),
            );
          }}
        />
      )}
    </>
  );
}

function FolderDialog({
  action,
  folders,
  close,
  saved,
}: {
  action: Action;
  folders: Folder[];
  close: () => void;
  saved: () => Promise<void>;
}) {
  const [title, setTitle] = useState(action.folder?.title || ""),
    [parent, setParent] = useState(action.parent);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const [deleteRules, setDeleteRules] = useState(false);
  const excluded = action.folder
    ? folders
        .filter((folder) =>
          folderTrail(folder.uid, folders).some(
            (item) => item.uid === action.folder!.uid,
          ),
        )
        .map((folder) => folder.uid)
    : [];
  const caption =
    action.kind === "create"
      ? "New folder"
      : action.kind === "rename"
        ? "Rename folder"
        : action.kind === "move"
          ? "Move folder"
          : "Delete folder";
  return (
    <Dialog
      title={t(caption)}
      onClose={() => {
        if (!busy) close();
      }}
    >
      <form
        onSubmit={async (event) => {
          event.preventDefault();
          setBusy(true);
          setError("");
          try {
            const uid = encodeURIComponent(action.folder?.uid || "");
            if (action.kind === "create")
              await api("/api/folders", {
                method: "POST",
                body: jsonBody({ title, parentUid: parent }),
              });
            else if (action.kind === "rename") {
              await api(`/api/folders/${uid}`, {
                method: "PUT",
                body: jsonBody({ title, version: action.folder!.version }),
              });
            } else if (action.kind === "move")
              await api(`/api/folders/${uid}/move`, {
                method: "POST",
                body: jsonBody({ parentUid: parent }),
              });
            else
              await api(
                `/api/folders/${uid}${deleteRules ? "?forceDeleteRules=true" : ""}`,
                { method: "DELETE" },
              );
            await saved();
          } catch (cause) {
            setError(message(cause));
          } finally {
            setBusy(false);
          }
        }}
      >
        {action.kind === "delete" ? (
          <>
            <p>
              <strong>{action.folder?.title}</strong>
            </p>
            <p>
              {t(
                "Deleting this folder also deletes its subfolders, dashboards and unconnected library panels.",
              )}
            </p>
            <label className="checkbox-label">
              <input
                type="checkbox"
                checked={deleteRules}
                onChange={(event) => setDeleteRules(event.target.checked)}
                disabled={busy}
              />
              {t("Delete alert rules in this folder")}
            </label>
          </>
        ) : action.kind !== "move" ? (
          <label>
            {t("Folder name")}
            <input
              value={title}
              onChange={(event) => setTitle(event.target.value)}
              required
              maxLength={100}
              disabled={busy}
            />
          </label>
        ) : (
          <p>{action.folder?.title}</p>
        )}
        {(action.kind === "create" || action.kind === "move") && (
          <label>
            {t("Parent folder")}
            <FolderSelect
              folders={folders}
              value={parent}
              onChange={setParent}
              excluded={excluded}
              disabled={busy}
            />
          </label>
        )}
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <div className="form-actions">
          <button type="button" disabled={busy} onClick={close}>
            {t("Cancel")}
          </button>
          <button
            className={action.kind === "delete" ? "danger" : "primary"}
            disabled={busy}
          >
            {t(
              busy
                ? "Saving…"
                : action.kind === "delete"
                  ? "Delete folder"
                  : "Save folder",
            )}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

export function FolderBreadcrumbs({
  folders,
  selected,
  select,
}: {
  folders: Folder[];
  selected: string | null;
  select: (uid: string | null) => void;
}) {
  return (
    <nav className="folder-breadcrumbs" aria-label={t("Folder path")}>
      <button onClick={() => select(null)}>{t("All dashboards")}</button>
      {selected !== null && (
        <>
          <ChevronRight size={14} />
          <button onClick={() => select("")}>{t("General")}</button>
        </>
      )}
      {selected &&
        folderTrail(selected, folders).map((folder) => (
          <span key={folder.uid}>
            <ChevronRight size={14} />
            <button onClick={() => select(folder.uid)}>{folder.title}</button>
          </span>
        ))}
    </nav>
  );
}
