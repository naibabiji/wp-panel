package models

type RemoteBackupSettings struct {
	Enabled    bool   `json:"enabled"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	AuthType   string `json:"auth_type"`
	Password   string `json:"password,omitempty"`
	SSHKey     string `json:"ssh_key,omitempty"`
	RemotePath string `json:"remote_path"`
	KeepLocal  bool   `json:"keep_local"`
}
