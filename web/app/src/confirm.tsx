import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { useI18n } from "./i18n";
import { Btn, Input, Modal } from "./ui";

export interface ConfirmOptions {
  title: string;
  message: string;
  confirmLabel?: string;
  danger?: boolean;
}

export interface PromptOptions {
  title: string;
  label?: string;
  initial?: string;
  placeholder?: string;
  confirmLabel?: string;
  type?: string;
  // When set, a second field must match before the prompt resolves.
  confirm?: boolean;
  confirmLabelText?: string;
}

const ConfirmContext = createContext<(opts: ConfirmOptions) => Promise<boolean>>(() =>
  Promise.resolve(false),
);
const PromptContext = createContext<(opts: PromptOptions) => Promise<string | null>>(() =>
  Promise.resolve(null),
);

export function useConfirm() {
  return useContext(ConfirmContext);
}
export function usePrompt() {
  return useContext(PromptContext);
}

interface PendingConfirm {
  opts: ConfirmOptions;
  resolve: (v: boolean) => void;
}
interface PendingPrompt {
  opts: PromptOptions;
  resolve: (v: string | null) => void;
}

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const { t } = useI18n();
  const [confirmP, setConfirmP] = useState<PendingConfirm | null>(null);
  const [promptP, setPromptP] = useState<PendingPrompt | null>(null);
  const [value, setValue] = useState("");
  const [value2, setValue2] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  const ask = useCallback(
    (opts: ConfirmOptions) =>
      new Promise<boolean>((resolve) => setConfirmP({ opts, resolve })),
    [],
  );
  const prompt = useCallback(
    (opts: PromptOptions) =>
      new Promise<string | null>((resolve) => {
        setValue(opts.initial ?? "");
        setValue2("");
        setPromptP({ opts, resolve });
      }),
    [],
  );

  const closeConfirm = useCallback(
    (result: boolean) => {
      confirmP?.resolve(result);
      setConfirmP(null);
    },
    [confirmP],
  );
  const closePrompt = useCallback(
    (result: string | null) => {
      promptP?.resolve(result);
      setPromptP(null);
    },
    [promptP],
  );

  useEffect(() => {
    if (!confirmP) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") closeConfirm(false);
      // With a button focused (Cancel has autoFocus), Enter must activate
      // that button, not blanket-confirm a possibly destructive dialog.
      if (e.key === "Enter" && !(document.activeElement instanceof HTMLButtonElement)) {
        closeConfirm(true);
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [confirmP, closeConfirm]);

  useEffect(() => {
    if (promptP) inputRef.current?.focus();
  }, [promptP]);

  return (
    <ConfirmContext.Provider value={ask}>
      <PromptContext.Provider value={prompt}>
        {children}

        {confirmP && (
          <Modal title={confirmP.opts.title} onClose={() => closeConfirm(false)} className="max-w-sm">
            <p className="-mt-2 mb-5 text-sm leading-relaxed text-dim">{confirmP.opts.message}</p>
            <div className="flex justify-end gap-2.5">
              <Btn variant="ghost" onClick={() => closeConfirm(false)} autoFocus>
                {t.cancel}
              </Btn>
              <Btn variant={confirmP.opts.danger ? "danger" : "primary"} onClick={() => closeConfirm(true)}>
                {confirmP.opts.confirmLabel ?? t.confirm}
              </Btn>
            </div>
          </Modal>
        )}

        {promptP && (
          <Modal title={promptP.opts.title} onClose={() => closePrompt(null)} className="max-w-sm">
            {(() => {
              const o = promptP.opts;
              const mismatch = !!o.confirm && value2.length > 0 && value !== value2;
              const valid = value.trim().length > 0 && (!o.confirm || value === value2);
              return (
                <form
                  onSubmit={(e) => {
                    e.preventDefault();
                    if (valid) closePrompt(value.trim());
                  }}
                >
                  {o.label && <label className="mb-1.5 block text-[13px] text-dim">{o.label}</label>}
                  <Input
                    ref={inputRef}
                    type={o.type}
                    value={value}
                    placeholder={o.placeholder}
                    onChange={(e) => setValue(e.target.value)}
                  />
                  {o.confirm && (
                    <>
                      <label className="mt-3 mb-1.5 block text-[13px] text-dim">
                        {o.confirmLabelText ?? t.confirmPassword}
                      </label>
                      <Input
                        type={o.type}
                        value={value2}
                        placeholder={t.confirmPasswordHint}
                        onChange={(e) => setValue2(e.target.value)}
                      />
                    </>
                  )}
                  {mismatch && <p className="mt-2 text-[13px] text-danger">{t.passwordMismatch}</p>}
                  <div className="mt-5 flex justify-end gap-2.5">
                    <Btn type="button" variant="ghost" onClick={() => closePrompt(null)}>
                      {t.cancel}
                    </Btn>
                    <Btn type="submit" variant="primary" disabled={!valid}>
                      {o.confirmLabel ?? t.confirm}
                    </Btn>
                  </div>
                </form>
              );
            })()}
          </Modal>
        )}
      </PromptContext.Provider>
    </ConfirmContext.Provider>
  );
}
