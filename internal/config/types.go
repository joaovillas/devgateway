package config

// SchemaVersion é a maior versão de schema que este binário sabe interpretar,
// tanto em gateway.json quanto nos documentos de rota.
const SchemaVersion = 1

// GatewayFile é o conteúdo de gateway.json. Todos os campos são opcionais:
// o que não está no arquivo vem do ambiente ou do padrão embutido.
type GatewayFile struct {
	SchemaVersion *int         `json:"schemaVersion,omitempty" yaml:"schemaVersion,omitempty"`
	Ports         *PortsFile   `json:"ports,omitempty" yaml:"ports,omitempty"`
	Seed          *uint64      `json:"seed,omitempty" yaml:"seed,omitempty"`
	History       *HistoryFile `json:"history,omitempty" yaml:"history,omitempty"`
	Capture       *CaptureFile `json:"capture,omitempty" yaml:"capture,omitempty"`
	RoutesDir     *string      `json:"routesDir,omitempty" yaml:"routesDir,omitempty"`
}

type PortsFile struct {
	Traffic *int `json:"traffic,omitempty" yaml:"traffic,omitempty"`
	Admin   *int `json:"admin,omitempty" yaml:"admin,omitempty"`
}

type HistoryFile struct {
	// Backend é memory, ndjson ou sqlite.
	Backend *string `json:"backend,omitempty" yaml:"backend,omitempty"`
	// Path é o arquivo usado pelos backends ndjson e sqlite.
	Path *string `json:"path,omitempty" yaml:"path,omitempty"`
	// Capacity é o número de trocas mantidas pelo backend memory.
	Capacity *int `json:"capacity,omitempty" yaml:"capacity,omitempty"`
	// Record liga o registro das trocas.
	Record *bool `json:"record,omitempty" yaml:"record,omitempty"`
	// Expose liga a leitura do histórico pela API.
	Expose *bool `json:"expose,omitempty" yaml:"expose,omitempty"`
}

type CaptureFile struct {
	// MaxBodyBytes é o limite a partir do qual corpos são truncados.
	MaxBodyBytes *int `json:"maxBodyBytes,omitempty" yaml:"maxBodyBytes,omitempty"`
}

// Route é um documento de rota em routes/.
type Route struct {
	SchemaVersion int        `json:"schemaVersion" yaml:"schemaVersion"`
	Name          string     `json:"name" yaml:"name"`
	Upstream      string     `json:"upstream,omitempty" yaml:"upstream,omitempty"`
	Match         RouteMatch `json:"match" yaml:"match"`
	// StripPrefix remove a parte fixa do padrão de path antes de encaminhar.
	StripPrefix bool `json:"stripPrefix,omitempty" yaml:"stripPrefix,omitempty"`
	// PreserveHost mantém o Host original em vez do host do upstream.
	PreserveHost bool `json:"preserveHost,omitempty" yaml:"preserveHost,omitempty"`
	// Timeout é o tempo limite de resposta do upstream (504 ao excedê-lo).
	Timeout   *Duration  `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Overrides []Override `json:"overrides,omitempty" yaml:"overrides,omitempty"`
}

// RouteMatch define o casamento da rota. Pelo menos um dos dois é exigido.
type RouteMatch struct {
	Host string `json:"host,omitempty" yaml:"host,omitempty"`
	// Path é exato ("/health") ou curinga de sufixo ("/api/payments/*").
	Path string `json:"path,omitempty" yaml:"path,omitempty"`
}

// Override intercepta parte do tráfego de uma rota.
type Override struct {
	Name  string        `json:"name" yaml:"name"`
	Match OverrideMatch `json:"match" yaml:"match"`
	// Respond é a resposta sintetizada. Sem ele, o override só atrasa ou derruba.
	Respond *Respond `json:"respond,omitempty" yaml:"respond,omitempty"`
	// Probability é a fração das requisições selecionadas em que o override vale.
	// Ausente equivale a 1.0.
	Probability *float64 `json:"probability,omitempty" yaml:"probability,omitempty"`
	Latency     *Latency `json:"latency,omitempty" yaml:"latency,omitempty"`
	Drop        bool     `json:"drop,omitempty" yaml:"drop,omitempty"`
	// TTL é o tempo de vida a partir do registro do override.
	TTL *Duration `json:"ttl,omitempty" yaml:"ttl,omitempty"`
	// MaxApplications é o número máximo de aplicações antes de expirar.
	MaxApplications *int `json:"maxApplications,omitempty" yaml:"maxApplications,omitempty"`
}

// OverrideMatch seleciona requisições. Todos os critérios declarados precisam casar.
type OverrideMatch struct {
	// Path é exato ou curinga de sufixo. Exclusivo com PathRegex.
	Path      string             `json:"path,omitempty" yaml:"path,omitempty"`
	PathRegex string             `json:"pathRegex,omitempty" yaml:"pathRegex,omitempty"`
	Method    string             `json:"method,omitempty" yaml:"method,omitempty"`
	Headers   map[string]Matcher `json:"headers,omitempty" yaml:"headers,omitempty"`
	Query     map[string]Matcher `json:"query,omitempty" yaml:"query,omitempty"`
	Body      *Matcher           `json:"body,omitempty" yaml:"body,omitempty"`
}

// Respond é a resposta declarada. Body é texto ou uma estrutura serializada como JSON.
type Respond struct {
	Status  int               `json:"status,omitempty" yaml:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	Body    any               `json:"body,omitempty" yaml:"body,omitempty"`
}
