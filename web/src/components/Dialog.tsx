import { useEffect, useRef, type ReactNode, type CSSProperties } from "react";
import { AppModal } from "../ui";

export function Dialog({
  title,
  children,
  onClose,
  style,
}: {
  title: string;
  children: ReactNode;
  onClose: () => void;
  style?: CSSProperties;
}) {
  const opened = useRef(false);
  useEffect(() => {
    if (opened.current) return;
    opened.current = true;
    // Match the previous native dialog behavior: focus the first usable field.
    const frame = requestAnimationFrame(() => {
      document
        .querySelector<HTMLElement>(
          "[role=dialog] [autofocus], [role=dialog] input:not([disabled]), [role=dialog] textarea:not([disabled]), [role=dialog] select:not([disabled])",
        )
        ?.focus();
    });
    return () => cancelAnimationFrame(frame);
  }, []);
  return (
    <AppModal title={title} onClose={onClose} style={style}>
      {children}
    </AppModal>
  );
}
