import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, type LogFile } from "../api";
import { useI18n } from "../i18n";
import { setQuery, useLocation } from "../router";
import { useToast } from "../toast";
import { Btn, Combobox, SearchInput, StatusDot } from "../ui";

const PAGE_BYTES = 64 * 1024;
const MAX_LINES = 5000;

export default function LogsView({ onAuthLost }: { onAuthLost: () => void }) {
  const { t } = useI18n();
  const toast = useToast();
  const loc = useLocation();
  const selected = loc.searchParams.get("file") ?? "";
  const filter = loc.searchParams.get("q") ?? "";
  const setSelected = (p: string) => setQuery({ file: p, q: null });
  const setFilter = (q: string) => setQuery({ q }, { replace: true });
  const [files, setFiles] = useState<LogFile[]>([]);
  const [lines, setLines] = useState<string[]>([]);
  const [offset, setOffset] = useState(0);
  const [atEnd, setAtEnd] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [live, setLive] = useState(true);
  const viewRef = useRef<HTMLDivElement>(null);
  const stickBottom = useRef(true);
  const esRef = useRef<EventSource | null>(null);

  useEffect(() => {
    api
      .logs()
      .then((r) => {
        setFiles(r.files);
        if (r.files.length > 0 && !selected) {
          const preferred =
            r.files.find((f) => f.path.endsWith("/access.log")) ?? r.files[0];
          setQuery({ file: preferred.path }, { replace: true });
        }
      })
      .catch((err) => {
        if (err instanceof ApiError && err.status === 401) onAuthLost();
        else toast(t.loadError, "error");
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const isGz = files.find((f) => f.path === selected)?.gzip ?? false;

  // Live files open empty at EOF (history stays behind "Load older");
  // .gz files have no tail to follow, so they load a page right away.
  const loadInitial = useCallback(
    async (path: string, gz: boolean) => {
      setLoaded(false);
      try {
        const chunk = await api.logRead(path, 0, gz ? PAGE_BYTES : 1);
        setLines(gz ? chunk.lines : []);
        setOffset(gz ? chunk.offset : chunk.size);
        setAtEnd(gz ? chunk.atEnd : chunk.size === 0);
        setLoaded(true);
        stickBottom.current = true;
        requestAnimationFrame(() => {
          viewRef.current?.scrollTo({ top: viewRef.current.scrollHeight });
        });
      } catch (err) {
        if (err instanceof ApiError && err.status === 401) onAuthLost();
        else toast(err instanceof ApiError ? err.message : t.loadError, "error");
      }
    },
    [onAuthLost, toast, t],
  );

  useEffect(() => {
    if (selected) loadInitial(selected, isGz);
    // loadInitial's identity must not reset the view on unrelated re-renders.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selected, isGz]);

  // Live follow over SSE. With no `from`, the server starts the stream at the
  // current end of file, so enabling follow — including after pausing and
  // clearing — shows only new lines instead of replaying what was written
  // while it was off. Gated on `loaded` so loadInitial cannot clear the view
  // after the first streamed lines arrive.
  useEffect(() => {
    esRef.current?.close();
    esRef.current = null;
    if (!selected || !live || isGz || !loaded) return;
    const es = new EventSource(api.logStreamURL(selected));
    es.onmessage = (ev) => {
      const line = JSON.parse(ev.data) as string;
      setLines((prev) => {
        const next = [...prev, line];
        return next.length > MAX_LINES ? next.slice(next.length - MAX_LINES) : next;
      });
    };
    es.onerror = () => {
      // EventSource reconnects on its own; on auth loss it keeps failing,
      // so probe once.
      api.me().catch((err) => {
        if (err instanceof ApiError && err.status === 401) {
          es.close();
          onAuthLost();
        }
      });
    };
    esRef.current = es;
    return () => es.close();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selected, live, isGz, loaded, onAuthLost]);

  // Auto-scroll while the user stays at the bottom.
  useEffect(() => {
    if (stickBottom.current) {
      viewRef.current?.scrollTo({ top: viewRef.current.scrollHeight });
    }
  }, [lines]);

  // Changing (or clearing) the filter jumps to the newest matching line,
  // so clearing it returns to the live tail instead of staying scrolled up.
  useEffect(() => {
    stickBottom.current = true;
    requestAnimationFrame(() => {
      viewRef.current?.scrollTo({ top: viewRef.current.scrollHeight });
    });
  }, [filter]);

  function onScroll() {
    const el = viewRef.current;
    if (!el) return;
    stickBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  }

  async function loadOlder() {
    if (!selected || atEnd) return;
    try {
      const chunk = await api.logRead(selected, offset, PAGE_BYTES);
      const el = viewRef.current;
      const prevHeight = el?.scrollHeight ?? 0;
      setLines((prev) => [...chunk.lines, ...prev]);
      setOffset(chunk.offset);
      setAtEnd(chunk.atEnd);
      requestAnimationFrame(() => {
        if (el) el.scrollTop = el.scrollHeight - prevHeight;
      });
    } catch (err) {
      toast(err instanceof ApiError ? err.message : t.loadError, "error");
    }
  }

  const shown = filter
    ? lines.filter((l) => l.toLowerCase().includes(filter.toLowerCase()))
    : lines;

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <div className="flex min-h-[56px] flex-wrap items-center gap-2.5 border-b border-line bg-panel px-4 py-2.5">
        <Combobox
          value={selected}
          onChange={setSelected}
          options={files.map((f) => ({ value: f.path, label: f.path }))}
          className="w-[240px] max-w-full"
          placeholder={t.logFile}
          ariaLabel={t.logFile}
        />

        <label className={`inline-flex items-center gap-1.5 text-xs ${isGz ? "opacity-50" : ""}`}>
          <input
            type="checkbox"
            checked={live && !isGz}
            disabled={isGz}
            onChange={(e) => setLive(e.target.checked)}
            className="h-3.5 w-3.5 accent-accent"
          />
          <StatusDot on={live && !isGz} pulse />
          {t.follow}
        </label>

        <SearchInput
          value={filter}
          onChange={setFilter}
          placeholder={t.filter}
          className="min-w-[140px] flex-1"
        />

        <span className="text-xs whitespace-nowrap text-dim">{t.lines(shown.length)}</span>
        <Btn className="min-h-[32px] px-2.5 text-[13px]" onClick={() => setLines([])}>
          {t.clear}
        </Btn>
      </div>

      <div
        ref={viewRef}
        onScroll={onScroll}
        className="min-h-0 flex-1 overflow-auto bg-inset py-2 font-mono text-[12.5px] leading-normal"
        style={{ WebkitOverflowScrolling: "touch" }}
      >
        {!atEnd && loaded && (
          <div className="flex justify-center py-2">
            <Btn className="min-h-[28px] px-4 text-xs" onClick={loadOlder}>
              {t.loadOlder}
            </Btn>
          </div>
        )}
        {shown.length === 0 ? (
          <div className="px-3 py-2 text-dim">{t.emptyLog}</div>
        ) : (
          shown.map((line, i) => (
            <div
              key={i}
              className="border-b border-line/70 px-4 py-0.5 whitespace-pre-wrap break-all select-text last:border-b-0"
            >
              {line}
            </div>
          ))
        )}
      </div>
    </div>
  );
}
