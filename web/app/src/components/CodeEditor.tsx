import { useEffect, useRef } from "react";
import { Annotation, EditorState, Compartment } from "@codemirror/state";
import {
  EditorView,
  keymap,
  lineNumbers,
  highlightActiveLine,
  highlightActiveLineGutter,
  drawSelection,
  highlightSpecialChars,
} from "@codemirror/view";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import {
  StreamLanguage,
  syntaxHighlighting,
  HighlightStyle,
  bracketMatching,
  indentOnInput,
} from "@codemirror/language";
import { searchKeymap, highlightSelectionMatches } from "@codemirror/search";
import { tags } from "@lezer/highlight";

// Minimal nginx tokenizer. The legacy mode only knows a fixed directive
// list and renders everything else (and anything after it) as plain text;
// this instead colours the first word of every statement as a directive, so
// highlighting stays consistent for any config, including regex locations.
const nginxMode = {
  name: "nginx",
  startState: () => ({ directive: true }),
  token(stream: any, state: { directive: boolean }): string | null {
    if (stream.eatSpace()) return null;
    const ch = stream.peek();
    if (ch === "#") {
      stream.skipToEnd();
      return "comment";
    }
    if (ch === '"' || ch === "'") {
      stream.next();
      let esc = false;
      let c: string | null;
      while ((c = stream.next()) != null) {
        if (c === ch && !esc) break;
        esc = !esc && c === "\\";
      }
      return "string";
    }
    if (ch === "$") {
      stream.next();
      stream.eatWhile(/[A-Za-z0-9_]/);
      return "variableName";
    }
    if (ch === ";" || ch === "{" || ch === "}") {
      stream.next();
      state.directive = true;
      return null;
    }
    const from = stream.pos;
    stream.eatWhile(/[^\s;{}#"']/);
    if (state.directive) {
      state.directive = false;
      return "keyword";
    }
    return /^\d/.test(stream.string.slice(from, stream.pos)) ? "number" : null;
  },
  languageData: { commentTokens: { line: "#" } },
};

// Marks programmatic doc replacements (external value sync) so the update
// listener doesn't report them as user edits and spuriously mark dirty.
const External = Annotation.define<boolean>();

// Mobile browsers run their "Select all" menu action against the DOM, which
// only holds the rendered viewport, so on a file taller than the screen it
// stops at the visible edge. Detect that clamped selection and extend it to
// the whole document.
const promoteSelectAll = EditorView.updateListener.of((u) => {
  if (!u.selectionSet || u.docChanged || u.state.selection.ranges.length !== 1) return;
  const sel = u.state.selection.main;
  const { from, to } = u.view.viewport;
  const full = u.state.doc.length;
  if (!sel.empty && (from > 0 || to < full) && sel.from === from && sel.to === to) {
    queueMicrotask(() => u.view.dispatch({ selection: { anchor: 0, head: full }, userEvent: "select" }));
  }
});

// Two token palettes referencing the app theme through CSS variables is
// not possible for all tags, so use explicit colors per theme.
const lightHighlight = HighlightStyle.define([
  { tag: tags.keyword, color: "#0550ae", fontWeight: "600" },
  { tag: tags.number, color: "#953800" },
  { tag: tags.string, color: "#0a3069" },
  { tag: tags.comment, color: "#6e7781", fontStyle: "italic" },
  { tag: tags.variableName, color: "#8250df" },
]);

const darkHighlight = HighlightStyle.define([
  { tag: tags.keyword, color: "#79b8ff", fontWeight: "600" },
  { tag: tags.number, color: "#ffab70" },
  { tag: tags.string, color: "#a5d6ff" },
  { tag: tags.comment, color: "#8b949e", fontStyle: "italic" },
  { tag: tags.variableName, color: "#d2a8ff" },
]);

export default function CodeEditor({
  value,
  dark,
  readOnly = false,
  onChange,
  onSave,
}: {
  value: string;
  dark: boolean;
  readOnly?: boolean;
  onChange: (v: string) => void;
  onSave: () => void;
}) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const themeCompartment = useRef(new Compartment());
  const readOnlyCompartment = useRef(new Compartment());
  const onChangeRef = useRef(onChange);
  const onSaveRef = useRef(onSave);
  onChangeRef.current = onChange;
  onSaveRef.current = onSave;

  useEffect(() => {
    if (!host.current) return;
    const state = EditorState.create({
      doc: value,
      extensions: [
        lineNumbers(),
        highlightActiveLine(),
        highlightActiveLineGutter(),
        highlightSpecialChars(),
        drawSelection(),
        history(),
        bracketMatching(),
        indentOnInput(),
        highlightSelectionMatches(),
        StreamLanguage.define(nginxMode),
        themeCompartment.current.of(syntaxHighlighting(dark ? darkHighlight : lightHighlight)),
        readOnlyCompartment.current.of(EditorState.readOnly.of(readOnly)),
        keymap.of([
          {
            key: "Mod-s",
            run: () => {
              onSaveRef.current();
              return true;
            },
          },
          indentWithTab,
          ...defaultKeymap,
          ...historyKeymap,
          ...searchKeymap,
        ]),
        EditorView.updateListener.of((u) => {
          if (u.docChanged && !u.transactions.some((tr) => tr.annotation(External)))
            onChangeRef.current(u.state.doc.toString());
        }),
        promoteSelectAll,
        EditorView.lineWrapping,
      ],
    });
    view.current = new EditorView({ state, parent: host.current });
    return () => {
      view.current?.destroy();
      view.current = null;
    };
    // Recreated only when the document identity changes via key prop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    view.current?.dispatch({
      effects: themeCompartment.current.reconfigure(
        syntaxHighlighting(dark ? darkHighlight : lightHighlight),
      ),
    });
  }, [dark]);

  useEffect(() => {
    view.current?.dispatch({
      effects: readOnlyCompartment.current.reconfigure(EditorState.readOnly.of(readOnly)),
    });
  }, [readOnly]);

  // External value replacement (file switch handled by key prop; this
  // covers programmatic resets like discarding changes).
  useEffect(() => {
    const v = view.current;
    if (!v) return;
    if (v.state.doc.toString() !== value) {
      v.dispatch({
        changes: { from: 0, to: v.state.doc.length, insert: value },
        annotations: External.of(true),
      });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value]);

  return <div ref={host} className="editor-host min-h-0 flex-1 overflow-hidden" />;
}
