package database

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"strings"
)

func backfillSSLCertificateSources() error {
	var certPathColumn int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('websites') WHERE name='ssl_cert_path'`).Scan(&certPathColumn); err != nil {
		return err
	}
	if certPathColumn == 0 {
		return nil
	}
	rows, err := DB.Query(`SELECT id, ssl_cert_path FROM websites WHERE ssl_enabled=1 AND ssl_cert_source=''`)
	if err != nil {
		return err
	}
	type certificateRow struct {
		id   int
		path string
	}
	var certificates []certificateRow
	for rows.Next() {
		var item certificateRow
		if err := rows.Scan(&item.id, &item.path); err != nil {
			rows.Close()
			return err
		}
		certificates = append(certificates, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, item := range certificates {
		source := "manual"
		if certificateIssuedByLetsEncrypt(item.path) {
			source = "auto"
		}
		if _, err := DB.Exec(`UPDATE websites SET ssl_cert_source=? WHERE id=? AND ssl_cert_source=''`, source, item.id); err != nil {
			return err
		}
	}
	return nil
}

func certificateIssuedByLetsEncrypt(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	issuer := strings.ToLower(cert.Issuer.CommonName + " " + strings.Join(cert.Issuer.Organization, " "))
	return strings.Contains(issuer, "let's encrypt") || strings.Contains(issuer, "lets encrypt")
}
