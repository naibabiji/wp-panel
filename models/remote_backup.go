package models

type RemoteBackupSettings struct {
	Enabled        bool   `json:"enabled"`
	BackupType     string `json:"backup_type"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Username       string `json:"username"`
	AuthType       string `json:"auth_type"`
	ConnectionMode string `json:"connection_mode"`
	ServerID       string `json:"server_id"`
	Password       string `json:"password,omitempty"`
	SSHKey         string `json:"ssh_key,omitempty"`
	RemotePath     string `json:"remote_path"`
	RemoteBasePath string `json:"remote_base_path"`
	IsolatePath    bool   `json:"isolate_path"`
	KeepLocal      bool   `json:"keep_local"`
	S3Endpoint     string `json:"s3_endpoint"`
	S3Bucket       string `json:"s3_bucket"`
	S3Region       string `json:"s3_region"`
	S3AccessKeyID  string `json:"s3_access_key_id"`
	S3SecretKey    string `json:"s3_secret_key,omitempty"`
	S3PathPrefix   string `json:"s3_path_prefix"`
	S3BasePrefix   string `json:"s3_base_prefix"`
}
