package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/config/writer"
	"github.com/gamerjp64/gateway/internal/store"
)

func (h *Handler) settingsRoutes() {
	h.handle("/api/settings", map[string]http.HandlerFunc{
		"GET":   h.getSettings,
		"PATCH": h.patchSettings,
	})
	h.handle("/api/settings/document", map[string]http.HandlerFunc{
		"GET": h.getSettingsDocument,
		"PUT": h.putSettingsDocument,
	})
	h.handle("/api/learning", map[string]http.HandlerFunc{
		"GET": h.getLearning,
		"PUT": h.putLearning,
	})
	h.handle("/api/reload", map[string]http.HandlerFunc{
		"POST": h.reload,
	})
}

// settingValue é um valor efetivo com a indicação de que o ambiente o trava.
type settingValue struct {
	config.EffectiveValue
	// Locked é verdadeiro quando o valor vem de variável de ambiente: a API
	// recusa alterá-lo.
	Locked bool `json:"locked"`
}

type settingsFile struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

type settingsView struct {
	File   settingsFile   `json:"file"`
	Values []settingValue `json:"values"`
}

func (h *Handler) settingsView() settingsView {
	s := h.live.Load().Settings
	eff := s.Effective()
	v := settingsView{
		File:   settingsFile{Path: h.loader.ConfigPath},
		Values: make([]settingValue, len(eff)),
	}
	if st, err := os.Stat(h.loader.ConfigPath); err == nil && !st.IsDir() {
		v.File.Exists = true
	}
	for i, e := range eff {
		v.Values[i] = settingValue{EffectiveValue: e, Locked: e.Source.Origin == config.OriginEnv}
	}
	return v
}

// getSettings devolve a configuração efetiva do processo com a origem de
// cada valor: ambiente, arquivo ou padrão.
func (h *Handler) getSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.settingsView())
}

// settingsResult é a resposta de uma alteração da configuração do processo.
type settingsResult struct {
	Settings settingsView `json:"settings"`
	// Applied são as chaves cujo valor efetivo mudou.
	Applied []string `json:"applied"`
	// Notes explicam efeitos que o usuário precisa saber: a porta antiga
	// que deixou de aceitar conexões, o histórico que não foi migrado.
	Notes []string `json:"notes"`
}

// patchSettings altera gateway.json com um JSON Merge Patch sobre o formato
// do arquivo e aplica o resultado a quente. Uma chave tocada cujo valor vem
// do ambiente recusa a alteração inteira, sem tocar o arquivo.
func (h *Handler) patchSettings(w http.ResponseWriter, r *http.Request) {
	patch, err := readPatch(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.checkLocked(touchedKeys(patch, ""), nil); err != nil {
		writeErr(w, err)
		return
	}
	res, err := h.changeSettings(r.Header.Get("If-Match"), func(cur []byte) ([]byte, error) {
		return h.patchedFile(cur, patch)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	setETag(w, res.version)
	writeJSON(w, http.StatusOK, res.body)
}

// patchedFile aplica o merge patch sobre o conteúdo atual de gateway.json
// (vazio quando o arquivo não existe) e devolve o arquivo novo, serializado.
func (h *Handler) patchedFile(cur []byte, patch map[string]any) ([]byte, error) {
	var base any = map[string]any{}
	if len(bytes.TrimSpace(cur)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(cur))
		dec.UseNumber()
		if err := dec.Decode(&base); err != nil {
			// O arquivo em disco ficou ilegível desde a carga; a validação
			// dele aponta o problema.
			_, perr := config.ParseGatewayFile(h.loader.ConfigPath, cur)
			if perr == nil {
				perr = err
			}
			return nil, perr
		}
	} else {
		// Um gateway.json criado pela API declara a versão de schema.
		base = map[string]any{"schemaVersion": json.Number(fmt.Sprint(config.SchemaVersion))}
	}
	merged, err := json.Marshal(mergePatch(base, patch))
	if err != nil {
		return nil, err
	}
	var g config.GatewayFile
	if err := config.DecodeJSON(h.loader.ConfigPath, merged, &g); err != nil {
		return nil, err
	}
	return config.MarshalGatewayFile(g)
}

// touchedKeys lista as chaves da configuração do processo alcançadas pelo
// merge patch: uma folha nomeia a própria chave, e um objeto trocado por
// null ou por um valor que não é objeto alcança todas as chaves abaixo dele.
func touchedKeys(patch map[string]any, prefix string) []string {
	var out []string
	for k, v := range patch {
		path := prefix + k
		if sub, ok := v.(map[string]any); ok {
			out = append(out, touchedKeys(sub, path+".")...)
			continue
		}
		for _, e := range (config.Settings{}).Effective() {
			if e.Key == path || strings.HasPrefix(e.Key, path+".") {
				out = append(out, e.Key)
			}
		}
	}
	slices.Sort(out)
	return out
}

// checkLocked recusa alterar valores que vêm do ambiente, nomeando a
// variável responsável. keys são as chaves alcançadas pela alteração; allow,
// quando presente, dispensa a recusa de uma chave cujo valor não muda de
// fato.
func (h *Handler) checkLocked(keys []string, allow func(config.EffectiveValue) bool) error {
	for _, e := range h.live.Load().Settings.Effective() {
		if e.Source.Origin != config.OriginEnv || !slices.Contains(keys, e.Key) {
			continue
		}
		if allow != nil && allow(e) {
			continue
		}
		return &requestError{http.StatusConflict, apiError{
			Error: "locked",
			Field: e.Key,
			Env:   e.Source.Name,
			Message: fmt.Sprintf("%s vem da variável de ambiente %s e não pode ser alterado pela API; %s não foi modificado",
				e.Key, e.Source.Name, filepath.Base(h.loader.ConfigPath)),
		}}
	}
	return nil
}

// settingsChange é uma alteração de gateway.json aplicada.
type settingsChange struct {
	body    settingsResult
	version string
}

// changeSettings grava em gateway.json o conteúdo produzido por edit a partir
// do atual e aplica a configuração resultante a quente, na ordem: valida o
// arquivo novo; prepara as portas e o backend do histórico alterados; grava
// o arquivo de forma atômica; troca portas, backend e snapshot. Qualquer
// falha preserva a configuração em vigor e o arquivo intacto. Tudo acontece
// sob o mutex de escrita, de modo que duas alterações concorrentes não se
// perdem.
func (h *Handler) changeSettings(ifMatch string, edit func(cur []byte) ([]byte, error)) (settingsChange, error) {
	path := h.loader.ConfigPath
	var data []byte
	unchanged := false
	var beforeTraffic, beforeAdmin int
	change, snap, switched, err := h.replace(writer.CauseAPI, func() (*config.Snapshot, error) {
		cur, err := os.ReadFile(path)
		exists := err == nil
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("lendo %s: %w", path, err)
		}
		if ifMatch != "" && (!exists || writer.Version(cur) != ifMatch) {
			return nil, writer.ErrStale
		}
		if data, err = edit(cur); err != nil {
			return nil, err
		}
		unchanged = exists && bytes.Equal(cur, data)
		return h.settingsSnapshot(data)
	}, func() error {
		if unchanged {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("criando o diretório de %s: %w", path, err)
		}
		if err := writer.WriteFileAtomic(path, data); err != nil {
			return fmt.Errorf("gravando %s: %w", path, err)
		}
		return nil
	}, func() {
		beforeTraffic, beforeAdmin = h.currentPorts()
	})
	if err != nil {
		return settingsChange{}, err
	}
	res := settingsResult{Settings: h.settingsView(), Applied: change.Settings, Notes: []string{}}
	if res.Applied == nil {
		res.Applied = []string{}
	}
	traffic, adminPort := h.currentPorts()
	if traffic != beforeTraffic {
		res.Notes = append(res.Notes, fmt.Sprintf("porta de tráfego agora é %d; a %d deixou de aceitar conexões e conclui as requisições em curso", traffic, beforeTraffic))
	}
	if adminPort != beforeAdmin {
		res.Notes = append(res.Notes, fmt.Sprintf("porta de administração agora é %d; a %d deixa de aceitar conexões depois desta resposta", adminPort, beforeAdmin))
	}
	if switched != "" {
		s := snap.Settings
		where := store.BackendName(s)
		if where != config.BackendMemory {
			where += " (" + s.HistoryPath + ")"
		}
		res.Notes = append(res.Notes, fmt.Sprintf("histórico agora em %s; as trocas anteriores continuam no backend %s e não foram migradas", where, switched))
	}
	return settingsChange{body: res, version: writer.Version(data)}, nil
}

// settingsSnapshot monta o snapshot com gateway.json no conteúdo data. As
// rotas em vigor são mantidas; só um diretório de rotas diferente é lido do
// disco.
func (h *Handler) settingsSnapshot(data []byte) (*config.Snapshot, error) {
	cur := h.live.Load()
	s, _, err := config.SettingsFrom(h.loader.ConfigPath, data, h.loader.Getenv)
	if err != nil {
		return nil, err
	}
	if s.RoutesDir == cur.Settings.RoutesDir {
		return config.NewSnapshot(s, cur.Routes, cur.Warnings), nil
	}
	docs, warnings, err := config.ReadRoutesDir(s.RoutesDir)
	if err != nil {
		return nil, err
	}
	routes, err := config.BuildRoutes(docs)
	if err != nil {
		return nil, err
	}
	return config.NewSnapshot(s, routes, warnings), nil
}

func (h *Handler) currentPorts() (int, int) {
	if h.ports != nil {
		return h.ports()
	}
	s := h.live.Load().Settings
	return s.TrafficPort, s.AdminPort
}

// replace troca a configuração inteira pelo snapshot que load constrói,
// aplicando a quente o que vive fora dele (portas, backend do histórico)
// antes de publicá-lo. persist, quando presente, grava o que precisa ser
// gravado depois de tudo preparado e antes da troca. before roda sob o mutex
// de escrita, antes de qualquer troca. Devolve, além da alteração, o nome do
// backend do histórico anterior quando ele foi trocado, e publica o evento
// history correspondente.
func (h *Handler) replace(cause string, load func() (*config.Snapshot, error), persist func() error, before func()) (writer.Change, *config.Snapshot, string, error) {
	var switched string
	change, snap, err := h.writer.Replace(cause, func() (*config.Snapshot, error) {
		if before != nil {
			before()
		}
		return load()
	}, func(old, next *config.Snapshot) error {
		prev := h.history.Backend()
		var err error
		if h.apply != nil {
			err = h.apply(old.Settings, next.Settings, persist)
		} else if persist != nil {
			err = persist()
		}
		if err == nil && store.Reopens(old.Settings, next.Settings) {
			switched = prev
		}
		return err
	})
	if err != nil {
		return change, nil, "", err
	}
	if switched != "" {
		h.events.publish("history", historyEvent{Cause: "backend", Backend: h.history.Backend()})
	}
	return change, snap, switched, nil
}

// getSettingsDocument devolve gateway.json como está em disco. Sem o
// arquivo, devolve {} e o cabeçalho X-Gateway-File-Exists: false.
func (h *Handler) getSettingsDocument(w http.ResponseWriter, _ *http.Request) {
	data, err := os.ReadFile(h.loader.ConfigPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Gateway-File-Exists", "false")
		w.Write([]byte("{}\n"))
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal", "lendo "+h.loader.ConfigPath+": "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Gateway-File-Exists", "true")
	setETag(w, writer.Version(data))
	w.Write(data)
}

// putSettingsDocument grava gateway.json como enviado e o aplica a quente,
// com as mesmas regras de PATCH /api/settings. Um documento que altera um
// valor vindo do ambiente é recusado; um que apenas mantém o que o arquivo
// já declarava, ou repete o valor em vigor, é aceito.
func (h *Handler) putSettingsDocument(w http.ResponseWriter, r *http.Request) {
	data, err := readBody(r, []string{"application/json"})
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := config.ParseGatewayFile(h.loader.ConfigPath, data); err != nil {
		writeErr(w, err)
		return
	}
	cur, _ := os.ReadFile(h.loader.ConfigPath)
	oldDoc, newDoc := jsonTree(cur), jsonTree(data)
	var keys []string
	for _, e := range h.live.Load().Settings.Effective() {
		if !jsonEqual(valueAt(oldDoc, e.Key), valueAt(newDoc, e.Key)) {
			keys = append(keys, e.Key)
		}
	}
	err = h.checkLocked(keys, func(e config.EffectiveValue) bool {
		v := valueAt(newDoc, e.Key)
		return v != nil && jsonEqual(v, e.Value)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := h.changeSettings(r.Header.Get("If-Match"), func([]byte) ([]byte, error) { return data, nil })
	if err != nil {
		writeErr(w, err)
		return
	}
	setETag(w, res.version)
	writeJSON(w, http.StatusOK, res.body)
}

// jsonTree interpreta um JSON como árvore genérica; o que não for JSON vira
// nil.
func jsonTree(data []byte) any {
	var v any
	if json.Unmarshal(data, &v) != nil {
		return nil
	}
	return v
}

// valueAt devolve o valor na chave pontuada ("history.backend"), ou nil.
func valueAt(tree any, key string) any {
	for part := range strings.SplitSeq(key, ".") {
		m, ok := tree.(map[string]any)
		if !ok {
			return nil
		}
		tree = m[part]
	}
	return tree
}

// jsonEqual compara dois valores pela forma JSON, de modo que 9090 (int) e
// 9090 (float64) sejam iguais.
func jsonEqual(a, b any) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	var va, vb any
	json.Unmarshal(ja, &va)
	json.Unmarshal(jb, &vb)
	return reflect.DeepEqual(va, vb)
}

// learningView é o estado do modo aprendizado.
type learningView struct {
	Enabled bool          `json:"enabled"`
	Source  config.Source `json:"source"`
	Locked  bool          `json:"locked"`
	// Learned conta, por rota, os overrides gravados pelo aprendizado.
	Learned map[string]int `json:"learned"`
}

func (h *Handler) learningView() learningView {
	snap := h.live.Load()
	src := snap.Settings.Sources["learning.enabled"]
	v := learningView{
		Enabled: snap.Settings.LearningEnabled,
		Source:  src,
		Locked:  src.Origin == config.OriginEnv,
		Learned: map[string]int{},
	}
	for _, r := range snap.Routes {
		n := 0
		for _, o := range r.Doc.Overrides {
			if o.Source != nil && o.Source.Kind == config.SourceLearned {
				n++
			}
		}
		v.Learned[r.Name()] = n
	}
	return v
}

func (h *Handler) getLearning(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.learningView())
}

// putLearning liga ou desliga o modo aprendizado, gravando learning.enabled
// em gateway.json.
func (h *Handler) putLearning(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if body.Enabled == nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{
			Error: "invalid", Field: "enabled", Message: "enabled é obrigatório: true liga e false desliga o aprendizado",
		})
		return
	}
	patch := map[string]any{"learning": map[string]any{"enabled": *body.Enabled}}
	if err := h.checkLocked([]string{"learning.enabled"}, nil); err != nil {
		writeErr(w, err)
		return
	}
	if _, err := h.changeSettings(r.Header.Get("If-Match"), func(cur []byte) ([]byte, error) {
		return h.patchedFile(cur, patch)
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.learningView())
}

type reloadChanged struct {
	Routes   []string `json:"routes"`
	Settings []string `json:"settings"`
}

type reloadResult struct {
	Routes   int           `json:"routes"`
	Changed  reloadChanged `json:"changed"`
	Warnings []string      `json:"warnings"`
}

// reload relê gateway.json e o diretório de rotas e aplica o resultado a
// quente, portas e backend do histórico incluídos. Uma configuração
// inválida, ou que não pode ser aplicada, é recusada e a anterior continua
// em vigor; requisições em curso terminam sob o snapshot com que começaram.
func (h *Handler) reload(w http.ResponseWriter, _ *http.Request) {
	change, snap, _, err := h.replace(writer.CauseReload, h.loader.Load, nil, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	warnings := snap.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	writeJSON(w, http.StatusOK, reloadResult{
		Routes:   len(snap.Routes),
		Changed:  reloadChanged{Routes: change.Routes, Settings: change.Settings},
		Warnings: warnings,
	})
}
