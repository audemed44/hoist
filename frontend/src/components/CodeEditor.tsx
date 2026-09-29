import { indentWithTab } from "@codemirror/commands";
import { yaml } from "@codemirror/lang-yaml";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { unifiedMergeView } from "@codemirror/merge";
import { EditorState, type Extension } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { tags } from "@lezer/highlight";
import { basicSetup } from "codemirror";
import { useEffect, useRef } from "preact/hooks";

/** Colours come from the page's CSS variables, so it follows the theme. */
const theme = EditorView.theme({
  "&": {
    color: "var(--fg)",
    backgroundColor: "transparent",
    fontSize: "12.5px",
    border: "1px solid var(--line)",
  },
  "&.cm-focused": { outline: "none", borderColor: "var(--accent)" },
  ".cm-scroller": { fontFamily: "var(--font-mono)", lineHeight: "1.6" },
  ".cm-content": { caretColor: "var(--accent)", padding: "10px 0" },
  ".cm-gutters": {
    backgroundColor: "transparent",
    color: "var(--fg-4)",
    border: "none",
    borderRight: "1px solid var(--line-soft)",
  },
  ".cm-activeLine": { backgroundColor: "var(--well-bg)" },
  ".cm-activeLineGutter": { backgroundColor: "transparent", color: "var(--fg-2)" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection": {
    backgroundColor: "rgb(var(--accent-rgb) / 0.35) !important",
  },
  ".cm-cursor": { borderLeftColor: "var(--accent)" },
  ".cm-panels": { backgroundColor: "var(--raised)", color: "var(--fg)" },
  ".cm-panels.cm-panels-top": { borderBottom: "1px solid var(--line)" },
  ".cm-searchMatch": { backgroundColor: "rgb(var(--accent-rgb) / 0.25)" },
  ".cm-foldPlaceholder": {
    backgroundColor: "var(--well-bg)",
    border: "none",
    color: "var(--fg-2)",
  },
  ".cm-tooltip": { backgroundColor: "var(--raised)", border: "1px solid var(--line)" },
  ".cm-changedLine": { backgroundColor: "rgb(var(--good-rgb) / 0.1) !important" },
  ".cm-changedText": { background: "rgb(var(--good-rgb) / 0.3) !important" },
  ".cm-deletedChunk": { backgroundColor: "rgb(var(--bad-rgb) / 0.1) !important" },
  ".cm-deletedChunk del": { textDecoration: "none", background: "rgb(var(--bad-rgb) / 0.3)" },
  ".cm-insertedLine": { backgroundColor: "rgb(var(--good-rgb) / 0.1) !important" },
});

const highlight = HighlightStyle.define([
  { tag: [tags.propertyName, tags.definition(tags.propertyName)], color: "var(--accent-hi)" },
  { tag: [tags.string, tags.special(tags.string)], color: "var(--fg)" },
  { tag: [tags.number, tags.bool, tags.null], color: "var(--warn)" },
  { tag: [tags.comment, tags.lineComment], color: "var(--fg-3)", fontStyle: "italic" },
  { tag: [tags.punctuation, tags.separator, tags.squareBracket, tags.brace], color: "var(--fg-3)" },
  { tag: [tags.keyword, tags.typeName, tags.labelName, tags.meta], color: "var(--good)" },
]);

const base: Extension = [
  basicSetup,
  keymap.of([indentWithTab]),
  yaml(),
  theme,
  syntaxHighlighting(highlight),
];

/**
 * A YAML editor. `value` is the starting text; change `docKey` to replace
 * it. Pass `original` to show changes against it inline, and `readOnly` for
 * a viewer.
 */
export function CodeEditor(props: {
  value: string;
  docKey?: unknown;
  onChange?: (value: string) => void;
  readOnly?: boolean;
  original?: string;
  height?: string;
}) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const onChange = useRef(props.onChange);
  onChange.current = props.onChange;

  useEffect(() => {
    const extensions: Extension[] = [
      base,
      EditorView.updateListener.of((u) => {
        if (u.docChanged) onChange.current?.(u.state.doc.toString());
      }),
    ];
    if (props.readOnly)
      extensions.push(EditorState.readOnly.of(true), EditorView.editable.of(false));
    if (props.original !== undefined) {
      extensions.push(
        unifiedMergeView({ original: props.original, mergeControls: false, gutter: true }),
      );
    }
    if (props.height) {
      extensions.push(EditorView.theme({ "&": { height: props.height } }));
    }
    view.current = new EditorView({
      parent: host.current!,
      state: EditorState.create({ doc: props.value, extensions }),
    });
    return () => view.current?.destroy();
  }, [props.readOnly, props.original, props.height]);

  // The editor owns its text while it's being edited. Echoing every change
  // back through `value` would race fast typing (a render can arrive with an
  // older value), so it's only replaced when `docKey` changes.
  useEffect(() => {
    const v = view.current;
    if (v && v.state.doc.toString() !== props.value) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: props.value } });
    }
  }, [props.docKey]);

  return <div class="code-editor" ref={host} />;
}
