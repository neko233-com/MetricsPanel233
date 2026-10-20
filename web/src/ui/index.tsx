import {
  forwardRef,
  useEffect,
  useRef,
  type ButtonHTMLAttributes,
  type CSSProperties,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
} from "react";
import { createPortal } from "react-dom";
import {
  Circle,
  CircleAlert,
  CircleCheck,
  CircleX,
  Info,
  TriangleAlert,
  X,
} from "lucide-react";
import { t as tr } from "../i18n";

// Product UI primitives. Grafana components remain available only to the
// optional import/plugin compatibility layer, never to these pages.
export type AppTheme = {
  colors: {
    border: { weak: string; medium: string };
    background: { primary: string; secondary: string };
    text: { secondary: string; disabled: string };
    success: { text: string };
  };
  spacing: (value: number) => string;
  shape: { radius: { default: string } };
  typography: {
    h4: { fontSize: string };
    body: { fontSize: string };
    fontWeightMedium: number;
  };
};

export const appTheme: AppTheme = {
  colors: {
    border: { weak: "#303744", medium: "#3a4353" },
    background: { primary: "#181b23", secondary: "#1b2029" },
    text: { secondary: "#aebbd1", disabled: "#6f7c91" },
    success: { text: "#39d99c" },
  },
  spacing: (value) => `${value * 8}px`,
  shape: { radius: { default: "5px" } },
  typography: {
    h4: { fontSize: "20px" },
    body: { fontSize: "13px" },
    fontWeightMedium: 500,
  },
};

// Styles are pure functions of the theme, so no subscription is required.
export function useStyles2<T>(styles: (theme: AppTheme) => T): T {
  return styles(appTheme);
}

type ButtonVariant = "primary" | "secondary" | "destructive" | "success" | "accent";
type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: ButtonVariant;
  size?: "sm" | "md" | "lg";
  fill?: "solid" | "outline" | "text";
  icon?: ReactNode;
};

export function Button({
  variant = "secondary",
  size = "md",
  fill = "solid",
  icon,
  className = "",
  children,
  type = "button",
  ...rest
}: ButtonProps) {
  const classes = [
    "mp-button",
    variant === "primary" ? "primary" : "",
    variant === "destructive" ? "danger" : "",
    fill === "text" ? "text-button" : "",
    size === "sm" ? "compact" : "",
    className,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <button type={type} className={classes} {...rest}>
      {icon}
      {children}
    </button>
  );
}

type InputProps = InputHTMLAttributes<HTMLInputElement> & { invalid?: boolean };
export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { invalid, className = "", ...rest },
  ref,
) {
  return (
    <input
      ref={ref}
      className={`mp-input ${invalid ? "invalid" : ""} ${className}`}
      {...rest}
    />
  );
});

export function Field({
  label,
  required,
  children,
}: {
  label?: ReactNode;
  required?: boolean;
  children: ReactNode;
}) {
  return (
    <label className="mp-field">
      {label ? (
        <span className="mp-field-label">
          {label}
          {required ? <b aria-hidden="true"> *</b> : null}
        </span>
      ) : null}
      {children}
    </label>
  );
}

type AlertVariant = "success" | "warning" | "error" | "info";
export function Alert({
  severity = "info",
  title,
  children,
}: {
  severity?: AlertVariant;
  title: ReactNode;
  children?: ReactNode;
}) {
  const IconComponent =
    severity === "error"
      ? CircleAlert
      : severity === "warning"
        ? TriangleAlert
        : severity === "success"
          ? CircleCheck
          : Info;
  return (
    <div
      className={`mp-alert ${severity}`}
      role={severity === "error" || severity === "warning" ? "alert" : "status"}
    >
      <IconComponent size={18} aria-hidden="true" />
      <div>
        <strong>{title}</strong>
        {children}
      </div>
    </div>
  );
}

type BadgeColor = "blue" | "red" | "green" | "orange" | "purple" | "darkgrey" | "brand";
export function Badge({ text, color = "blue" }: { text?: ReactNode; color?: BadgeColor }) {
  return <span className={`mp-badge ${color}`}>{text}</span>;
}

export type SelectOption = { label: string; value: string };
export function Select({
  options,
  value,
  onChange,
  width,
  className = "",
  ...rest
}: {
  options: SelectOption[];
  value?: string;
  onChange?: (option: SelectOption) => void;
  width?: number;
  className?: string;
} & Omit<SelectHTMLAttributes<HTMLSelectElement>, "onChange" | "value" | "children">) {
  return (
    <select
      className={`mp-select ${className}`}
      value={value ?? ""}
      onChange={(event) => {
        const selected = options.find((option) => option.value === event.target.value);
        onChange?.(selected ?? { label: event.target.value, value: event.target.value });
      }}
      style={width ? { width: `${width * 8}px` } : undefined}
      {...rest}
    >
      {options.map((option) => (
        <option key={option.value} value={option.value}>
          {option.label}
        </option>
      ))}
    </select>
  );
}

export function Spinner() {
  return <span className="mp-spinner" role="status" aria-label={tr("Loading")} />;
}

const iconMap = {
  "check-circle": CircleCheck,
  circle: Circle,
  "times-circle": CircleX,
  info: Info,
  warning: TriangleAlert,
} as const;

export function Icon({
  name,
  size = 16,
}: {
  name: keyof typeof iconMap | string;
  size?: number;
}) {
  const IconComponent = iconMap[name as keyof typeof iconMap] ?? Circle;
  return <IconComponent size={size} aria-hidden="true" />;
}

export function AppModal({
  title,
  children,
  onClose,
  style,
  className = "",
}: {
  title: string;
  children: ReactNode;
  onClose: () => void;
  style?: CSSProperties;
  className?: string;
}) {
  const dialog = useRef<HTMLDivElement>(null);
  const close = useRef(onClose);
  close.current = onClose;
  useEffect(() => {
    const previous = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") close.current();
    };
    document.addEventListener("keydown", onKey);
    const frame = requestAnimationFrame(() => {
      dialog.current
        ?.querySelector<HTMLElement>(
          "[autofocus], input:not([disabled]), textarea:not([disabled]), select:not([disabled])",
        )
        ?.focus();
    });
    return () => {
      cancelAnimationFrame(frame);
      document.removeEventListener("keydown", onKey);
      document.body.style.overflow = previous;
    };
  }, []);
  return createPortal(
    <div
      className="mp-modal-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div
        ref={dialog}
        className={`mp-modal ${className}`}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        style={style}
      >
        <header className="mp-modal-header">
          <h2>{title}</h2>
          <button
            type="button"
            className="icon-button"
            aria-label={tr("Close dialog")}
            onClick={onClose}
          >
            <X size={19} />
          </button>
        </header>
        <div className="mp-modal-body">{children}</div>
      </div>
    </div>,
    document.body,
  );
}
