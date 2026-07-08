import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, type LogFile } from "../api";
import { useI18n } from "../i18n";
import { setQuery, useLocation } from "../router";
import { useToast } from "../toast";
import { Btn, Combobox, StatusDot } from "../ui";

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
  const [size, setSize] = useState(0);
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

  // Live files open empty at the end of file so only new lines show;
  // history stays behind "Load older". Rotated .gz files have no tail to
  // follow, so they load their last page right away.
  const loadInitial = useCallback(
    async (path: string, gz: boolean) => {
      setLoaded(false);
      try {
        const chunk = await api.logRead(path, 0, gz ? PAGE_BYTES : 1);
        setLines(gz ? chunk.lines : []);
        setOffset(gz ? chunk.offset : chunk.size);
        setAtEnd(gz ? chunk.atEnd : chunk.size === 0);
        setSize(chunk.size);
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
  }, [selected, isGz, loadInitial]);

  // Live follow over SSE. Gated on `loaded` so the stream starts from the
  // file's end (the size set by loadInitial) — not from byte 0, which would
  // replay the whole file on open.
  useEffect(() => {
    esRef.current?.close();
    esRef.current = null;
    if (!selected || !live || isGz || !loaded) return;
    const es = new EventSource(api.logStreamURL(selected, size));
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
    // `size` intentionally omitted: it marks the stream start position at
    // toggle time, not a live dependency. `loaded` gates the open so size
    // is set first.
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

        <input
          type="search"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder={t.filter}
          className="min-h-[36px] min-w-[140px] flex-1 rounded-md bg-ctrl px-2.5 py-1.5 text-sm focus:outline-2 focus:outline-accent/60"
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
            <div key={i} className="px-4 py-px whitespace-pre-wrap break-all select-text">
              {line}
            </div>
          ))
        )}
      </div>
    </div>
  );
}
