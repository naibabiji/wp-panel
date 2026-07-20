package executor

import (
	"path"
	"strings"
)

const (
	phpSiteSecretsRoot          = "/var/wp-panel/site-secrets"
	phpDisabledFunctionsSetting = "exec,passthru,shell_exec,system,proc_open,popen,show_source"
)

func sitePHPOpenBaseDir(webRoot, domain string) string {
	return strings.Join([]string{
		webRoot,
		"/tmp",
		"/usr/share/php",
		path.Join(phpSiteSecretsRoot, domain),
	}, ":")
}

func sitePHPDisabledFunctions() string {
	return phpDisabledFunctionsSetting
}

func sitePHPRunnerOpenBaseDir(webRoot, domain, runnerDir string) string {
	return strings.Join([]string{
		sitePHPOpenBaseDir(webRoot, domain),
		runnerDir,
	}, ":")
}
