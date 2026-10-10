import { t as tr } from "../i18n";
import { useEffect, useRef, type ReactNode, type CSSProperties } from "react";
import { X } from "lucide-react";
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
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current!;
    dialog.showModal();
    dialog
      .querySelector<HTMLElement>(
        "[autofocus], input:not([disabled]), textarea:not([disabled]), select:not([disabled])",
      )
      ?.focus();
    return () => dialog.close();
  }, []);
  return (
    <dialog
      ref={ref}
      aria-label={title}
      style={style}
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      onClick={(e) => {
        if (e.target === ref.current) {
          const rect = ref.current!.getBoundingClientRect();
          if (
            e.clientX < rect.left ||
            e.clientX > rect.right ||
            e.clientY < rect.top ||
            e.clientY > rect.bottom
          )
            onClose();
        }
      }}
    >
      <div className="dialog-heading">
        <h2>{title}</h2>
        <button
          className="icon-button"
          aria-label={tr("Close dialog")}
          onClick={onClose}
        >
          <X size={20} />
        </button>
      </div>
      {children}
    </dialog>
  );
}
