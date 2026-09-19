import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import type { ApiError } from "./api";
import { WriteCancelled } from "./commentsGuard";
import { toApiError } from "./hooks";
import { applyMergePatch, canCompose, composePatches, deepEqual, isEmptyPatch, type MergePatch } from "./patch";

export interface PatchWriter<T> {
  /** The value the screen shows: the server's, with the not-yet-confirmed changes on top. */
  value: T;
  /**
   * Records a change. With no `delay` it goes out at once; with `delay` it
   * waits for the control to stop moving for that long (dragging a continuous
   * control).
   */
  change(patch: MergePatch, delay?: number): void;
  /** Sends right away whatever was waiting on the debounce (the end of a drag gesture). */
  flush(): void;
  /** There is a change on its way to the server or waiting its turn. */
  busy: boolean;
  error: ApiError | null;
  dismissError(): void;
}

/**
 * Writes by PATCH (merge patch), with no save button and no flicker.
 *
 * - Changes in a row are joined into a single patch, and only one PATCH is in
 *   flight at a time; what arrives during the flight goes out right after it,
 *   already composed. When composing would change the meaning (removing a key
 *   and then putting an object in it turns into a merge over the old value),
 *   the two patches go out in sequence instead of together.
 * - While the write has not come back, the screen shows the value that was
 *   asked for. When it comes back, the server's response takes over (it is the
 *   reconciled state, which may differ from what was asked), until the re-read
 *   brings the same value through `base`.
 * - If the write fails, the value goes back to the server's and the error is
 *   left showing. What had been composed on top of the write that failed is
 *   discarded too: it was thought out over a value the server did not accept.
 * - Unmounting the control (switching route, closing the tab) does not lose
 *   the last change: what was waiting on the debounce goes out at once, and
 *   the queue runs to the end, only without updating a screen that is gone.
 */
export function usePatchWriter<T>(base: T, send: (patch: MergePatch) => Promise<T>): PatchWriter<T> {
  const [, rerender] = useReducer((n: number) => n + 1, 0);
  const [error, setError] = useState<ApiError | null>(null);
  const sendRef = useRef(send);
  sendRef.current = send;

  const queue = useRef<MergePatch[]>([]);
  const inflight = useRef<MergePatch | null>(null);
  const confirmed = useRef<T | null>(null);
  const timer = useRef<number | undefined>(undefined);
  const alive = useRef(true);

  const flush = useCallback(() => {
    window.clearTimeout(timer.current);
    timer.current = undefined;
    if (inflight.current || queue.current.length === 0) return;
    const patch = queue.current.shift()!;
    inflight.current = patch;
    if (alive.current) rerender();
    sendRef.current(patch).then(
      (res) => {
        inflight.current = null;
        if (alive.current) {
          confirmed.current = res;
          setError(null);
          rerender();
        }
        flush();
      },
      (e: unknown) => {
        inflight.current = null;
        // Whoever asked gave up on saving, or the write failed: what came
        // afterwards was composed on top of it and does not go out either.
        queue.current = [];
        if (alive.current) {
          if (!(e instanceof WriteCancelled)) setError(toApiError(e));
          rerender();
        }
      },
    );
  }, []);

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      // The last change must not die on the debounce.
      if (timer.current !== undefined) flush();
    };
  }, [flush]);

  const change = useCallback(
    (patch: MergePatch, delay?: number) => {
      if (isEmptyPatch(patch)) return;
      const q = queue.current;
      const last = q[q.length - 1];
      if (last && canCompose(last, patch)) q[q.length - 1] = composePatches(last, patch);
      else q.push(patch);
      rerender();
      window.clearTimeout(timer.current);
      timer.current = undefined;
      if (delay) timer.current = window.setTimeout(flush, delay);
      else flush();
    },
    [flush],
  );

  // Reconciliation: the re-read that brings the confirmed value ends the
  // period in which the PATCH response counts for more than `base`. If the
  // re-read brings another value (another write, an edit to the file), it wins
  // shortly afterwards.
  useEffect(() => {
    if (!confirmed.current) return;
    if (deepEqual(base, confirmed.current)) {
      confirmed.current = null;
      rerender();
      return;
    }
    const id = window.setTimeout(() => {
      if (inflight.current || queue.current.length) return;
      confirmed.current = null;
      rerender();
    }, 1200);
    return () => window.clearTimeout(id);
  }, [base]);

  let value = confirmed.current ?? base;
  if (inflight.current) value = applyMergePatch(value, inflight.current);
  for (const p of queue.current) value = applyMergePatch(value, p);

  return {
    value,
    change,
    flush,
    busy: inflight.current !== null || queue.current.length > 0,
    error,
    dismissError: useCallback(() => setError(null), []),
  };
}
