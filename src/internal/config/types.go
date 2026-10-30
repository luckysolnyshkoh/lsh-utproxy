package config

import "time"

type Config struct {
	Global    Global     `json:"global"`
	Defaults  Defaults   `json:"defaults"`
	Frontends []Frontend `json:"frontends"`
	Backends  []Backend  `json:"backends"`
}

type Global struct {
	LogLevel      string        `json:"log_level"`
	LogFormat     string        `json:"log_format"`
	LogTarget     string        `json:"log_target"`
	MaxConn       int           `json:"max_conn"`
	MaxProcs      int           `json:"max_procs"`
	StatsSocket   string        `json:"stats_socket"`
	StatsTimeout  time.Duration `json:"stats_timeout"`
	GraceShutdown time.Duration `json:"grace_shutdown"`
	Chroot        string        `json:"chroot"`
	User          string        `json:"user"`
	Group         string        `json:"group"`
	Daemon        bool          `json:"daemon"`
}

type Defaults struct {
	Mode               string        `json:"mode"`
	Balance            string        `json:"balance"`
	TimeoutConnect     time.Duration `json:"timeout_connect"`
	TimeoutClient      time.Duration `json:"timeout_client"`
	TimeoutServer      time.Duration `json:"timeout_server"`
	TimeoutTunnel      time.Duration `json:"timeout_tunnel"`
	TimeoutIdle        time.Duration `json:"timeout_idle"`
	TimeoutCheck       time.Duration `json:"timeout_check"`
	MaxConn            int           `json:"max_conn"`
	MaxConnFrontend    int           `json:"max_conn_frontend"`
	MaxConnBackend     int           `json:"max_conn_backend"`
	OptionTCPKeepAlive bool          `json:"option_tcp_keepalive"`
	TCPKeepAliveIdle   time.Duration `json:"tcp_keepalive_idle"`
	TCPKeepAliveIntvl  time.Duration `json:"tcp_keepalive_intvl"`
	TCPKeepAliveCnt    int           `json:"tcp_keepalive_cnt"`
	BufferSizeRead     int           `json:"buffer_size_read"`
	BufferSizeWrite    int           `json:"buffer_size_write"`
	UDPSessionTTL      time.Duration `json:"udp_session_ttl"`
	UDPMaxSessions     int           `json:"udp_max_sessions"`
	UDPBufferSize      int           `json:"udp_buffer_size"`
	ReUsePort          bool          `json:"re_use_port"`
}

type Frontend struct {
	Name               string            `json:"name"`
	Mode               string            `json:"mode"`
	Bind               []Bind            `json:"bind"`
	DefaultBackend     string            `json:"default_backend"`
	Options            map[string]string `json:"options"`
	MaxConn            int               `json:"max_conn"`
	TimeoutClient      time.Duration     `json:"timeout_client"`
	TimeoutTunnel      time.Duration     `json:"timeout_tunnel"`
	TimeoutIdle        time.Duration     `json:"timeout_idle"`
	ACLs               []ACL             `json:"acls"`
	UseBackendRules    []UseBackend      `json:"use_backend_rules"`
	OptionTCPKeepAlive bool              `json:"option_tcp_keepalive"`
	// AccessRules is the ordered allow/deny/drop list of this frontend (a
	// listen block fills the frontend half). The first rule that matches the
	// source address wins; the rest of the list is not evaluated.
	AccessRules []AccessRule `json:"access_rules"`
}

// AccessRule is one "allow|deny|drop <cidr>" line. Addr keeps the text as it
// was written; it is compiled into networks by internal/acl at startup.
type AccessRule struct {
	Action string `json:"action"` // allow | deny | drop
	Addr   string `json:"addr"`   // all, localhost, 10.0.0.0/16, 173.24.5.136, ::1/128
	Line   int    `json:"line"`   // config file line, for log and error messages
}

type Bind struct {
	Addr      string `json:"addr"`
	Port      int    `json:"port"`
	Interface string `json:"interface,omitempty"`
	SSL       bool   `json:"ssl,omitempty"`
	Verify    string `json:"verify,omitempty"`
}

type ACL struct {
	Name      string `json:"name"`
	Criterion string `json:"criterion"`
	Value     string `json:"value"`
}

type UseBackend struct {
	Backend   string `json:"backend"`
	Condition string `json:"condition"`
}

type Backend struct {
	Name               string            `json:"name"`
	Mode               string            `json:"mode"`
	Balance            string            `json:"balance"`
	Servers            []Server          `json:"servers"`
	Options            map[string]string `json:"options"`
	MaxConn            int               `json:"max_conn"`
	TimeoutConnect     time.Duration     `json:"timeout_connect"`
	TimeoutServer      time.Duration     `json:"timeout_server"`
	TimeoutTunnel      time.Duration     `json:"timeout_tunnel"`
	TimeoutIdle        time.Duration     `json:"timeout_idle"`
	TimeoutCheck       time.Duration     `json:"timeout_check"`
	OptionTCPKeepAlive bool              `json:"option_tcp_keepalive"`
	TCPKeepAliveIdle   time.Duration     `json:"tcp_keepalive_idle"`
	TCPKeepAliveIntvl  time.Duration     `json:"tcp_keepalive_intvl"`
	TCPKeepAliveCnt    int               `json:"tcp_keepalive_cnt"`
	BufferSizeRead     int               `json:"buffer_size_read"`
	BufferSizeWrite    int               `json:"buffer_size_write"`
	UDPSessionTTL      time.Duration     `json:"udp_session_ttl"`
	UDPMaxSessions     int               `json:"udp_max_sessions"`
	UDPBufferSize      int               `json:"udp_buffer_size"`
	ReUsePortBackend   bool              `json:"re_use_port_backend"`
	CheckInterval      time.Duration     `json:"check_interval"`
	CheckFall          int               `json:"check_fall"`
	CheckRise          int               `json:"check_rise"`
}

type Server struct {
	Name    string        `json:"name"`
	Addr    string        `json:"addr"`
	Weight  int           `json:"weight"`
	MaxConn int           `json:"max_conn"`
	Backup  bool          `json:"backup"`
	Check   bool          `json:"check"`
	Inter   time.Duration `json:"inter"`
	Fall    int           `json:"fall"`
	Rise    int           `json:"rise"`
	Port    int           `json:"port"`
}
