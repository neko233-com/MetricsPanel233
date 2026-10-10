import { lazy, Suspense, useState } from "react";
import { ListFilter } from "lucide-react";
import type { Dashboard, InterpolationValues } from "../api";
import type { TimeSelection } from "../grafana/time-range";
import { t } from "../i18n";
const Editor = lazy(() => import("./AnnotationQueriesEditor"));

export function AnnotationQueriesButton({
  dashboard,
  values = {},
  range,
  reload,
}: {
  dashboard: Dashboard;
  values?: InterpolationValues;
  range: TimeSelection;
  reload: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button onClick={() => setOpen(true)}>
        <ListFilter size={17} />
        {t("Annotation queries")}
      </button>
      {open && (
        <Suspense fallback={<p role="status">{t("Loading…")}</p>}>
          <Editor
            dashboard={dashboard}
            values={values}
            range={range}
            reload={reload}
            onClose={() => setOpen(false)}
          />
        </Suspense>
      )}
    </>
  );
}
