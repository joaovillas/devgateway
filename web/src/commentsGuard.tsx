import { useCallback, useEffect, useMemo, useRef, useState } from "react";

/**
 * Aviso único de perda de comentários (tarefa 8.8). Uma escrita de rota ou de
 * override pela API reescreve o documento YAML inteiro e perde comentários e
 * ordem das chaves. Antes da primeira escrita num documento com comentários
 * (`hasComments`), o painel pede confirmação; confirmada, ela é lembrada para
 * aquele arquivo e não volta a aparecer. Se o arquivo voltar a ter comentários
 * depois de reescrito (alguém os pôs de novo à mão), o aviso volta a valer.
 *
 * A confirmação só fica gravada depois que a escrita dá certo: se ela falhar
 * (409, 422, rede), o arquivo continua com os comentários e o aviso volta na
 * próxima vez. A chave inclui `scope` (o diretório de rotas do processo),
 * para a confirmação de um projeto não valer para outro servido na mesma origem.
 */

const KEY = "gateway.painel.comentariosConfirmados";

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
    // Sem armazenamento (janela privada, bloqueio): a confirmação vale só nesta sessão.
  }
}

/** A escrita esperava confirmação e o usuário desistiu. Não é erro da API. */
export class WriteCancelled extends Error {
  constructor() {
    super("escrita cancelada antes de gravar");
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
  /** Roda `run` depois da confirmação, quando ela é necessária. */
  guard<T>(doc: GuardedDoc, run: () => Promise<T>): Promise<T>;
  /** Arquivo aguardando confirmação agora, ou null. */
  asking: string | null;
  confirm(): void;
  cancel(): void;
  /** Informa o estado atual de um documento: sem comentários, a confirmação antiga caduca. */
  observe(doc: GuardedDoc): void;
}

export function useCommentsGuard(scope = ""): CommentsGuard {
  const acks = useRef<Set<string>>(readAcks());
  // Confirmadas nesta sessão, com a escrita ainda a caminho: não perguntam de
  // novo, mas só vão para o armazenamento quando a escrita der certo.
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
          // A escrita não aconteceu: os comentários continuam lá, e o aviso também.
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

  // Ao desmontar, nenhuma escrita fica pendurada esperando resposta.
  useEffect(
    () => () => {
      for (const w of waiting.current) w.reject(new WriteCancelled());
      waiting.current = [];
    },
    [],
  );

  return useMemo(() => ({ guard, asking, confirm, cancel, observe }), [guard, asking, confirm, cancel, observe]);
}
