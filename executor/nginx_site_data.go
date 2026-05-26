package executor

import (
	"path/filepath"
	"strings"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/models"
)

func nginxDataFromSite(site *models.Website) *NginxSiteData {
	cfg := config.AppConfig
	aliases := splitAliases(site.Aliases)
	accessLogMode := site.AccessLogMode
	if accessLogMode == "" {
		accessLogMode = "off"
	}
	templateVer := site.TemplateVersion
	if templateVer == "" {
		templateVer = "v1.0"
	}
	fCacheTTL := site.FCacheTTL
	if fCacheTTL <= 0 {
		fCacheTTL = 300
	}

	data := &NginxSiteData{
		Domain:        site.Domain,
		Aliases:       aliases,
		ServerNames:   buildServerNames(site.Domain, aliases),
		WebRoot:       site.WebRoot,
		LogDir:        site.LogDir,
		SystemUser:    site.SystemUser,
		UseSSL:        site.SSLEnabled,
		SiteType:      site.SiteType,
		SSLCertPath:   site.SSLCertPath,
		SSLKeyPath:    site.SSLKeyPath,
		PHPProxy:      "unix:" + filepath.Join(cfg.Paths.PHPFPMSock, site.Domain+".sock"),
		TemplateVer:   templateVer,
		AccessLogMode: accessLogMode,
		FCacheEnabled: site.FCacheEnabled,
		FCacheTTL:     fCacheTTL,
		FCacheKey:     site.FCacheKey,
		XMLRPCEnabled: site.XMLRPCEnabled,
	}
	if data.UseSSL {
		if data.SSLCertPath == "" {
			data.SSLCertPath = filepath.Join(cfg.Paths.Certificates, site.Domain, "fullchain.pem")
		}
		if data.SSLKeyPath == "" {
			data.SSLKeyPath = filepath.Join(cfg.Paths.Certificates, site.Domain, "privkey.pem")
		}
	}
	return data
}

func splitAliases(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, "\n")
	aliases := make([]string, 0, len(parts))
	for _, alias := range parts {
		alias = strings.TrimSpace(alias)
		if alias != "" {
			aliases = append(aliases, alias)
		}
	}
	return aliases
}
