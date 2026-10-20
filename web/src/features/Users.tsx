import { css } from "@emotion/css";
import { useCallback, useEffect, useState } from "react";
import { UserPlus } from "lucide-react";
import {
  api,
  jsonBody,
  message,
  type AuthUser,
} from "../api";
import { t as tr } from "../i18n";
import { Dialog } from "../components/Dialog";
import {
  Alert,
  Badge,
  Button,
  Field,
  Input,
  Select,
  Spinner,
  useStyles2,
  type AppTheme,
} from "../ui";

type AdminUser = AuthUser & {
  membershipVersion: number;
  version: number;
  disabled: boolean;
};

const roles = [
  { label: "Viewer", value: "Viewer" },
  { label: "Editor", value: "Editor" },
  { label: "Admin", value: "Admin" },
];

export function Users({
  notify,
  currentUser,
}: {
  notify: (text: string, error?: boolean) => void;
  currentUser: AuthUser;
}) {
  const styles = useStyles2(getStyles);
  const [users, setUsers] = useState<AdminUser[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState({
    login: "",
    name: "",
    email: "",
    password: "",
    role: "Viewer",
  });

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setUsers(await api<AdminUser[]>("/api/admin/users"));
      setError("");
    } catch (cause) {
      setError(message(cause));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const create = async () => {
    try {
      await api("/api/admin/users", {
        method: "POST",
        body: jsonBody(form),
      });
      setCreating(false);
      setForm({ login: "", name: "", email: "", password: "", role: "Viewer" });
      notify(tr("User created"));
      await load();
    } catch (cause) {
      setError(message(cause));
    }
  };

  const changeRole = async (user: AdminUser, role: string) => {
    try {
      await api(`/api/admin/users/${user.id}`, {
        method: "PUT",
        body: jsonBody({
          version: user.version,
          membershipVersion: user.membershipVersion,
          role,
        }),
      });
      notify(tr("Role updated"));
      await load();
    } catch (cause) {
      setError(message(cause));
    }
  };

  const toggleDisabled = async (user: AdminUser) => {
    try {
      await api(`/api/admin/users/${user.id}`, {
        method: "PUT",
        body: jsonBody({
          version: user.version,
          membershipVersion: user.membershipVersion,
          disabled: !user.disabled,
        }),
      });
      notify(user.disabled ? tr("User enabled") : tr("User disabled"));
      await load();
    } catch (cause) {
      setError(message(cause));
    }
  };

  return (
    <>
      <div className="page-heading">
        <div>
          <h1>{tr("Users")}</h1>
          <p>
            {users.length} {tr("accounts")} ·{" "}
            {tr("Roles apply to the whole workspace.")}
          </p>
        </div>
        <Button variant="primary" onClick={() => setCreating(true)}>
          <UserPlus size={18} />
          {tr("Create user")}
        </Button>
      </div>
      {error && <Alert severity="error" title={error} />}
      {loading ? (
        <Spinner />
      ) : (
        <table className={styles.table}>
          <thead>
            <tr>
              <th>{tr("Login")}</th>
              <th>{tr("Name")}</th>
              <th>{tr("Email")}</th>
              <th>{tr("Role")}</th>
              <th>{tr("Status")}</th>
              <th aria-label={tr("Actions")} />
            </tr>
          </thead>
          <tbody>
            {users.map((user) => (
              <tr key={user.id}>
                <td>{user.login}</td>
                <td>{user.name}</td>
                <td>{user.email || "—"}</td>
                <td>
                  {user.id === currentUser.id ? (
                    <Badge text={user.role} color="blue" />
                  ) : (
                    <Select
                      aria-label={`${tr("Role")} ${user.login}`}
                      options={roles}
                      value={user.role}
                      width={14}
                      onChange={(option) =>
                        option && void changeRole(user, String(option.value))
                      }
                    />
                  )}
                </td>
                <td>
                  <Badge
                    text={user.disabled ? tr("Disabled") : tr("Active")}
                    color={user.disabled ? "red" : "green"}
                  />
                </td>
                <td>
                  {user.id === currentUser.id ? null : (
                    <Button
                      size="sm"
                      variant={user.disabled ? "secondary" : "destructive"}
                      fill="text"
                      onClick={() => void toggleDisabled(user)}
                    >
                      {user.disabled ? tr("Enable") : tr("Disable")}
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {creating && (
        <Dialog title={tr("Create user")} onClose={() => setCreating(false)}>
          <div className={styles.form}>
            <Field label={tr("Login")} required>
              <Input
                autoFocus
                aria-label={tr("Login")}
                value={form.login}
                onChange={(e) => setForm({ ...form, login: e.target.value })}
                autoComplete="off"
              />
            </Field>
            <Field label={tr("Name")} required>
              <Input
                aria-label={tr("Name")}
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                autoComplete="off"
              />
            </Field>
            <Field label={tr("Email")}>
              <Input
                type="email"
                aria-label={tr("Email")}
                value={form.email}
                onChange={(e) => setForm({ ...form, email: e.target.value })}
                autoComplete="off"
              />
            </Field>
            <Field label={tr("Password")} required>
              <Input
                type="password"
                aria-label={tr("Password")}
                value={form.password}
                minLength={12}
                onChange={(e) => setForm({ ...form, password: e.target.value })}
                autoComplete="new-password"
              />
            </Field>
            <Field label={tr("Role")} required>
              <Select
                aria-label={tr("Role")}
                options={roles}
                value={form.role}
                onChange={(option) =>
                  option && setForm({ ...form, role: String(option.value) })
                }
              />
            </Field>
            <div className={styles.actions}>
              <Button variant="secondary" onClick={() => setCreating(false)}>
                {tr("Cancel")}
              </Button>
              <Button
                variant="primary"
                disabled={!form.login || !form.name || form.password.length < 12}
                onClick={() => void create()}
              >
                {tr("Create user")}
              </Button>
            </div>
          </div>
        </Dialog>
      )}
    </>
  );
}

function getStyles(theme: AppTheme) {
  return {
    table: css({
      width: "100%",
      borderCollapse: "collapse",
      th: {
        textAlign: "left",
        padding: theme.spacing(1),
        borderBottom: `1px solid ${theme.colors.border.medium}`,
        color: theme.colors.text.secondary,
        fontWeight: theme.typography.fontWeightMedium,
      },
      td: {
        padding: theme.spacing(1),
        borderBottom: `1px solid ${theme.colors.border.weak}`,
        verticalAlign: "middle",
      },
    }),
    form: css({
      display: "grid",
      gap: theme.spacing(1),
      minWidth: 360,
    }),
    actions: css({
      display: "flex",
      justifyContent: "flex-end",
      gap: theme.spacing(1),
      marginTop: theme.spacing(1),
    }),
  };
}
