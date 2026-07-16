// Package rpc is the authenticated Unix-socket protocol between the
// unprivileged panel-api and the privileged panel-agent.
//
// Security model: the API never executes shell strings as root. It sends
// typed commands; the agent validates every parameter and performs the
// operation itself. Transport is newline-delimited JSON over a Unix socket
// whose file mode restricts connections, plus a shared-secret handshake.
package rpc

import "encoding/json"

// Request is one command envelope.
type Request struct {
	ID     uint64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is the reply envelope for a Request with the same ID.
type Response struct {
	ID     uint64          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// handshake is the first message on every connection.
type handshake struct {
	Auth string `json:"auth"`
}

// Method names. The agent registers a handler per method; unknown methods
// are rejected.
const (
	MethodPing             = "Ping"
	MethodSystemStatus     = "SystemStatus"
	MethodCreateSite       = "CreateSite"
	MethodDeleteSite       = "DeleteSite"
	MethodApplySiteConfig  = "ApplySiteConfig"
	MethodIssueCertificate = "IssueCertificate"
	MethodCreateDatabase   = "CreateDatabase"
	MethodDropDatabase     = "DropDatabase"
	MethodDeployRelease    = "DeployRelease"
	MethodPromoteRelease   = "PromoteRelease"
	MethodRollbackRelease  = "RollbackRelease"
	MethodCreateStaging    = "CreateStaging"
	MethodSyncStagingDB    = "SyncStagingDatabase"
	MethodRunBackup        = "RunBackup"
	MethodRestoreSnapshot  = "RestoreSnapshot"
	MethodVerifyBackup     = "VerifyBackup"
	MethodPurgeCache       = "PurgeCache"
	MethodReloadWebServer  = "ReloadWebServer"
	MethodCheckDrift       = "CheckDrift"

	// v1.1 / v1.2
	MethodRestartService  = "RestartService"
	MethodServiceStatus   = "ServiceStatus"
	MethodTailLog         = "TailLog"
	MethodWriteCrontab    = "WriteCrontab"
	MethodRunCron         = "RunCron"
	MethodFirewallStatus  = "FirewallStatus"
	MethodFirewallRule    = "FirewallRule"
	MethodDBQuery         = "DBQuery"
	MethodDBExport        = "DBExport"
	MethodDBImport        = "DBImport"
	MethodLaunchAdminer   = "LaunchAdminer"
	MethodListFiles       = "ListFiles"
	MethodReadFile        = "ReadFile"
	MethodWriteFile       = "WriteFile"
	MethodTransferFile    = "TransferFile"
	MethodManageFile      = "ManageFile"
	MethodSetSFTP         = "SetSFTP"
	MethodSSHKeys         = "SSHKeys"
	MethodWPMagicLogin    = "WPMagicLogin"
	MethodWPPlugins       = "WPPlugins"
	MethodWPUpdate        = "WPUpdate"
	MethodWPObjectCache   = "WPObjectCache"
	MethodPanelCert       = "PanelCertificate"
	MethodSelfUpdate      = "SelfUpdate"
	MethodCacheStats      = "CacheStats"
	MethodWarmCache       = "WarmCache"
	MethodTestBackup      = "TestBackup"
	MethodImportMigration = "ImportMigration"
)
