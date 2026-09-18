import { useCallback, useEffect, useId, useRef, useState } from "react";
import { api, isApiError, type Exchange, type ExchangeFilter, type Message, type RouteResource } from "../api";
import { decodeMessage, type DecodedBody } from "../body";
import type { CommentsGuard } from "../commentsGuard";
import { describeFilter } from "../exchangeFilter";
import { bytes, clock, ms, num } from "../format";
import { toApiError, useResource, type Load } from "../hooks";
import { DeriveDraft } from "./DeriveDraft";
import { ErrorNote } from "./ErrorNote";
import { ArrowIcon, CloseIcon, PlusIcon } from "./Icons";
import { Empty, Loading } from "./States";
import type { ApiError } from "../api";

export interface OpenedExchange {
  id: string;
  /** Troca completa já em mãos (resultado da navegação); sem ela, o detalhe a lê por id. */
  data?: Exchange;
}

interface ExchangeDetailProps {
  opened: OpenedExchange;
  /** Filtro ativo da lista: a navegação item a item o respeita. */
  filter: ExchangeFilter;
  onOpen: (ex: OpenedExchange) => void;
  onClose: () => void;
  routes: Load<RouteResource[]>;
  guard: CommentsGuard;
  onShowRoute: (name: string) => void;
}

type Dir = "newer" | "older";

/** A troca aberta: requisição e resposta completas, tempo decomposto e navegação. */
export function ExchangeDetail({ opened, filter, onOpen, onClose, routes, guard, onShowRoute }: ExchangeDetailProps) {
  const { id, data } = opened;
  const [ex, reload] = useResource<Exchange>(
    (signal) => (data && data.id === id ? Promise.resolve(data) : api.getExchange(id, signal)),
    [id],
  );
  const [nav, setNav] = useState<{ dir: Dir; busy: boolean; message?: string; error?: ApiError } | null>(null);
  const [drafting, setDrafting] = useState(false);
  const [created, setCreated] = useState<{ route: string; name: string; enabled: boolean } | null>(null);
  const root = useRef<HTMLDivElement>(null);

  // Troca nova: o rascunho e os avisos da anterior não valem para ela.
  useEffect(() => {
    setDrafting(false);
    setCreated(null);
    setNav(null);
  }, [id]);

  // Troca aberta só pelo id (a origem de um override, por exemplo): lida a
  // troca, quem a abriu fica sabendo da rota dela, que decide o documento ao lado.
  useEffect(() => {
    // Só a troca do id atual: a que ficou de uma abertura anterior não volta.
    if (ex.kind === "ready" && !data && ex.data.id === id) onOpen({ id: ex.data.id, data: ex.data });
  }, [ex, data, id, onOpen]);

  // Estreito, os painéis empilham e o detalhe fica abaixo da lista: abrir uma
  // troca rola a página até ele. Espera a troca carregar: antes disso a página
  // ainda é curta demais para chegar lá.
  const ready = ex.kind !== "loading";
  useEffect(() => {
    if (ready && window.matchMedia("(max-width: 900px)").matches) {
      root.current?.closest(".panel")?.scrollIntoView({ block: "start" });
    }
  }, [id, ready]);

  // Uma navegação por vez; trocar de troca ou fechar o detalhe a cancela, para
  // uma resposta atrasada não reabrir nada nem chegar fora de ordem.
  const navCtl = useRef<AbortController | null>(null);
  useEffect(
    () => () => {
      navCtl.current?.abort();
      navCtl.current = null;
    },
    [id],
  );
  const go = useCallback(
    (dir: Dir) => {
      if (navCtl.current) return;
      const ctl = new AbortController();
      navCtl.current = ctl;
      setNav({ dir, busy: true });
      (dir === "older" ? api.olderExchange(id, filter, ctl.signal) : api.newerExchange(id, filter, ctl.signal)).then(
        (next) => {
          if (ctl.signal.aborted) return;
          navCtl.current = null;
          setNav(null);
          onOpen({ id: next.id, data: next });
        },
        (e: unknown) => {
          if (ctl.signal.aborted) return;
          navCtl.current = null;
          const err = toApiError(e);
          setNav(
            isApiError(err, "no_more")
              ? { dir, busy: false, message: err.message }
              : { dir, busy: false, error: err },
          );
        },
      );
    },
    [id, filter, onOpen],
  );

  // j e k percorrem as trocas de qualquer ponto da tela, fora de campos de texto.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey) return;
      if (e.key !== "j" && e.key !== "k") return;
      if (navCtl.current) return; // já há uma navegação a caminho
      const t = e.target as HTMLElement | null;
      if (t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return;
      if (t?.closest("[role=alertdialog]")) return;
      e.preventDefault();
      go(e.key === "j" ? "older" : "newer");
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [go]);

  const filterText = describeFilter(filter);
  const busy = nav?.busy === true;

  const bar = (
    <div className="xd__bar">
      <span className="xd__nav" role="group" aria-label="Percorrer as trocas">
        <button
          type="button"
          className="button button--sm"
          onClick={() => go("newer")}
          disabled={busy}
          aria-keyshortcuts="k"
          title="Troca mais nova com o filtro ativo (tecla k)"
        >
          <ArrowIcon dir="up" /> mais nova <kbd>k</kbd>
        </button>
        <button
          type="button"
          className="button button--sm"
          onClick={() => go("older")}
          disabled={busy}
          aria-keyshortcuts="j"
          title="Troca mais antiga com o filtro ativo (tecla j)"
        >
          <ArrowIcon dir="down" /> mais antiga <kbd>j</kbd>
        </button>
      </span>
      <span className="xd__navnote" role="status">
        {busy ? "lendo…" : nav?.message ? nav.message : filterText ? `filtro: ${filterText}` : "sem filtro"}
      </span>
      <button type="button" className="icon-button xd__close" onClick={onClose} aria-label="Fechar a troca">
        <CloseIcon />
      </button>
    </div>
  );

  let body;
  if (ex.kind === "loading") {
    body = <Loading what="a troca" />;
  } else if (ex.kind === "error") {
    const e = ex.error;
    body =
      e.code === "not_found" ? (
        <Empty title="A troca saiu do histórico">
          <p>
            <span className="mono">…{id.slice(-8)}</span> não está mais no backend: ele guarda um número limitado de
            trocas e descarta as mais antigas. As vizinhas ainda podem ser percorridas.
          </p>
        </Empty>
      ) : e.code === "history_disabled" ? (
        <Empty title="Histórico desabilitado">
          <p>{e.message}</p>
        </Empty>
      ) : (
        <div className="xd__pad">
          <ErrorNote error={e} what={`GET /api/exchanges/${id} falhou`} />
          <button type="button" className="link-button" onClick={reload}>
            Tentar de novo
          </button>
        </div>
      );
  } else {
    const x = ex.data;
    body = (
      <>
        <Summary x={x} onShowRoute={onShowRoute} />
        <DeriveBlock
          x={x}
          routes={routes}
          drafting={drafting}
          created={created}
          onDraft={() => {
            setCreated(null);
            setDrafting(true);
          }}
          onShowRoute={onShowRoute}
        />
        {drafting ? (
          <DeriveDraft
            exchange={x}
            routes={routes}
            guard={guard}
            onCancel={() => setDrafting(false)}
            onCreated={(c) => {
              setDrafting(false);
              setCreated(c);
            }}
          />
        ) : null}
        <TimeLanes x={x} />
        <MessageSection title="requisição" m={x.request} x={x} kind="request" />
        <MessageSection title="resposta" m={x.response} x={x} kind="response" />
        <Routing x={x} />
      </>
    );
  }

  return (
    <div
      className="xd"
      ref={root}
      onKeyDown={(e) => {
        if (e.key !== "Escape" || e.defaultPrevented) return;
        const t = e.target as HTMLElement;
        if (/^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName)) return;
        e.preventDefault();
        onClose();
      }}
    >
      {bar}
      {nav?.error ? (
        <div className="xd__pad">
          <ErrorNote error={nav.error} onDismiss={() => setNav(null)} what="A navegação falhou" />
        </div>
      ) : null}
      {body}
    </div>
  );
}

function outcomeText(x: Exchange): { text: string; fault: boolean } {
  const by = x.override ? ` por ${x.override}` : "";
  switch (x.outcome) {
    case "synthesized":
      return { text: `resposta sintetizada${by}`, fault: true };
    case "dropped":
      return {
        text: `conexão derrubada${by}${x.dropMode === "stream_reset" ? " (stream HTTP/2 cancelado)" : ""}`,
        fault: true,
      };
    case "gateway":
      return { text: "erro do próprio gateway", fault: false };
    default:
      return {
        text: (x.status ?? 0) >= 500 ? "erro do upstream, repassado sem intervenção" : "respondida pelo upstream",
        fault: false,
      };
  }
}

function Summary({ x, onShowRoute }: { x: Exchange; onShowRoute: (name: string) => void }) {
  const o = outcomeText(x);
  const delayed = (x.interventions ?? []).includes("delayed");
  return (
    <header className="xd__head">
      <p className="xd__req">
        <span className="xd__method">{x.method}</span> <span className="xd__path">{x.path}</span>
        {x.query ? <span className="dim">?{x.query}</span> : null}
      </p>
      <p className="xd__verdict">
        <span className={"xd__status mono" + (o.fault ? " status--fault" : "")}>{x.status ? x.status : "sem status"}</span>
        <span className={o.fault ? "tone-drop" : undefined}>{o.text}</span>
        {delayed ? <span className="tone-injected">atrasada {ms(x.timing.injectedMs)}</span> : null}
        <span className="dim mono">
          {ms(x.timing.totalMs)} · {clock(x.start)}
        </span>
      </p>
      {x.error ? <p className="xd__error mono">{x.error}</p> : null}
      <p className="xd__meta dim">
        {x.route ? (
          <>
            rota{" "}
            <button type="button" className="text-button text-button--inline mono" onClick={() => onShowRoute(x.route!)}>
              {x.route}
            </button>
          </>
        ) : (
          "nenhuma rota casou"
        )}
        {x.upstream ? (
          <>
            {" · "}upstream <span className="mono">{x.upstream}</span>
          </>
        ) : null}
        {" · "}troca <span className="mono" title={x.id}>…{x.id.slice(-8)}</span>
        <span className="mono"> #{x.seq}</span>
      </p>
    </header>
  );
}

/** Ação de criar override, ou a razão de não poder, e o resultado da última criação. */
function DeriveBlock({
  x,
  routes,
  drafting,
  created,
  onDraft,
  onShowRoute,
}: {
  x: Exchange;
  routes: Load<RouteResource[]>;
  drafting: boolean;
  created: { route: string; name: string; enabled: boolean } | null;
  onDraft: () => void;
  onShowRoute: (name: string) => void;
}) {
  if (created) {
    return (
      <p className="xd__done" role="status">
        Override <span className="mono">{created.name}</span> criado em <span className="mono">{created.route}</span>,{" "}
        {created.enabled ? "já valendo" : "desligado"}. O documento ao lado mostra as linhas novas.{" "}
        <button type="button" className="link-button" onClick={() => onShowRoute(created.route)}>
          Ajustar na rota
        </button>
      </p>
    );
  }
  if (drafting) return null;
  const known = routes.kind === "ready" && x.route ? routes.data.some((r) => r.route.name === x.route) : true;
  const reason = !x.route
    ? "Nenhuma rota casou esta troca, e um override mora sempre numa rota."
    : !known
      ? `A rota ${x.route} não existe mais.`
      : x.outcome === "synthesized"
        ? "A resposta desta troca foi sintetizada por um override: não há resposta do upstream para copiar."
        : x.outcome === "dropped"
          ? "A conexão desta troca foi derrubada: não há resposta para copiar."
          : x.outcome === "gateway"
            ? "Esta troca terminou em erro do gateway: não há resposta do upstream para copiar."
            : null;
  return (
    <div className="xd__derive">
      <button type="button" className="button" onClick={onDraft} disabled={reason !== null} aria-describedby={reason ? "xd-derive-why" : undefined}>
        <PlusIcon /> Criar override a partir desta troca
      </button>
      {reason ? (
        <span className="dim" id="xd-derive-why">
          {reason}
        </span>
      ) : (
        <span className="dim">Monta um rascunho com esta requisição e esta resposta; nada é gravado até você confirmar.</span>
      )}
    </div>
  );
}

// ---------- Tempo ----------

/**
 * Passo "redondo" para as marcas do eixo: 1, 2 ou 5 vezes uma potência de 10.
 * É o menor passo que deixa no máximo `most` marcas além do zero (a largura
 * da faixa decide quantas cabem sem os rótulos se encostarem).
 */
/** Rótulo curto de uma marca redonda do eixo: "0", "200 ms", "1 s", "1,5 s". */
function tickLabel(v: number): string {
  if (v === 0) return "0";
  if (v >= 1000) {
    const s = v / 1000;
    return `${num(s, Number.isInteger(s) ? 0 : 1)} s`;
  }
  return `${num(v, Number.isInteger(v) ? 0 : 1)} ms`;
}

function tickStep(total: number, most: number): number {
  const limit = Math.max(1, most);
  const p = 10 ** Math.floor(Math.log10(total / limit));
  for (const m of [1, 2, 5, 10, 20]) if (Math.floor(total / (m * p) + 1e-9) <= limit) return m * p;
  return 20 * p;
}

/**
 * Waterfall em escala verdadeira: um eixo de 0 ao total, e cada grandeza na
 * própria faixa, começando onde a anterior termina. A ordem é a do caminho da
 * requisição: o upstream responde, o atraso injetado vem depois da resposta
 * pronta, e o overhead do gateway (somado ao longo do caminho) fecha a conta.
 */
function TimeLanes({ x }: { x: Exchange }) {
  const t = x.timing;
  const total = Math.max(t.totalMs, t.upstreamMs + t.injectedMs + t.gatewayMs, 0.001);
  const pct = (v: number) => (v / total) * 100;
  // Uma marca a cada ~64px da faixa (rótulos curtos: "1 s", "200 ms"): em
  // 390px sobram o zero e uma marca, e os rótulos nunca se encostam.
  const track = useRef<HTMLTableCellElement>(null);
  const [trackWidth, setTrackWidth] = useState(0);
  useEffect(() => {
    const el = track.current;
    if (!el) return;
    const ro = new ResizeObserver(([entry]) => setTrackWidth(entry?.contentRect.width ?? 0));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const step = tickStep(total, trackWidth > 0 ? Math.min(5, Math.floor(trackWidth / 64)) : 2);
  const ticks: number[] = [];
  for (let i = 0; i * step <= total + 1e-9; i++) ticks.push(Number((i * step).toPrecision(12)));
  // Colada ao fim da faixa, a última marca alinha o rótulo pela direita para
  // não vazar da borda.
  const endTick =
    ticks.length > 1 && trackWidth > 0 && (1 - ticks[ticks.length - 1]! / total) * trackWidth < 22
      ? ticks[ticks.length - 1]
      : undefined;
  const lanes = [
    {
      key: "upstream",
      label: "upstream",
      value: t.upstreamMs,
      start: 0,
      empty:
        x.outcome === "synthesized"
          ? "não consultado"
          : x.outcome === "dropped"
            ? "conexão derrubada"
            : x.outcome === "gateway"
              ? "sem resposta"
              : "0 ms",
    },
    { key: "injected", label: "injetado", value: t.injectedMs, start: t.upstreamMs, empty: "sem atraso" },
    { key: "gateway", label: "gateway", value: t.gatewayMs, start: t.upstreamMs + t.injectedMs, empty: "0 ms" },
  ];
  const headId = useId();
  return (
    <section className="xd__section" aria-labelledby={headId}>
      <h3 className="section-title" id={headId}>
        tempo
        <span className="section-title__note">
          total <span className="mono">{ms(t.totalMs)}</span>
          {t.injectedMs > 0 ? (
            <>
              {" "}
              · <span className="tone-injected">{Math.round(pct(t.injectedMs))}% injetado</span>
            </>
          ) : null}
        </span>
      </h3>
      <table className="lanes">
        <caption className="sr-only">
          Decomposição do tempo da troca, em escala linear de 0 a {ms(total)}
        </caption>
        <tbody>
          {lanes.map((l) => (
            <tr key={l.key} className={l.value > 0 ? undefined : "is-zero"}>
              <th scope="row" className="lanes__label">
                {l.label}
              </th>
              <td className="lanes__track">
                <span className="lanes__rail">
                  {l.value > 0 ? (
                    <span
                      className={`lanes__seg fall__seg--${l.key}`}
                      style={{ left: `${pct(l.start)}%`, width: `${pct(l.value)}%` }}
                    />
                  ) : null}
                </span>
              </td>
              <td className={"lanes__value" + (l.value > 0 ? " mono" : " dim")}>
                {l.value > 0 ? (
                  <>
                    {ms(l.value)} <span className="dim">{Math.round(pct(l.value))}%</span>
                  </>
                ) : (
                  l.empty
                )}
              </td>
            </tr>
          ))}
          <tr className="lanes__axis" aria-hidden="true">
            <th />
            <td className="lanes__track" ref={track}>
              <span className="lanes__ticks">
                {ticks.map((v) => (
                  <span
                    key={v}
                    className={"lanes__tick" + (v === endTick ? " lanes__tick--end" : "")}
                    style={{ left: `${pct(v)}%` }}
                  >
                    {tickLabel(v)}
                  </span>
                ))}
              </span>
            </td>
            <td />
          </tr>
        </tbody>
      </table>
      {t.injectedMs > 0 ? (
        <p className="hint">
          O atraso é aplicado depois que a resposta fica pronta, então soma ao tempo do upstream em vez de se sobrepor a
          ele.
        </p>
      ) : null}
    </section>
  );
}

// ---------- Requisição e resposta ----------

function MessageSection({ title, m, x, kind }: { title: string; m: Message; x: Exchange; kind: "request" | "response" }) {
  const headId = useId();
  const decoded = decodeMessage(m);
  const headers = Object.entries(m.headers ?? {})
    .filter((e): e is [string, string[]] => Array.isArray(e[1]) && e[1].length > 0)
    .sort(([a], [b]) => a.localeCompare(b));
  const noResponse = kind === "response" && (x.outcome === "dropped" || (!x.status && headers.length === 0));
  return (
    <section className="xd__section" aria-labelledby={headId}>
      <h3 className="section-title" id={headId}>
        {title}
        <span className="section-title__note">
          {kind === "request" ? (
            <span className="mono">
              {x.host}
              {x.clientAddr ? ` · de ${x.clientAddr}` : ""}
            </span>
          ) : noResponse ? (
            "nenhuma resposta foi escrita"
          ) : (
            <span className="mono">status {x.status}</span>
          )}
        </span>
      </h3>
      {noResponse ? null : (
        <>
          {headers.length ? (
            <dl className="hdrs">
              {headers.map(([k, vs]) => (
                <div className="hdrs__row" key={k}>
                  <dt className="mono">{k}</dt>
                  <dd className="mono">
                    {vs.map((v, i) => (
                      <span key={i} className="hdrs__val">
                        {v}
                      </span>
                    ))}
                  </dd>
                </div>
              ))}
            </dl>
          ) : (
            <p className="dim xd__pad-x">sem cabeçalhos</p>
          )}
          <BodyView d={decoded} />
        </>
      )}
    </section>
  );
}

function BodyView({ d }: { d: DecodedBody }) {
  const v = d.view;
  const kindLabel = v.kind === "json" ? (v.partial ? "JSON incompleto" : "JSON") : v.kind === "text" ? "texto" : v.kind === "binary" ? "binário" : null;
  return (
    <div className="body">
      <p className="body__head">
        <span className="body__label">corpo</span>
        {v.kind === "empty" ? (
          <span className="dim">{d.size > 0 ? `${bytes(d.size)} não capturados` : "vazio"}</span>
        ) : (
          <span className="dim">
            {kindLabel}
            {d.contentType ? <span className="mono"> · {d.contentType}</span> : null}
            <span className="mono"> · {bytes(d.size)}</span>
          </span>
        )}
      </p>
      {d.truncated ? (
        <p className="body__cut" role="note">
          <strong>Corpo cortado na captura:</strong> guardados <span className="mono">{bytes(d.captured, d.size)}</span> de{" "}
          <span className="mono">{bytes(d.size)}</span> (limite <span className="mono">capture.maxBodyBytes</span>). O
          final não aparece aqui e o JSON pode não fechar.
        </p>
      ) : null}
      {v.kind === "json" || v.kind === "text" ? (
        <>
          <pre className={"code" + (d.truncated ? " code--cut" : "")} tabIndex={0} aria-label="Conteúdo do corpo">
            {v.text}
          </pre>
          {d.truncated ? (
            <p className="body__tail">
              cortado aqui: faltam <span className="mono">{bytes(d.size - d.captured)}</span> que não foram capturados
            </p>
          ) : null}
        </>
      ) : null}
      {v.kind === "binary" ? (
        <>
          <p className="dim xd__pad-x">
            Conteúdo binário: os primeiros {v.shown} bytes em hexadecimal
            {d.captured > v.shown ? `, de ${bytes(d.captured)} capturados` : ""}.
          </p>
          <pre className="code code--hex" tabIndex={0} aria-label="Início do corpo em hexadecimal">
            {v.hex}
          </pre>
        </>
      ) : null}
    </div>
  );
}

function Routing({ x }: { x: Exchange }) {
  const headId = useId();
  const rows: [string, string | undefined][] = [
    ["override", x.override],
    ["intervenções", (x.interventions ?? []).length ? (x.interventions ?? []).join(", ") : "nenhuma"],
    ["resultado", x.outcome],
    ["queda", x.dropMode],
    ["início", x.start],
    ["id", x.id],
  ];
  return (
    <section className="xd__section" aria-labelledby={headId}>
      <h3 className="section-title" id={headId}>
        registro
      </h3>
      <dl className="hdrs">
        {rows
          .filter(([, v]) => v)
          .map(([k, v]) => (
            <div className="hdrs__row" key={k}>
              <dt>{k}</dt>
              <dd className="mono">{v}</dd>
            </div>
          ))}
      </dl>
    </section>
  );
}
