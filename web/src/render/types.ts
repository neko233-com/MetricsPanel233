import type { ComponentType, ReactNode } from "react";
import type { DataFrame } from "@grafana/data";
import type { Dashboard, InterpolationValues, Panel } from "../api";
import type { Annotation } from "../grafana/annotations";
import type { TimeSelection } from "../grafana/time-range";

export type RendererKind =
  | "timeseries"
  | "table"
  | "stat"
  | "gauge"
  | "bargauge"
  | "logs"
  | "traces"
  | "heatmap"
  | "histogram"
  | "pie"
  | "candlestick"
  | "geomap"
  | "nodegraph"
  | "state-timeline"
  | "status-history"
  | "text"
  | "vector"
  | (string & {});

export type RendererProps = {
  panel: Panel;
  frames: DataFrame[];
  values: InterpolationValues;
  range: TimeSelection;
  dashboard?: Dashboard;
  streaming?: boolean;
  loading?: boolean;
  timeInfo?: string;
  controls?: ReactNode;
  annotations?: Annotation[];
  annotationDashboard?: Dashboard;
  annotationError?: string;
  queryError?: string;
  onRange?: (range: TimeSelection) => void;
  onEdit?: () => void;
  onRemove?: () => void;
};

export type RendererDefinition = {
  kind: RendererKind;
  label: string;
  capabilities: {
    compare?: boolean;
    stack?: boolean;
    streaming?: boolean;
    vector?: boolean;
  };
  component: ComponentType<RendererProps>;
};
