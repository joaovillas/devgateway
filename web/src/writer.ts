import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import type { ApiError } from "./api";
import { WriteCancelled } from "./commentsGuard";
import { toApiError } from "./hooks";
import { applyMergePatch, canCompose, composePatches, deepEqual, isEmptyPatch, type MergePatch } from "./patch";

export interface PatchWriter<T> {
  /** O valor que a tela mostra: o do servidor com as alterações ainda não confirmadas por cima. */
  value: T;
  /**
   * Registra uma alteração. Sem `delay`, ela sai já; com `delay`, espera o
   * controle parar de mexer por esse tempo (arrasto de um controle contínuo).
   */
  change(patch: MergePatch, delay?: number): void;
  /** Manda já o que estava esperando o debounce (fim do gesto de arrasto). */
  flush(): void;
  /** Há alteração a caminho do servidor ou esperando a vez. */
  busy: boolean;
  error: ApiError | null;
  dismissError(): void;
}

/**
 * Escreve por PATCH (merge patch) sem botão de salvar e sem piscar.
 *
 * - Alterações seguidas se juntam num patch só, e só um PATCH fica em voo por
 *   vez; o que chega durante o voo sai logo depois, já composto. Quando a
 *   composição mudaria o sentido (remover uma chave e depois pôr um objeto
 *   nela vira um merge sobre o valor antigo), os dois patches saem em
 *   sequência em vez de juntos.
 * - Enquanto a escrita não volta, a tela mostra o valor pedido. Quando volta,
 *   a resposta do servidor passa a valer (é o estado reconciliado, que pode
 *   diferir do pedido), até a releitura trazer o mesmo valor por `base`.
 * - Se a escrita falha, o valor volta ao do servidor e o erro fica exposto. O
 *   que tinha sido composto sobre a escrita que falhou também é descartado:
 *   foi pensado sobre um valor que o servidor não aceitou.
 * - Desmontar o controle (trocar de rota, fechar a aba) não perde a última
 *   alteração: o que esperava o debounce sai na hora, e a fila continua até
 *   o fim, só sem atualizar a tela que já não existe.
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
        // Quem pediu desistiu de gravar, ou a escrita falhou: o que veio
        // depois foi composto sobre ela e também não sai.
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
      // A última alteração não pode morrer no debounce.
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

  // Reconciliação: a releitura que traz o valor confirmado encerra o período
  // em que a resposta do PATCH vale mais que `base`. Se a releitura trouxer
  // outro valor (outra escrita, edição do arquivo), ela vence pouco depois.
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
