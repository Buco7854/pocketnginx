import { useCallback, useState } from "react";
import { api, ApiError } from "./api";
import { useConfirm } from "./confirm";
import { useI18n } from "./i18n";
import { useToast } from "./toast";
import { useOutput } from "./components/OutputPanel";

// Shared open/save state for a config file, with the dirty-discard
// confirmation and nginx -t failure output wiring. Used by the config
// browser's editor and the site/stream editors.
export function useFileEditor(onAuthLost: () => void) {
  const { t } = useI18n();
  const toast = useToast();
  const output = useOutput();
  const ask = useConfirm();

  const [path, setPath] = useState<string | null>(null);
  const [content, setContent] = useState("");
  const [savedContent, setSavedContent] = useState("");
  const [baseHash, setBaseHash] = useState("");
  const [readOnly, setReadOnly] = useState(false);
  const [symlink, setSymlink] = useState("");
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);

  const dirty = content !== savedContent;

  const handleErr = useCallback(
    (err: unknown, fallback: string) => {
      if (err instanceof ApiError && err.status === 401) {
        toast(t.sessionExpired, "warn");
        onAuthLost();
        return;
      }
      toast(err instanceof ApiError ? err.message : fallback, "error");
    },
    [toast, t, onAuthLost],
  );

  // confirmDiscard resolves true when it is safe to drop current changes.
  const confirmDiscard = useCallback(async () => {
    if (!dirty) return true;
    return ask({ title: t.unsavedChanges, message: t.discardChanges, danger: true });
  }, [dirty, ask, t]);

  const open = useCallback(
    async (p: string, external = false, link = "") => {
      if (!(await confirmDiscard())) return false;
      setLoading(true);
      try {
        const res = await api.readFile(p);
        setPath(p);
        setContent(res.content);
        setSavedContent(res.content);
        setBaseHash(res.hash);
        setReadOnly(external);
        setSymlink(link);
        return true;
      } catch (err) {
        handleErr(err, t.loadError);
        return false;
      } finally {
        setLoading(false);
      }
    },
    [confirmDiscard, handleErr, t],
  );

  // reset clears editor state unconditionally (no discard prompt). Use it
  // when the buffer is gone for good — e.g. the open file was just deleted.
  const reset = useCallback(() => {
    setPath(null);
    setContent("");
    setSavedContent("");
    setBaseHash("");
    setSymlink("");
  }, []);

  const close = useCallback(async () => {
    if (!(await confirmDiscard())) return false;
    reset();
    return true;
  }, [confirmDiscard, reset]);

  const save = useCallback(async () => {
    if (!path || saving || readOnly || !dirty) return;
    setSaving(true);
    try {
      let res;
      try {
        res = await api.writeFile(path, content, baseHash || undefined);
      } catch (err) {
        // Conflict: the file changed on disk. The buffer is kept either
        // way; overwriting retries without the base hash.
        if (!(err instanceof ApiError && err.status === 409)) throw err;
        const gone = err.gone === true;
        const overwrite = await ask({
          title: gone ? t.goneTitle : t.conflictTitle,
          message: gone ? t.goneMessage : t.conflictMessage,
          confirmLabel: gone ? t.createFile : t.overwrite,
          danger: true,
        });
        if (!overwrite) return;
        res = await api.writeFile(path, content);
      }
      setSavedContent(content);
      setBaseHash(res.hash ?? "");
      toast(t.saved);
      if (res.output?.includes("warn")) output(t.output, res.output);
    } catch (err) {
      if (err instanceof ApiError && err.status === 422) {
        toast(t.saveFailed, "error");
        if (err.output) output(t.output, err.output);
      } else {
        handleErr(err, t.actionFailed);
      }
    } finally {
      setSaving(false);
    }
  }, [path, saving, readOnly, dirty, content, baseHash, ask, toast, t, output, handleErr]);

  return {
    path,
    content,
    setContent,
    dirty,
    loading,
    saving,
    readOnly,
    symlink,
    open,
    close,
    reset,
    save,
    confirmDiscard,
    handleErr,
  };
}
