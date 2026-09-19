import { useCallback, useEffect, useMemo, useRef, useState } from "react";

/**
 * The one-off warning about losing comments (task 8.8). A write to a route or
 * an override through the API rewrites the whole YAML document and loses the
 * comments and the key order. Before the first write to a document that has
 * comments (`hasComments`), the panel asks for confirmation; once confirmed,
 * it is remembered for that file and does not show up again. If the file comes
 * to have comments again after being rewritten (someone put them back by
 * hand), the warning applies once more.
 *
 * The confirmation is only stored after the write succeeds: if it fails (409,
 * 422, network), the file still has its comments and the warning comes back
 * next time. The key includes `scope` (the process's routes directory), so
 * that a confirmation for one project does not count for another served from
 * the same origin.
 */

const KEY = "gateway.panel.commentsAcknowledged";

function readAcks(): Set<string> {
  try {
    const raw = window.localStorage.getItem(KEY);
    const list: unknown = raw ? JSON.parse(raw) : [];
    return new Set(Array.isArray(list) ? list.filter((x): x is string => typeof x === "string") : []);
  } catch {
    return new Set();
  }
}

function writeAcks(acks: Set<string>): void {
  try {
    window.localStorage.setItem(KEY, JSON.stringify([...acks]));
  } catch {
    // With no storage (private window, blocked): the confirmation only counts for this session.
  }
}

/** The write was waiting for confirmation and the user gave up. Not an API error. */
export class WriteCancelled extends Error {
  constructor() {
    super("write cancelled before it was saved");
    this.name = "WriteCancelled";
  }
}

export interface GuardedDoc {
  file: string;
  hasComments: boolean;
}

interface Waiting {
  file: string;
  resolve: () => void;
  reject: (e: Error) => void;
}

export interface CommentsGuard {
  /** Runs `run` after the confirmation, when one is needed. */
  guard<T>(doc: GuardedDoc, run: () => Promise<T>): Promise<T>;
  /** The file waiting for confirmation right now, or null. */
  asking: string | null;
  confirm(): void;
  cancel(): void;
  /** Reports a document's current state: with no comments, the old confirmation expires. */
  observe(doc: GuardedDoc): void;
}

export function useCommentsGuard(scope = ""): CommentsGuard {
  const acks = useRef<Set<string>>(readAcks());
  // Confirmed in this session, with the write still on its way: they do not
  // ask again, but they only go to storage once the write succeeds.
  const pending = useRef<Set<string>>(new Set());
  const scopeRef = useRef(scope);
  scopeRef.current = scope;
  const keyOf = (file: string) => (scopeRef.current ? `${scopeRef.current} :: ${file}` : file);
  const waiting = useRef<Waiting[]>([]);
  const askingRef = useRef<string | null>(null);
  const [asking, setAsking] = useState<string | null>(null);

  const guard = useCallback(<T,>(doc: GuardedDoc, run: () => Promise<T>): Promise<T> => {
    const key = keyOf(doc.file);
    if (!doc.hasComments || acks.current.has(key)) return run();
    const confirmedRun = () =>
      run().then(
        (r) => {
          if (pending.current.delete(key)) {
            acks.current.add(key);
            writeAcks(acks.current);
          }
          return r;
        },
        (e: unknown) => {
          // The write did not happen: the comments are still there, and so is the warning.
          pending.current.delete(key);
          throw e;
        },
      );
    if (pending.current.has(key)) return confirmedRun();
    return new Promise<void>((resolve, reject) => {
      waiting.current.push({ file: doc.file, resolve, reject });
      if (!askingRef.current) {
        askingRef.current = doc.file;
        setAsking(doc.file);
      }
    }).then(confirmedRun);
  }, []);

  const settle = useCallback((ok: boolean) => {
    const file = askingRef.current;
    if (!file) return;
    if (ok) pending.current.add(keyOf(file));
    const rest: Waiting[] = [];
    for (const w of waiting.current) {
      if (w.file !== file) rest.push(w);
      else if (ok) w.resolve();
      else w.reject(new WriteCancelled());
    }
    waiting.current = rest;
    askingRef.current = rest[0]?.file ?? null;
    setAsking(askingRef.current);
  }, []);

  const confirm = useCallback(() => settle(true), [settle]);
  const cancel = useCallback(() => settle(false), [settle]);

  const observe = useCallback((doc: GuardedDoc) => {
    if (!doc.hasComments && acks.current.delete(keyOf(doc.file))) writeAcks(acks.current);
  }, []);

  // On unmount, no write is left hanging and waiting for an answer.
  useEffect(
    () => () => {
      for (const w of waiting.current) w.reject(new WriteCancelled());
      waiting.current = [];
    },
    [],
  );

  return useMemo(() => ({ guard, asking, confirm, cancel, observe }), [guard, asking, confirm, cancel, observe]);
}
