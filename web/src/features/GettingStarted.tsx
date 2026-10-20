import { css } from "@emotion/css";
import { useEffect, useState } from "react";
import { api, type Metric, type Target } from "../api";
import { t as tr } from "../i18n";
import {
  Badge,
  Button,
  Icon,
  useStyles2,
  type AppTheme,
} from "../ui";

const hiddenKey = "metricspanel-getting-started-hidden";

type Step = {
  id: string;
  title: string;
  description: string;
  done: boolean;
  action: string;
  view: "collectors" | "explore" | "alerts";
};

export function GettingStarted({
  targets,
  metrics,
  tick,
  onNavigate,
}: {
  targets: Target[];
  metrics: Metric[];
  tick: number;
  onNavigate: (view: "collectors" | "explore" | "alerts") => void;
}) {
  const styles = useStyles2(getStyles);
  const [alerts, setAlerts] = useState<number | null>(null);
  const [hidden, setHidden] = useState(
    () => localStorage.getItem(hiddenKey) === "1",
  );

  useEffect(() => {
    const controller = new AbortController();
    api<unknown[]>("/alerts/rules", { signal: controller.signal })
      .then((rules) => setAlerts(rules.length))
      .catch(() => {
        // The guide is optional; an unavailable alert list must not block data.
      });
    return () => controller.abort();
  }, [tick]);

  if (hidden) return null;

  const steps: Step[] = [
    {
      id: "collector",
      title: tr("Connect a collector"),
      description: tr(
        "Scrape a Prometheus exporter or connect MySQL, Redis or PostgreSQL directly.",
      ),
      done: targets.length > 0,
      action: tr("Add collector"),
      view: "collectors",
    },
    {
      id: "data",
      title: tr("See live data"),
      description: tr(
        "Explore the metrics this workspace is already collecting.",
      ),
      done: metrics.length > 0,
      action: tr("Open Explore"),
      view: "explore",
    },
    {
      id: "alert",
      title: tr("Create your first alert"),
      description: tr(
        "Turn a PromQL query into a durable alert with a pending period.",
      ),
      done: (alerts || 0) > 0,
      action: tr("Create alert"),
      view: "alerts",
    },
  ];
  const complete = steps.filter((step) => step.done).length;

  return (
    <section className={styles.panel} aria-labelledby="getting-started-title">
      <header className={styles.header}>
        <div>
          <h2 id="getting-started-title">{tr("Getting started")}</h2>
          <p>
            {complete === steps.length
              ? tr("Everything is connected. This checklist can be dismissed.")
              : tr("Three steps to a working single-node setup.")}
          </p>
        </div>
        <div className={styles.headerActions}>
          <Badge
            text={`${complete}/${steps.length}`}
            color={complete === steps.length ? "green" : "orange"}
          />
          <Button
            size="sm"
            fill="text"
            onClick={() => {
              localStorage.setItem(hiddenKey, "1");
              setHidden(true);
            }}
          >
            {tr("Dismiss")}
          </Button>
        </div>
      </header>
      <ol className={styles.list}>
        {steps.map((step) => (
          <li className={styles.step} key={step.id}>
            <span
              className={step.done ? styles.doneIcon : styles.pendingIcon}
              aria-hidden="true"
            >
              <Icon name={step.done ? "check-circle" : "circle"} />
            </span>
            <div className={styles.copy}>
              <strong>{step.title}</strong>
              <span>{step.description}</span>
            </div>
            <Button
              size="sm"
              variant={step.done ? "secondary" : "primary"}
              fill={step.done ? "text" : "solid"}
              onClick={() => onNavigate(step.view)}
            >
              {step.done ? tr("Review") : step.action}
            </Button>
          </li>
        ))}
      </ol>
    </section>
  );
}

function getStyles(theme: AppTheme) {
  return {
    panel: css({
      marginBottom: theme.spacing(2),
      padding: theme.spacing(2),
      border: `1px solid ${theme.colors.border.weak}`,
      borderRadius: theme.shape.radius.default,
      background: theme.colors.background.secondary,
    }),
    header: css({
      display: "flex",
      alignItems: "flex-start",
      justifyContent: "space-between",
      gap: theme.spacing(2),
      marginBottom: theme.spacing(1.5),
      h2: {
        margin: 0,
        fontSize: theme.typography.h4.fontSize,
      },
      p: {
        margin: `${theme.spacing(0.5)} 0 0`,
        color: theme.colors.text.secondary,
      },
    }),
    headerActions: css({
      display: "flex",
      alignItems: "center",
      gap: theme.spacing(1),
      flexShrink: 0,
    }),
    list: css({
      display: "grid",
      gap: theme.spacing(1),
      margin: 0,
      padding: 0,
      listStyle: "none",
    }),
    step: css({
      display: "flex",
      alignItems: "center",
      gap: theme.spacing(1.5),
      padding: `${theme.spacing(1)} ${theme.spacing(1.5)}`,
      border: `1px solid ${theme.colors.border.weak}`,
      borderRadius: theme.shape.radius.default,
      background: theme.colors.background.primary,
    }),
    copy: css({
      display: "grid",
      gap: theme.spacing(0.25),
      flex: 1,
      minWidth: 0,
      span: {
        color: theme.colors.text.secondary,
      },
    }),
    doneIcon: css({
      display: "inline-flex",
      color: theme.colors.success.text,
      fontSize: theme.typography.body.fontSize,
    }),
    pendingIcon: css({
      display: "inline-flex",
      color: theme.colors.text.disabled,
      fontSize: theme.typography.body.fontSize,
    }),
  };
}
