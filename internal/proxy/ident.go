package proxy

import "strings"

// HeaderGateway é o único cabeçalho próprio que o gateway acrescenta ao
// tráfego, na requisição ao upstream e na resposta ao cliente.
const HeaderGateway = "X-Gateway"

// Ident é o conteúdo do cabeçalho X-Gateway: a rota casada e, quando um
// override intervém, o override responsável e o tipo de intervenção.
type Ident struct {
	Route        string // nome da rota casada
	Override     string // "rota/override", quando um override intervém
	Intervention string // tipo de intervenção, como "synthesized"
}

// String monta o valor no formato "route=x; override=x/y;
// intervention=synthesized", omitindo as partes vazias. Sem nenhuma parte
// (resposta sem rota casada), o valor é vazio e o cabeçalho segue presente,
// marcando a resposta como produzida pelo gateway.
func (id Ident) String() string {
	parts := make([]string, 0, 3)
	for _, p := range [...]struct{ key, val string }{
		{"route", id.Route},
		{"override", id.Override},
		{"intervention", id.Intervention},
	} {
		if p.val != "" {
			parts = append(parts, p.key+"="+p.val)
		}
	}
	return strings.Join(parts, "; ")
}
