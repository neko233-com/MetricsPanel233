import { useEffect, useState } from "react";
import { BrowserRouter } from "react-router";
import { PluginExtensionPoints, type PluginExtensionLink } from "@grafana/data";
import { X } from "lucide-react";
import { Dialog } from "../components/Dialog";
import { t } from "../i18n";
import { sdkRuntime } from "./plugin-runtime";
import {
  useExtensionLinks,
  useExtensionComponents,
  useExtensionOverlays,
  dismissExtensionModal,
  dismissExtensionSidebar,
  dismissExtensionFailure,
} from "./extensions";

export function ExtensionLink({
  link,
  close,
  asMenuItem = false,
}: {
  link: PluginExtensionLink;
  close?: () => void;
  asMenuItem?: boolean;
}) {
  const label = link.group?.name || link.category;
  const click = (event: React.MouseEvent) => {
    if (link.onClick) {
      event.preventDefault();
      link.onClick(event);
    }
    close?.();
  };
  return link.path ? (
    <a
      role={asMenuItem ? "menuitem" : undefined}
      href={link.path}
      target={link.openInNewTab ? "_blank" : undefined}
      rel={link.openInNewTab ? "noopener noreferrer" : undefined}
      onClick={click}
      title={link.description}
    >
      {label && <small>{label}</small>}
      {link.title}
    </a>
  ) : (
    <button
      role={asMenuItem ? "menuitem" : undefined}
      type="button"
      onClick={click}
      title={link.description}
    >
      {label && <small>{label}</small>}
      {link.title}
    </button>
  );
}
function Chrome() {
  const { links } = useExtensionLinks({
    extensionPointId: PluginExtensionPoints.SingleTopBarAction,
    limitPerPlugin: 3,
  });
  const { links: menuLinks } = useExtensionLinks({
    extensionPointId: PluginExtensionPoints.MegaMenuAction,
    limitPerPlugin: 3,
  });
  const { components } = useExtensionComponents({
    extensionPointId: PluginExtensionPoints.AppChrome,
  });
  const { components: nav } = useExtensionComponents({
    extensionPointId: PluginExtensionPoints.NavRightButton,
    limitPerPlugin: 1,
  });
  const { modal, sidebar, failure } = useExtensionOverlays();
  return (
    <>
      {(links.length > 0 || menuLinks.length > 0 || nav.length > 0) && (
        <nav
          className="extension-chrome-actions"
          aria-label={t("Plugin actions")}
        >
          {[...links, ...menuLinks].map((link) => (
            <ExtensionLink key={link.id} link={link} />
          ))}
          {nav.map((Component) => (
            <Component key={Component.meta.id} />
          ))}
        </nav>
      )}
      {components.map((Component) => (
        <div className="extension-app-chrome" key={Component.meta.id}>
          <Component />
        </div>
      ))}
      {modal && (
        <BrowserRouter>
          <Dialog
            key={modal.provider + modal.hash + modal.title}
            title={modal.title}
            onClose={dismissExtensionModal}
            style={{
              width: modal.width,
              maxWidth: "calc(100vw - 32px)",
              maxHeight: "calc(100vh - 32px)",
              height: modal.height,
            }}
          >
            <modal.body {...modal.props} onDismiss={dismissExtensionModal} />
          </Dialog>
        </BrowserRouter>
      )}
      {sidebar && (
        <BrowserRouter>
          <aside className="extension-sidebar" aria-label={sidebar.title}>
            <div className="dialog-heading">
              <h2>{sidebar.title}</h2>
              <button
                className="icon-button"
                aria-label={t("Close sidebar")}
                onClick={dismissExtensionSidebar}
              >
                <X size={20} />
              </button>
            </div>
            <sidebar.body {...sidebar.props} />
          </aside>
        </BrowserRouter>
      )}
      {failure && (
        <div role="alert" className="extension-failure">
          {t("Extension failed")}: {failure}
          <button
            className="icon-button"
            aria-label={t("Dismiss notification")}
            onClick={dismissExtensionFailure}
          >
            <X size={16} />
          </button>
        </div>
      )}
    </>
  );
}
export default function ExtensionHost() {
  const [ready, setReady] = useState(false);
  useEffect(() => {
    let active = true;
    void sdkRuntime()
      .then(() => {
        if (active) setReady(true);
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, []);
  return ready ? <Chrome /> : null;
}
