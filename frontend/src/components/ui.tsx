import { X } from "lucide-preact";
import type { ComponentChildren } from "preact";
import { useEffect, useRef } from "preact/hooks";
import type { Tone } from "../lib";

/** A modal built on <dialog>, so focus trapping and Escape come for free. */
export function Dialog(props: {
  title: string;
  onClose: () => void;
  children: ComponentChildren;
  footer?: ComponentChildren;
  wide?: boolean;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current!;
    dialog.showModal();
    return () => dialog.close();
  }, []);
  return (
    <dialog
      ref={ref}
      class={`dialog ${props.wide ? "dialog-wide" : ""}`}
      onCancel={(e) => {
        e.preventDefault();
        props.onClose();
      }}
      onClick={(e) => e.target === ref.current && props.onClose()}
    >
      <div class="dialog-inner">
        <div class="dialog-head">
          <h2>{props.title}</h2>
          <button class="icon-btn" onClick={props.onClose} aria-label="Close">
            <X size={16} />
          </button>
        </div>
        <div class="dialog-body">{props.children}</div>
        {props.footer && <div class="dialog-foot">{props.footer}</div>}
      </div>
    </dialog>
  );
}

export function Field(props: { label: string; hint?: string; children: ComponentChildren }) {
  return (
    <label class="field">
      <span class="field-label">{props.label}</span>
      {props.children}
      {props.hint && <span class="field-hint">{props.hint}</span>}
    </label>
  );
}

export function Dot(props: { tone: Tone }) {
  return <span class={`dot ${props.tone}`} />;
}

/** A numbered heading over a heavy rule, as on Foyer. */
export function SectionHead(props: {
  index?: number;
  title: string;
  children?: ComponentChildren;
}) {
  return (
    <div class="section-head">
      {props.index !== undefined && (
        <span class="section-index">{String(props.index).padStart(2, "0")}</span>
      )}
      <h2 class="section-name">{props.title}</h2>
      <span class="spacer" />
      {props.children}
    </div>
  );
}

export function ErrorNote(props: { children: ComponentChildren }) {
  return <div class="note note-bad">{props.children}</div>;
}
