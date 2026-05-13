import type { ReactNode } from "react";

interface ModalProps {
  title: string;
  sub?: string;
  actions: ReactNode;
  children: ReactNode;
}

export function Modal({ title, sub, actions, children }: ModalProps) {
  return (
    <div className="shp-modal-scrim">
      <div className="shp-modal">
        <div className="shp-modal__head">
          <h3 className="shp-modal__title">{title}</h3>
          {sub && <div className="shp-modal__sub">{sub}</div>}
        </div>
        <div className="shp-modal__body">{children}</div>
        <div className="shp-modal__foot">{actions}</div>
      </div>
    </div>
  );
}
