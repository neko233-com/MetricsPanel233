import { createContext, useContext } from "react";
import type { AuthUser } from "../api";

export type SessionState = {
  user: AuthUser | null;
  canEdit: boolean;
  canAdmin: boolean;
};

// The default keeps the legacy single-workspace token mode fully usable.
export const SessionContext = createContext<SessionState>({
  user: null,
  canEdit: true,
  canAdmin: true,
});

export const useSession = () => useContext(SessionContext);
