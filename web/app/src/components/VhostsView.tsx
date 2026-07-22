import { useCallback, useEffect, useMemo, useState } from "react";
import { api, ApiError, type Site, type SiteAction, type VhostKind } from "../api";
import { type ConfirmOptions, useConfirm, usePrompt } from "../confirm";
import { BackIcon, CopyIcon, PencilIcon, PlusIcon, TrashIcon, WrenchIcon } from "../icons";
import { useI18n } from "../i18n";
import { setQuery, useLocation } from "../router";
import { useToast } from "../toast";
import { useFileEditor } from "../useFileEditor";
import { useReloadToast } from "../useReloadToast";
import { Btn, editorPaneCls, EmptyState, SearchInput, Spinner, StatusDot, Switch } from "../ui";
import SaveButton from "./SaveButton";
import CodeEditor from "./CodeEditor";
import { useOutput } from "./OutputPanel";
import { useDarkTheme } from "./useDarkTheme";

function DomainBadges({ domains }: { domains: string[] | null }) {
  if (!domains || domains.length === 0) return null;
  return (
    <span className="flex flex-wrap gap-1">
      {domains.map((d) => (
        <span key={d} className="rounded bg-ctrl px-1.5 py-0.5 font-mono text-[11px] text-dim">
          {d}
        </span>
      ))}
    </span>
  );
}

const siteTemplate = `server {
    listen 80;
    server_name example.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
`;

const streamTemplate = `server {
    listen 10000;
    proxy_pass 127.0.0.1:10001;
}
`;

function stateLabel(s: Site, t: ReturnType<typeof useI18n>["t"]): string {
  return s.maintenance ? t.siteMaintenance : s.enabled ? t.siteEnabled : t.siteDisabled;
}

function stateColor(s: Site): string {
  return s.maintenance ? "text-warn" : s.enabled ? "text-ok" : "text-dim";
}

export default function VhostsView({
  kind,
  onAuthLost,
  defaultReload,
}: {
  kind: VhostKind;
  onAuthLost: () => void;
  defaultReload: boolean;
}) {
  const { t } = useI18n();
  const toast = useToast();
  const output = useOutput();
  const ask = useConfirm();
  const askName = usePrompt();
  const loc = useLocation();
  const editing = loc.searchParams.get("edit");
  const filter = loc.searchParams.get("q") ?? "";
  const setEditing = (name: string | null) => setQuery({ edit: name });
  const setFilter = (q: string) => setQuery({ q }, { replace: true });
  const [sites, setSites] = useState<Site[] | null>(null);
  const [maintenanceOK, setMaintenanceOK] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState("");

  const refresh = useCallback(() => {
    api
      .vhosts(kind)
      .then((r) => {
        setSites(r.sites ?? []);
        setMaintenanceOK(r.maintenance);
      })
      .catch((err) => {
        if (err instanceof ApiError && err.status === 401) onAuthLost();
        else toast(t.loadError, "error");
      });
  }, [kind, onAuthLost, toast, t]);

  useEffect(refresh, [refresh]);

  // runAction applies an action to one or more vhosts. Every action
  // confirms first (opts.confirm:false skips it). busyKey scopes the
  // spinner to the acting rows.
  const runAction = useCallback(
    async (names: string[], action: SiteAction, opts?: { confirm?: boolean }): Promise<boolean> => {
      const confirmIt = opts?.confirm ?? true;
      if (confirmIt) {
        const n = names.length;
        const c: Record<SiteAction, ConfirmOptions | null> = {
          enable: { title: t.enableSite, message: t.confirmEnableVhosts(n) },
          disable: { title: t.disableSite, message: t.confirmDisableVhosts(n), danger: true },
          maintenance_on: { title: t.maintenanceOn, message: t.confirmMaintenanceVhosts(n) },
          maintenance_off: { title: t.maintenanceOff, message: t.confirmMaintenanceOffVhosts(n) },
          delete: { title: t.deleteAction, message: t.confirmDeleteVhosts(n), danger: true },
        };
        if (c[action] && !(await ask(c[action]!))) return false;
      }
      setBusy(names.join("\n"));
      try {
        await api.vhostAction(kind, names, action);
        toast(t.siteActionApplied);
        return true;
      } catch (err) {
        if (err instanceof ApiError && err.status === 401) {
          onAuthLost();
          return false;
        }
        toast(err instanceof ApiError ? err.message : t.actionFailed, "error");
        if (err instanceof ApiError && err.output) output(t.output, err.output);
        return false;
      } finally {
        setBusy("");
        if (names.length > 1) setSelected(new Set());
        refresh();
      }
    },
    [kind, ask, t, toast, output, onAuthLost, refresh],
  );

  const shown = useMemo(() => {
    if (!sites) return [];
    const q = filter.trim().toLowerCase();
    if (!q) return sites;
    return sites.filter(
      (s) =>
        s.name.toLowerCase().includes(q) ||
        (s.domains ?? []).some((d) => d.toLowerCase().includes(q)),
    );
  }, [sites, filter]);

  async function create() {
    const name = await askName({
      title: kind === "sites" ? t.newSite : t.newStream,
      label: t.namePrompt,
      placeholder: kind === "sites" ? "example.com" : "my-stream",
      confirmLabel: t.create,
    });
    if (!name) return;
    try {
      await api.writeFile(`${kind}-available/${name.trim()}`, kind === "sites" ? siteTemplate : streamTemplate);
      refresh();
      setEditing(name.trim());
    } catch (err) {
      if (err instanceof ApiError && err.status === 422 && err.output) {
        toast(t.saveFailed, "error");
        output(t.output, err.output);
      } else {
        toast(err instanceof ApiError ? err.message : t.actionFailed, "error");
      }
    }
  }

  async function cloneSite(name: string) {
    const newName = await askName({
      title: t.clone,
      label: t.clonePrompt,
      initial: `${name}-copy`,
      confirmLabel: t.clone,
    });
    if (!newName) return;
    try {
      await api.vhostClone(kind, name, newName.trim());
      toast(t.cloned);
      refresh();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : t.actionFailed, "error");
      if (err instanceof ApiError && err.output) output(t.output, err.output);
    }
  }

  if (editing !== null) {
    const site = sites?.find((s) => s.name === editing) ?? null;
    return (
      <VhostEditor
        kind={kind}
        name={editing}
        site={site}
        maintenanceOK={maintenanceOK}
        onAuthLost={onAuthLost}
        onBack={() => {
          setEditing(null);
          refresh();
        }}
        onRenamed={(n) => {
          setEditing(n);
          refresh();
        }}
        runAction={runAction}
        refresh={refresh}
        defaultReload={defaultReload}
      />
    );
  }

  if (sites === null) {
    return (
      <EmptyState>
        <Spinner />
      </EmptyState>
    );
  }

  const allSelected = shown.length > 0 && shown.every((s) => selected.has(s.name));
  const toggleAll = () => {
    const next = new Set(selected);
    if (allSelected) shown.forEach((s) => next.delete(s.name));
    else shown.forEach((s) => next.add(s.name));
    setSelected(next);
  };
  const toggleOne = (name: string) => {
    const next = new Set(selected);
    if (next.has(name)) next.delete(name);
    else next.add(name);
    setSelected(next);
  };
  const sel = [...selected];
  const selSites = sites.filter((s) => selected.has(s.name));
  const anyEnabled = selSites.some((s) => s.enabled && !s.maintenance);
  const anyMaint = selSites.some((s) => s.maintenance);
  const anyBusy = busy !== "";

  // Shared interactive controls, reused by the desktop table rows and the
  // mobile cards so both layouts stay in sync.
  const rowCheckbox = (s: Site) => (
    <input
      type="checkbox"
      checked={selected.has(s.name)}
      onChange={() => toggleOne(s.name)}
      aria-label={s.name}
      className="h-3.5 w-3.5 shrink-0 accent-accent"
    />
  );
  const enabledSwitch = (s: Site) => (
    <Switch
      checked={s.enabled}
      warn={s.maintenance}
      label={s.enabled ? `${t.disableSite} ${s.name}` : `${t.enableSite} ${s.name}`}
      onToggle={() => void runAction([s.name], s.enabled ? "disable" : "enable")}
    />
  );
  const maintSwitch = (s: Site) => (
    <Switch
      checked={s.maintenance}
      warn
      label={`${t.maintenanceOn} ${s.name}`}
      onToggle={() =>
        void runAction([s.name], s.maintenance ? "maintenance_off" : "maintenance_on")
      }
    />
  );
  const cloneBtn = (s: Site) => (
    <Btn
      variant="ghost"
      className="min-h-[30px] px-2"
      aria-label={`${t.clone} ${s.name}`}
      title={t.clone}
      onClick={() => void cloneSite(s.name)}
    >
      <CopyIcon size={15} />
    </Btn>
  );
  const deleteBtn = (s: Site) => (
    <Btn
      variant="danger"
      className="min-h-[30px] px-2"
      aria-label={`${t.deleteAction} ${s.name}`}
      title={t.deleteAction}
      onClick={() =>
        void runAction([s.name], "delete").then((ok) => {
          if (ok)
            setSelected((prev) => {
              const next = new Set(prev);
              next.delete(s.name);
              return next;
            });
        })
      }
    >
      <TrashIcon size={15} />
    </Btn>
  );
  const dash = <span className="text-xs text-dim">—</span>;

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <div className="flex min-h-[56px] flex-wrap items-center gap-2.5 border-b border-line bg-panel px-4 py-2.5">
        {selected.size > 0 ? (
          <>
            <span className="text-[13px] font-medium text-dim">{t.selectedCount(selected.size)}</span>
            <span className="flex-1" />
            <Btn className="min-h-[32px] text-[13px]" disabled={anyBusy} onClick={() => void runAction(sel, "enable")}>
              {t.enableSite}
            </Btn>
            <Btn className="min-h-[32px] text-[13px]" disabled={anyBusy} onClick={() => void runAction(sel, "disable")}>
              {t.disableSite}
            </Btn>
            {maintenanceOK && anyEnabled && (
              <Btn className="min-h-[32px] text-[13px]" disabled={anyBusy} onClick={() => void runAction(sel, "maintenance_on")}>
                <WrenchIcon size={13} /> {t.maintenanceOn}
              </Btn>
            )}
            {maintenanceOK && anyMaint && (
              <Btn className="min-h-[32px] text-[13px]" disabled={anyBusy} onClick={() => void runAction(sel, "maintenance_off")}>
                {t.maintenanceOff}
              </Btn>
            )}
            <Btn variant="danger" className="min-h-[32px] text-[13px]" disabled={anyBusy} onClick={() => void runAction(sel, "delete")}>
              <TrashIcon size={13} /> {t.deleteAction}
            </Btn>
          </>
        ) : (
          <>
            <SearchInput
              value={filter}
              onChange={setFilter}
              placeholder={t.filterVhosts}
              className="min-w-[160px] flex-1"
            />
            <Btn variant="primary" onClick={create}>
              <PlusIcon /> {kind === "sites" ? t.newSite : t.newStream}
            </Btn>
          </>
        )}
      </div>

      <div className="min-h-0 flex-1 overflow-auto p-3 min-[761px]:p-4">
        {sites.length === 0 ? (
          <EmptyState>{kind === "sites" ? t.noSites : t.noStreams}</EmptyState>
        ) : shown.length === 0 ? (
          <EmptyState>{t.noMatches}</EmptyState>
        ) : (
          <div className="mx-auto max-w-4xl">
            {/* Desktop: table with a header legend */}
            <div
              className="hidden overflow-hidden rounded-lg bg-panel min-[761px]:grid"
              style={{
                gridTemplateColumns: maintenanceOK
                  ? "2.75rem minmax(7rem,max-content) minmax(0,1fr) 6.5rem 5rem 5.5rem"
                  : "2.75rem minmax(7rem,max-content) minmax(0,1fr) 5rem 5.5rem",
              }}
            >
              <div className="col-span-full grid grid-cols-subgrid items-center border-b border-line px-3 py-2 text-[11px] font-medium tracking-wide text-dim uppercase">
                <span className="flex items-center">
                  <input
                    type="checkbox"
                    checked={allSelected}
                    onChange={toggleAll}
                    aria-label={t.selectAll}
                    className="h-3.5 w-3.5 shrink-0 accent-accent"
                  />
                </span>
                <span>{t.colName}</span>
                <span>{kind === "sites" ? t.colDomains : t.colTargets}</span>
                {maintenanceOK && <span className="text-center">{t.colMaintenance}</span>}
                <span className="text-center">{t.colEnabled}</span>
                <span />
              </div>
              {shown.map((s) => {
                const rowBusy = busy.split("\n").includes(s.name);
                return (
                  <div
                    key={s.name}
                    role="button"
                    tabIndex={0}
                    onClick={() => setEditing(s.name)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" || e.key === " ") {
                        e.preventDefault();
                        setEditing(s.name);
                      }
                    }}
                    className="col-span-full grid cursor-pointer grid-cols-subgrid items-center border-b border-line px-3 py-2 last:border-b-0 hover:bg-ctrl/60"
                  >
                    <span className="flex items-center" onClick={(e) => e.stopPropagation()}>
                      {rowCheckbox(s)}
                    </span>
                    <span className="truncate pr-3 font-mono text-sm font-semibold">{s.name}</span>
                    <span className="min-w-0 pr-3">
                      {s.domains && s.domains.length > 0 ? <DomainBadges domains={s.domains} /> : dash}
                    </span>
                    {maintenanceOK && (
                      <span className="flex items-center justify-center" onClick={(e) => e.stopPropagation()}>
                        {rowBusy ? null : s.enabled ? maintSwitch(s) : dash}
                      </span>
                    )}
                    <span className="flex items-center justify-center" onClick={(e) => e.stopPropagation()}>
                      {rowBusy ? <Spinner /> : enabledSwitch(s)}
                    </span>
                    <span className="flex items-center justify-center gap-1" onClick={(e) => e.stopPropagation()}>
                      {cloneBtn(s)}
                      {deleteBtn(s)}
                    </span>
                  </div>
                );
              })}
            </div>

            {/* Mobile: cards */}
            <div className="min-[761px]:hidden">
              <label className="mb-2 flex w-fit items-center gap-2 px-1 py-1">
                <input
                  type="checkbox"
                  checked={allSelected}
                  onChange={toggleAll}
                  aria-label={t.selectAll}
                  className="h-3.5 w-3.5 shrink-0 accent-accent"
                />
                <span className="text-xs text-dim">{t.selectAll}</span>
              </label>
              <div className="flex flex-col gap-2">
              {shown.map((s) => {
                const rowBusy = busy.split("\n").includes(s.name);
                return (
                  <div
                    key={s.name}
                    role="button"
                    tabIndex={0}
                    onClick={() => setEditing(s.name)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" || e.key === " ") {
                        e.preventDefault();
                        setEditing(s.name);
                      }
                    }}
                    className="flex cursor-pointer flex-col gap-2.5 rounded-lg bg-panel p-3"
                  >
                    <div className="flex items-center gap-3">
                      <span className="flex items-center" onClick={(e) => e.stopPropagation()}>
                        {rowCheckbox(s)}
                      </span>
                      <span className="min-w-0 flex-1 truncate font-mono text-sm font-semibold">{s.name}</span>
                    </div>
                    {s.domains && s.domains.length > 0 && (
                      <div className="pl-7">
                        <DomainBadges domains={s.domains} />
                      </div>
                    )}
                    <div className="flex flex-wrap items-center gap-x-5 gap-y-2 border-t border-line pt-2.5 pl-7">
                      {maintenanceOK && s.enabled && (
                        <span
                          className="flex items-center gap-2 text-xs text-dim"
                          onClick={(e) => e.stopPropagation()}
                        >
                          <WrenchIcon size={14} className={s.maintenance ? "text-warn" : "text-dim"} />
                          {t.colMaintenance}
                          {rowBusy ? <Spinner /> : maintSwitch(s)}
                        </span>
                      )}
                      <span
                        className="flex items-center gap-2 text-xs text-dim"
                        onClick={(e) => e.stopPropagation()}
                      >
                        {t.colEnabled}
                        {rowBusy ? <Spinner /> : enabledSwitch(s)}
                      </span>
                      <span className="ml-auto flex items-center gap-1" onClick={(e) => e.stopPropagation()}>
                        {cloneBtn(s)}
                        {deleteBtn(s)}
                      </span>
                    </div>
                  </div>
                );
              })}
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

// Detail editor: CodeMirror with a left action pane (enable toggle,
// maintenance, rename, delete).
function VhostEditor({
  kind,
  name,
  site,
  maintenanceOK,
  onAuthLost,
  onBack,
  onRenamed,
  runAction,
  refresh,
  defaultReload,
}: {
  kind: VhostKind;
  name: string;
  site: Site | null;
  maintenanceOK: boolean;
  onAuthLost: () => void;
  onBack: () => void;
  onRenamed: (n: string) => void;
  runAction: (names: string[], action: SiteAction, opts?: { confirm?: boolean }) => Promise<boolean>;
  refresh: () => void;
  defaultReload: boolean;
}) {
  const { t } = useI18n();
  const toast = useToast();
  const output = useOutput();
  const dark = useDarkTheme();
  const askName = usePrompt();
  const notifyReload = useReloadToast();
  const file = useFileEditor(onAuthLost);

  useEffect(() => {
    void file.open(`${kind}-available/${name}`);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind, name]);

  async function back() {
    if (await file.confirmDiscard()) onBack();
  }

  async function rename() {
    const newName = await askName({ title: t.rename, label: t.renamePrompt, initial: name, confirmLabel: t.rename });
    if (!newName || newName.trim() === name) return;
    if (file.dirty && !(await file.confirmDiscard())) return;
    try {
      const res = await api.vhostRename(kind, name, newName.trim());
      toast(t.renamed);
      notifyReload(res);
      onRenamed(newName.trim());
    } catch (err) {
      toast(err instanceof ApiError ? err.message : t.actionFailed, "error");
      if (err instanceof ApiError && err.output) output(t.output, err.output);
    }
  }

  async function clone() {
    const newName = await askName({
      title: t.clone,
      label: t.clonePrompt,
      initial: `${name}-copy`,
      confirmLabel: t.clone,
    });
    if (!newName) return;
    if (file.dirty && !(await file.confirmDiscard())) return;
    try {
      await api.vhostClone(kind, name, newName.trim());
      toast(t.cloned);
      onRenamed(newName.trim());
    } catch (err) {
      toast(err instanceof ApiError ? err.message : t.actionFailed, "error");
      if (err instanceof ApiError && err.output) output(t.output, err.output);
    }
  }

  async function del() {
    if (await runAction([name], "delete")) onBack();
  }

  // Right-hand action pane, same layout as the config editor: enable /
  // maintenance toggles and status (site-specific), then rename + delete.
  const pane = (
    <div className={editorPaneCls}>
      {site && (
        <>
          <div className="flex items-center gap-2 max-[760px]:mr-1">
            {site.maintenance ? (
              <span className="inline-block h-2 w-2 rounded-full bg-warn" />
            ) : (
              <StatusDot on={site.enabled} />
            )}
            <span className={`text-[13px] font-medium ${stateColor(site)}`}>{stateLabel(site, t)}</span>
          </div>
          <label className="flex cursor-pointer items-center justify-between gap-3 rounded-lg bg-inset px-3 py-2 max-[760px]:w-auto">
            <span className="text-[13px] font-medium">{t.enableSite}</span>
            <Switch
              checked={site.enabled}
              warn={site.maintenance}
              label={site.enabled ? `${t.disableSite} ${name}` : `${t.enableSite} ${name}`}
              onToggle={() => void runAction([name], site.enabled ? "disable" : "enable").then((ok) => ok && refresh())}
            />
          </label>
          {maintenanceOK && site.enabled && (
            <label className="flex cursor-pointer items-center justify-between gap-3 rounded-lg bg-inset px-3 py-2">
              <span className="flex items-center gap-1.5 text-[13px] font-medium">
                <WrenchIcon size={14} className={site.maintenance ? "text-warn" : "text-dim"} /> {t.maintenanceOn}
              </span>
              <Switch
                checked={site.maintenance}
                warn
                label={`${t.maintenanceOn} ${name}`}
                onToggle={() => void runAction([name], site.maintenance ? "maintenance_off" : "maintenance_on")}
              />
            </label>
          )}
          {site.domains && site.domains.length > 0 && (
            <div className="flex flex-wrap gap-1 max-[760px]:hidden">
              <DomainBadges domains={site.domains} />
            </div>
          )}
          <div className="h-px bg-line max-[760px]:hidden" />
        </>
      )}
      <Btn className="min-h-[36px] justify-start text-[13px] max-[760px]:justify-center" onClick={rename}>
        <PencilIcon size={14} /> {t.rename}
      </Btn>
      <Btn className="min-h-[36px] justify-start text-[13px] max-[760px]:justify-center" onClick={clone}>
        <CopyIcon size={14} /> {t.clone}
      </Btn>
      <Btn
        variant="danger"
        className="min-h-[36px] justify-start text-[13px] max-[760px]:justify-center"
        onClick={del}
      >
        <TrashIcon size={14} /> {t.deleteAction}
      </Btn>
    </div>
  );

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <div className="flex min-h-[56px] flex-wrap items-center gap-3 border-b border-line bg-panel px-4 py-2.5">
        <Btn variant="ghost" className="px-2" onClick={back} aria-label={t.back}>
          <BackIcon />
        </Btn>
        <span className="flex min-w-0 flex-1 items-center gap-1.5">
          <span className="min-w-0 truncate font-mono text-[13px] text-dim">{name}</span>
          {file.dirty && (
            <span className="inline-block h-2 w-2 shrink-0 rounded-full bg-warn" title={t.unsavedChanges} />
          )}
        </span>
        <SaveButton
          save={file.save}
          saving={file.saving}
          disabled={!file.dirty || file.saving}
          defaultReload={defaultReload}
        />
      </div>
      <div className="flex min-h-0 flex-1 flex-col min-[761px]:flex-row-reverse">
        {pane}
        {file.loading ? (
          <EmptyState>
            <Spinner />
          </EmptyState>
        ) : (
          <CodeEditor
            key={file.path ?? name}
            value={file.content}
            dark={dark}
            onChange={file.setContent}
            onSave={file.save}
          />
        )}
      </div>
    </div>
  );
}
