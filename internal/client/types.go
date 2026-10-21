package client

// Tipos del cable: subconjunto de los DTO del servidor (internal/transport/httpapi/dto) que usa el provider.

type Quota struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
	Pods   int64  `json:"pods"`
	PVCs   int64  `json:"pvcs"`
}

type Egress struct {
	CIDR   string `json:"cidr"`
	Port   int64  `json:"port"`
	Reason string `json:"reason"`
}

type Network struct {
	Domains       []string `json:"domains"`
	Internet      bool     `json:"internet"`
	InternetPorts []int64  `json:"internet_ports"`
	Egress        []Egress `json:"egress"`
}

type Service struct {
	Service    string   `json:"service"`
	SecretName string   `json:"secret_name"`
	Dedicated  bool     `json:"dedicated"`
	Resources  []string `json:"resources"`
	AddedBy    string   `json:"added_by"`
	AddedAt    string   `json:"added_at"`
}

type Env struct {
	Name       string    `json:"name"`
	Namespace  string    `json:"namespace"`
	Status     string    `json:"status"`
	Error      *string   `json:"error"`
	Quota      Quota     `json:"quota"`
	Hosts      []string  `json:"hosts"`
	CreatedBy  string    `json:"created_by"`
	ArchivedAt *string   `json:"archived_at"`
	Services   []Service `json:"services"`
	Network    Network   `json:"network"`
}

type Key struct {
	ID      string  `json:"id"`
	Created string  `json:"created"`
	Expires *string `json:"expires"`
	Revoked bool    `json:"revoked"`
}

type Account struct {
	Name    string   `json:"name"`
	ID      string   `json:"id"`
	Role    string   `json:"role"`
	Revoked bool     `json:"revoked"`
	Keys    []Key    `json:"keys"`
	Envs    []string `json:"envs"`
}

type Binding struct {
	ID           string            `json:"id"`
	Kind         string            `json:"kind"`
	Issuer       string            `json:"issuer"`
	ProjectPath  string            `json:"project_path"`
	RefProtected bool              `json:"ref_protected"`
	Claims       map[string]string `json:"claims"`
	Role         string            `json:"role"`
	Envs         []string          `json:"envs"`
	Branches     []string          `json:"branches"`
}

type Member struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type Links struct {
	Pool        *string  `json:"pool"`
	Replication []string `json:"replication"`
}

type App struct {
	ManagedBy   string    `json:"managed_by"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	Description string    `json:"description"`
	Group       string    `json:"group"`
	Status      string    `json:"status"`
	Error       *string   `json:"error"`
	Quota       Quota     `json:"quota"`
	CreatedBy   string    `json:"created_by"`
	ArchivedAt  *string   `json:"archived_at"`
	Envs        []Env     `json:"envs"`
	Accounts    []Account `json:"accounts"`
	Bindings    []Binding `json:"bindings"`
	Members     []Member  `json:"members"`
	Links       Links     `json:"links"`
}

// Estados de una App o entorno.
const (
	StatusActive       = "active"
	StatusProvisioning = "provisioning"
	StatusArchived     = "archived"
	StatusError        = "error"
)

func (a App) Env(name string) (Env, bool) {
	for _, e := range a.Envs {
		if e.Name == name {
			return e, true
		}
	}
	return Env{}, false
}

// ---- peticiones ----

type CreateEnvReq struct {
	Name  string `json:"name"`
	Quota *Quota `json:"quota"`
}

type CreateAppReq struct {
	Owners      []string       `json:"owners,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Owner       string         `json:"owner,omitempty"`
	Quota       *Quota         `json:"quota"`
	Envs        []CreateEnvReq `json:"envs"`
}

type SettingsReq struct {
	DisplayName *string `json:"display_name,omitempty"`
	Description *string `json:"description,omitempty"`
	Group       *string `json:"group,omitempty"`
}

type NetworkReq struct {
	Domains       []string `json:"domains"`
	Internet      bool     `json:"internet"`
	InternetPorts []int64  `json:"internet_ports"`
	Egress        []Egress `json:"egress"`
}

type AttachServiceReq struct {
	Service   string            `json:"service"`
	Options   map[string]string `json:"options"`
	Dedicated bool              `json:"dedicated"`
}

type ConnectionKey struct {
	Key    string `json:"key"`
	Secret bool   `json:"secret"`
	Value  string `json:"value,omitempty"`
}

type Connection struct {
	SecretName string          `json:"secret_name"`
	Keys       []ConnectionKey `json:"keys"`
}

type CreateAccountReq struct {
	Name          string   `json:"name"`
	Role          string   `json:"role"`
	ExpiresInDays int64    `json:"expires_in_days,omitempty"`
	Envs          []string `json:"envs"`
}

type AccountWithKey struct {
	Account Account           `json:"account"`
	KeyFile ServiceAccountKey `json:"key_file"`
}

type CreateBindingReq struct {
	Kind         string            `json:"kind"`
	Issuer       string            `json:"issuer"`
	ProjectPath  string            `json:"project_path"`
	RefProtected bool              `json:"ref_protected"`
	Claims       map[string]string `json:"claims"`
	Role         string            `json:"role"`
	Envs         []string          `json:"envs"`
	Branches     []string          `json:"branches"`
}

type AccessEntry struct {
	App     string `json:"app"`
	Hidden  bool   `json:"hidden"`
	Service string `json:"service"`
	Port    int64  `json:"port"`
}

type Access struct {
	Incoming []AccessEntry `json:"incoming"`
	Outgoing []AccessEntry `json:"outgoing"`
}

type AllowAccessReq struct {
	From    string `json:"from"`
	Service string `json:"service"`
	Port    int64  `json:"port"`
}

// ---- escalado ----

type ScaleRes struct {
	CPUMilli    int64 `json:"cpu_m"`
	MemoryBytes int64 `json:"memory_bytes"`
}

type ScaleHPA struct {
	Min          int64 `json:"min"`
	Max          int64 `json:"max"`
	CPUTarget    int64 `json:"cpu_target"`
	MemoryTarget int64 `json:"memory_target"`
	Managed      bool  `json:"managed"`
}

type ScaleVPA struct {
	Mode string   `json:"mode"`
	Min  ScaleRes `json:"min"`
	Max  ScaleRes `json:"max"`
}

type ScaleWorkload struct {
	Kind     string    `json:"kind"`
	Name     string    `json:"name"`
	Replicas int64     `json:"replicas"`
	HPA      *ScaleHPA `json:"hpa"`
	Vertical *ScaleVPA `json:"vertical"`
}

type Scaling struct {
	Workloads         []ScaleWorkload `json:"workloads"`
	VerticalAvailable bool            `json:"vertical_available"`
}

type ScaleHorizontalReq struct {
	Mode         string `json:"mode"`
	Replicas     int64  `json:"replicas"`
	Min          int64  `json:"min"`
	Max          int64  `json:"max"`
	CPUTarget    int64  `json:"cpu_target"`
	MemoryTarget int64  `json:"memory_target"`
}

type ScaleVerticalReq struct {
	Mode           string `json:"mode"`
	MinCPUMilli    int64  `json:"min_cpu_m"`
	MaxCPUMilli    int64  `json:"max_cpu_m"`
	MinMemoryBytes int64  `json:"min_memory_bytes"`
	MaxMemoryBytes int64  `json:"max_memory_bytes"`
}

type SetScalingReq struct {
	Horizontal *ScaleHorizontalReq `json:"horizontal,omitempty"`
	Vertical   *ScaleVerticalReq   `json:"vertical,omitempty"`
}

// ---- reglas por host ----

type RateLimit struct {
	Average       int64 `json:"average"`
	PeriodSeconds int64 `json:"period_seconds"`
	Burst         int64 `json:"burst"`
}

type BasicUser struct {
	Name     string `json:"name"`
	Password string `json:"password,omitempty"`
}

type CORS struct {
	Origins       []string `json:"origins"`
	Methods       []string `json:"methods"`
	Headers       []string `json:"headers"`
	Credentials   bool     `json:"credentials"`
	MaxAgeSeconds int64    `json:"max_age_seconds"`
}

// IPNote acompaña a una entrada de IP: solo `note` se envía; quién y cuándo los pone el servidor.
type IPNote struct {
	Note string `json:"note"`
	By   string `json:"by,omitempty"`
	At   string `json:"at,omitempty"`
}

// Maintenance es la página de mantenimiento de una dirección.
type Maintenance struct {
	HTML              string   `json:"html"`
	RetryAfterSeconds int64    `json:"retry_after_seconds"`
	AllowIPs          []string `json:"allow_ips"`
}

// HostRules son todas las reglas de una dirección. PUT las REEMPLAZA todas: lo que no se envía queda apagado, por eso el provider las envía completas.
type HostRules struct {
	IPAllow         []string          `json:"ip_allow"`
	IPDeny          []string          `json:"ip_deny"`
	IPNotes         map[string]IPNote `json:"ip_notes"`
	RateLimit       *RateLimit        `json:"rate_limit"`
	BasicAuth       []BasicUser       `json:"basic_auth"`
	WWW             string            `json:"www"`
	SecurityHeaders bool              `json:"security_headers"`
	Compress        bool              `json:"compress"`
	MaxBodyMB       int64             `json:"max_body_mb"`
	CORS            *CORS             `json:"cors"`
	HTTPSRedirect   bool              `json:"https_redirect"`
	TimeoutSeconds  int64             `json:"timeout_seconds"`
	Maintenance     *Maintenance      `json:"maintenance"`
}

type HostRulesView struct {
	Host  string    `json:"host"`
	Rules HostRules `json:"rules"`
	// Lo que el cluster admite: si no, las reglas se guardan pero no se aplican (el provider avisa).
	DenyAvailable        bool `json:"deny_available"`
	MaintenanceAvailable bool `json:"maintenance_available"`
	TimeoutAvailable     bool `json:"timeout_available"`
}

// ---- colas, runners, replicación, administradores ----

type JetStreamSpec struct {
	Subjects  []string `json:"subjects"`
	Retention string   `json:"retention"`
	Replicas  int64    `json:"replicas"`
	Storage   string   `json:"storage"`
	MaxAge    int64    `json:"max_age"`
	MaxMsgs   int64    `json:"max_msgs"`
	MaxBytes  int64    `json:"max_bytes"`
}

type Queue struct {
	Kind       string         `json:"kind"`
	Name       string         `json:"name"`
	Display    string         `json:"display"`
	Registered bool           `json:"registered"`
	Hidden     bool           `json:"hidden"`
	JetStream  *JetStreamSpec `json:"jetstream,omitempty"`
}

type QueueList struct {
	Items []Queue `json:"items"`
}

type CreateQueueReq struct {
	Name      string         `json:"name"`
	Display   string         `json:"display"`
	Kind      string         `json:"kind"`
	JetStream *JetStreamSpec `json:"jetstream,omitempty"`
}

type UpdateQueueReq struct {
	Display *string `json:"display,omitempty"`
	Hidden  *bool   `json:"hidden,omitempty"`
}

type RunnerPool struct {
	Name    string   `json:"name"`
	Labels  []string `json:"labels"`
	Scope   string   `json:"scope"`
	Min     int64    `json:"min"`
	Max     int64    `json:"max"`
	Origins []string `json:"origins"`
	Paused  bool     `json:"paused"`
	App     string   `json:"app"`
	Pending bool     `json:"pending"`
}

type Runners struct {
	Pools []RunnerPool `json:"pools"`
}

type CreateRunnerPoolReq struct {
	Name    string   `json:"name"`
	Labels  []string `json:"labels"`
	Scope   string   `json:"scope"`
	Min     *int64   `json:"min"`
	Max     *int64   `json:"max"`
	Origins []string `json:"origins"`
	App     string   `json:"app"`
}

type UpdateRunnerPoolReq struct {
	Paused  *bool     `json:"paused,omitempty"`
	App     *string   `json:"app,omitempty"`
	Origins *[]string `json:"origins,omitempty"`
	Min     *int64    `json:"min,omitempty"`
	Max     *int64    `json:"max,omitempty"`
}

type ProposeLinkReq struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Mode        string `json:"mode"`
	App         string `json:"app"`
}

type Proposal struct {
	Link      string `json:"link"`
	Version   int64  `json:"version"`
	State     string `json:"state"`
	NativeURL string `json:"native_url"`
}

type ReplLink struct {
	Name     string `json:"name"`
	Topology string `json:"topology"`
	Status   string `json:"status"`
}

type Admin struct {
	Email   string  `json:"email"`
	Source  string  `json:"source"`
	AddedBy string  `json:"added_by"`
	AddedAt *string `json:"added_at"`
}

type PersonApp struct {
	App  string `json:"app"`
	Role string `json:"role"`
}

type Person struct {
	Email       string      `json:"email"`
	Admin       bool        `json:"admin"`
	AdminSource string      `json:"admin_source"`
	Apps        []PersonApp `json:"apps"`
}

type Me struct {
	Email   string   `json:"email"`
	Name    string   `json:"name"`
	Roles   []string `json:"roles"`
	IsAdmin bool     `json:"is_admin"`
}

// ---- cuentas de servicio de plataforma ----

type PlatformBindingSpec struct {
	Issuer      string `json:"issuer,omitempty"`
	ProjectPath string `json:"project_path"`
	Environment string `json:"environment"`
	// Claims: condiciones extra exactas sobre claims del ID token (ref_type, ref, project_id, pipeline_source...).
	Claims map[string]string `json:"claims,omitempty"`
}

type CreatePlatformAccountReq struct {
	Name            string                `json:"name"`
	Scopes          []string              `json:"scopes"`
	Apps            []string              `json:"apps"`
	AppPrefix       string                `json:"app_prefix,omitempty"`
	ExpiresInDays   int64                 `json:"expires_in_days"`
	Bindings        []PlatformBindingSpec `json:"bindings,omitempty"`
	AllowedDomains  []string              `json:"allowed_domains"`
	AllowIdentities bool                  `json:"allow_identities"`
	AllowPorts      bool                  `json:"allow_ports"`
}

type PlatformAccount struct {
	Name            string   `json:"name"`
	ID              string   `json:"id"`
	Scopes          []string `json:"scopes"`
	Apps            []string `json:"apps"`
	AppPrefix       string   `json:"app_prefix"`
	ExpiresAt       string   `json:"expires_at"`
	AllowedDomains  []string `json:"allowed_domains"`
	AllowIdentities bool     `json:"allow_identities"`
	AllowPorts      bool     `json:"allow_ports"`
	Revoked         bool     `json:"revoked"`
	Keys            []struct {
		ID      string  `json:"id"`
		Expires *string `json:"expires"`
		Revoked bool    `json:"revoked"`
	} `json:"keys"`
	Bindings []struct {
		ProjectPath string `json:"project_path"`
		Environment string `json:"environment"`
		Issuer      string `json:"issuer"`
	} `json:"bindings"`
}

type PlatformAccountWithKey struct {
	Account PlatformAccount   `json:"account"`
	KeyFile ServiceAccountKey `json:"key_file"`
}

// ---- programación de carga ----

// ScheduleWorkload es lo que un perfil pide de una carga (min = max = 0: apagada).
type ScheduleWorkload struct {
	Min       int64 `json:"min"`
	Max       int64 `json:"max"`
	CPUm      int64 `json:"cpu_m"`
	MemMiB    int64 `json:"memory_mib"`
	LimCPUm   int64 `json:"cpu_limit_m"`
	LimMemMiB int64 `json:"memory_limit_mib"`
}

type ScheduleProfile struct {
	ID          string                      `json:"id"`
	Name        string                      `json:"name"`
	Description string                      `json:"description"`
	Color       string                      `json:"color"`
	Icon        string                      `json:"icon"`
	Workloads   map[string]ScheduleWorkload `json:"workloads"`
}

type ScheduleException struct {
	Date  string   `json:"date"`
	Label string   `json:"label"`
	Hours []string `json:"hours"` // 24 entradas; "" = esa hora sigue el horario semanal
}

// ScheduleSpec es la programación editable: cuerpo de PUT y parte de la respuesta. Weekly es 7 × 24 (0 = lunes); "" = perfil base.
type ScheduleSpec struct {
	Enabled    bool                `json:"enabled"`
	Timezone   string              `json:"timezone"`
	Base       string              `json:"base"`
	Profiles   []ScheduleProfile   `json:"profiles"`
	Weekly     [][]string          `json:"weekly"`
	Exceptions []ScheduleException `json:"exceptions"`
}

// ScheduleView es la respuesta de GET/PUT .../schedule (solo lo que usa el provider). Configured=false: todavía no hay nada guardado.
type ScheduleView struct {
	Configured bool         `json:"configured"`
	Enabled    bool         `json:"enabled"`
	Version    string       `json:"version"`
	Schedule   ScheduleSpec `json:"schedule"`
	CanEdit    bool         `json:"can_edit"`
}

// ---- puertos públicos TCP/UDP ----

// PortTarget es el workload y el puerto de contenedor al que va el tráfico.
type PortTarget struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Port int64  `json:"port"`
}

// PortSpec es un puerto editable (elemento de PUT .../ports).
type PortSpec struct {
	Name       string     `json:"name"`
	Protocol   string     `json:"protocol"`
	Port       int64      `json:"port"`
	Target     PortTarget `json:"target"`
	AllowCIDRs []string   `json:"allow_cidrs"`
}

// PortInfo es un puerto con su estado observado.
type PortInfo struct {
	PortSpec
	State     string   `json:"state"`
	LBReady   bool     `json:"lb_ready"`
	Addresses []string `json:"addresses"`
	Message   string   `json:"message"`
	Service   string   `json:"service"`
}

// PortsView es la respuesta de GET/PUT/DELETE .../ports (solo lo que usa el provider).
type PortsView struct {
	Enabled bool       `json:"enabled"`
	Version string     `json:"version"`
	Ports   []PortInfo `json:"ports"`
}

// SetPortsReq reemplaza la lista COMPLETA de puertos; Version es la de la última lectura ("" si no había puertos).
type SetPortsReq struct {
	Version string     `json:"version"`
	Ports   []PortSpec `json:"ports"`
}

// EgressTarget es el workload cuyos pods reciben la regla de salida.
type EgressTarget struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// EgressSpec es una regla de salida editable (elemento de PUT .../egress).
type EgressSpec struct {
	Name     string       `json:"name"`
	Protocol string       `json:"protocol"`
	Port     int64        `json:"port"`
	Target   EgressTarget `json:"target"`
}

// EgressInfo es una regla con su estado observado.
type EgressInfo struct {
	EgressSpec
	State   string `json:"state"`
	Message string `json:"message"`
	Policy  string `json:"policy"`
}

// EgressView es la respuesta de GET/PUT .../egress (solo lo que usa el provider).
type EgressView struct {
	Enabled bool         `json:"enabled"`
	Version string       `json:"version"`
	Rules   []EgressInfo `json:"rules"`
}

// SetEgressReq reemplaza la lista COMPLETA; Version es la de la última lectura ("" si no había reglas).
type SetEgressReq struct {
	Version string       `json:"version"`
	Rules   []EgressSpec `json:"rules"`
}
