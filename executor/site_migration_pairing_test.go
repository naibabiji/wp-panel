package executor

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const pairingPeerTestSchema = `CREATE TABLE site_migration_peers (
	id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', base_url TEXT NOT NULL DEFAULT '',
	certificate_sha256 TEXT NOT NULL DEFAULT '', local_certificate_sha256 TEXT NOT NULL DEFAULT '',
	inbound_credential_hash TEXT NOT NULL DEFAULT '', outbound_credential TEXT NOT NULL DEFAULT '',
	pair_token_hash TEXT NOT NULL DEFAULT '', pair_token_expires_at DATETIME, pairing_challenge TEXT NOT NULL DEFAULT '',
	pair_attempts INTEGER NOT NULL DEFAULT 0, protocol_version INTEGER NOT NULL DEFAULT 1,
	status TEXT NOT NULL DEFAULT 'pending', paired_at DATETIME, revoked_at DATETIME,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
)`

func TestSiteMigrationPairingBidirectionalPinnedTLS(t *testing.T) {
	dbA := newPairingTestDB(t)
	dbB := newPairingTestDB(t)
	certAPath, certA := newPairingTestCertificate(t, "panel-a")
	certBPath, certB := newPairingTestCertificate(t, "panel-b")
	serviceA, _ := NewSiteMigrationPairingService(dbA, "1.2.3", certAPath)
	serviceB, _ := NewSiteMigrationPairingService(dbB, "1.2.3", certBPath)

	serverA := newPairingTLSServer(t, certA, func(mux *http.ServeMux) {
		mux.HandleFunc("/api/site-migration/v1/pair/challenge", func(w http.ResponseWriter, r *http.Request) {
			var req SiteMigrationChallengeRequest
			if json.NewDecoder(r.Body).Decode(&req) != nil || serviceA.VerifyChallenge(r.Context(), req.PeerID, bearerValue(r), req) != nil {
				http.Error(w, "rejected", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
		})
	})
	defer serverA.Close()
	serverB := newPairingTLSServer(t, certB, func(mux *http.ServeMux) {
		mux.HandleFunc("/api/site-migration/v1/pair/redeem", func(w http.ResponseWriter, r *http.Request) {
			var req SiteMigrationRedeemRequest
			if json.NewDecoder(r.Body).Decode(&req) != nil || serviceB.Redeem(r.Context(), req) != nil {
				http.Error(w, "rejected", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
		})
		mux.HandleFunc("/api/site-migration/v1/preflight", func(w http.ResponseWriter, r *http.Request) {
			var req SiteMigrationPreflightRequest
			if json.NewDecoder(r.Body).Decode(&req) != nil || serviceB.AuthorizePeer(r.Context(), req.PeerID, bearerValue(r)) != nil {
				http.Error(w, "rejected", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(SiteMigrationPreflightResponse{Success: true, PanelVersion: "1.2.3",
				Conflicts: []SiteMigrationPreflightConflict{{Domain: "exists.example", SiteType: "wordpress"}}})
		})
		mux.HandleFunc("/api/site-migration/v1/peer/revoke", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				PeerID string `json:"peer_id"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil || serviceB.AuthorizePeer(r.Context(), req.PeerID, bearerValue(r)) != nil || serviceB.RevokePeer(r.Context(), req.PeerID) != nil {
				http.Error(w, "rejected", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
		})
	})
	defer serverB.Close()

	pkg, err := serviceB.GeneratePackage(context.Background(), serverB.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := serviceA.Connect(context.Background(), *pkg, serverA.URL); err != nil {
		t.Fatalf("Connect(): %v", err)
	}
	assertPairedCredentials(t, dbA, pkg.PeerID)
	assertPairedCredentials(t, dbB, pkg.PeerID)
	preflight, err := serviceA.RemotePreflight(context.Background(), pkg.PeerID, []string{"exists.example", "new.example"})
	if err != nil || len(preflight.Conflicts) != 1 || preflight.Conflicts[0].Domain != "exists.example" {
		t.Fatalf("RemotePreflight()=%+v err=%v", preflight, err)
	}
	if _, err := dbA.Exec(`UPDATE site_migration_peers SET protocol_version=1 WHERE id=?`, pkg.PeerID); err != nil {
		t.Fatal(err)
	}
	if _, err := serviceA.RemotePreflight(context.Background(), pkg.PeerID, []string{"new.example"}); err == nil {
		t.Fatal("legacy paired protocol remained usable after protocol upgrade")
	}
	if _, err := dbA.Exec(`UPDATE site_migration_peers SET protocol_version=? WHERE id=?`, siteMigrationProtocolVersion, pkg.PeerID); err != nil {
		t.Fatal(err)
	}
	if err := serviceB.AuthorizePeer(context.Background(), pkg.PeerID, "wrong credential"); err == nil {
		t.Fatal("wrong peer credential unexpectedly authorized")
	}
	if err := serviceA.RevokePeerEverywhere(context.Background(), pkg.PeerID); err != nil {
		t.Fatalf("RevokePeerEverywhere(): %v", err)
	}
	assertPeerStatus(t, dbA, pkg.PeerID, "revoked")
	assertPeerStatus(t, dbB, pkg.PeerID, "revoked")

	bad := *pkg
	bad.PeerID = "peer_invalidfingerprint01"
	bad.CertificateSHA256 = hashMigrationSecret("wrong certificate")
	if err := serviceA.Connect(context.Background(), bad, serverA.URL); err == nil {
		t.Fatal("wrong target fingerprint unexpectedly connected")
	}
}

func TestSiteMigrationPairingRevokesLocallyWhenRemoteIsUnavailable(t *testing.T) {
	db := newPairingTestDB(t)
	certPath, _ := newPairingTestCertificate(t, "panel-a")
	service, err := NewSiteMigrationPairingService(db, "1.2.3", certPath)
	if err != nil {
		t.Fatal(err)
	}
	service.httpTimeout = 50 * time.Millisecond
	peerID := "peer_00000000000000000001"
	if _, err := db.Exec(`INSERT INTO site_migration_peers
		(id,base_url,certificate_sha256,outbound_credential,protocol_version,status)
		VALUES (?,?,?,?,?,'paired')`, peerID, "https://127.0.0.1:1", strings.Repeat("a", 64), "outbound-credential", siteMigrationProtocolVersion); err != nil {
		t.Fatal(err)
	}
	if err := service.RevokePeerEverywhere(context.Background(), peerID); err != nil {
		t.Fatalf("RevokePeerEverywhere(): %v", err)
	}
	assertPeerStatus(t, db, peerID, "revoked")
}

func TestSiteMigrationPairingRejectsExpiredAndFiveFailures(t *testing.T) {
	db := newPairingTestDB(t)
	certPath, _ := newPairingTestCertificate(t, "panel-b")
	service, _ := NewSiteMigrationPairingService(db, "1.2.3", certPath)
	service.now = func() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) }
	pkg, err := service.GeneratePackage(context.Background(), "https://127.0.0.1:8443")
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return pkg.ExpiresAt.Add(time.Second) }
	if err := service.Redeem(context.Background(), SiteMigrationRedeemRequest{PeerID: pkg.PeerID}); err == nil {
		t.Fatal("expired token unexpectedly accepted")
	}

	service.now = func() time.Time { return time.Date(2026, 8, 23, 13, 0, 0, 0, time.UTC) }
	pkg, err = service.GeneratePackage(context.Background(), "https://127.0.0.1:8443")
	if err != nil {
		t.Fatal(err)
	}
	request := SiteMigrationRedeemRequest{PeerID: pkg.PeerID, Token: "invalid"}
	for range siteMigrationPairMaxFails + 1 {
		if err := service.Redeem(context.Background(), request); err == nil {
			t.Fatal("invalid token unexpectedly accepted")
		}
	}
	var attempts int
	if err := db.QueryRow(`SELECT pair_attempts FROM site_migration_peers WHERE id=?`, pkg.PeerID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != siteMigrationPairMaxFails {
		t.Fatalf("pair_attempts=%d, want %d", attempts, siteMigrationPairMaxFails)
	}
}

func newPairingTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(pairingPeerTestSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newPairingTestCertificate(t *testing.T, name string) (string, tlsCertificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	path := filepath.Join(t.TempDir(), name+".crt")
	if err := os.WriteFile(path, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return path, tlsCertificate{certPEM: certPEM, keyPEM: keyPEM}
}

type tlsCertificate struct{ certPEM, keyPEM []byte }

func newPairingTLSServer(t *testing.T, pair tlsCertificate, routes func(*http.ServeMux)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	routes(mux)
	server := httptest.NewUnstartedServer(mux)
	certificate, err := tls.X509KeyPair(pair.certPEM, pair.keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	return server
}

func bearerValue(r *http.Request) string {
	const prefix = "Bearer "
	value := r.Header.Get("Authorization")
	if len(value) <= len(prefix) || value[:len(prefix)] != prefix {
		return ""
	}
	return value[len(prefix):]
}

func assertPairedCredentials(t *testing.T, db *sql.DB, peerID string) {
	t.Helper()
	var status, inboundHash, outbound string
	if err := db.QueryRow(`SELECT status,inbound_credential_hash,outbound_credential FROM site_migration_peers WHERE id=?`, peerID).
		Scan(&status, &inboundHash, &outbound); err != nil {
		t.Fatal(err)
	}
	if status != "paired" || len(inboundHash) != 64 || len(outbound) < 40 {
		t.Fatalf("invalid paired credential state: status=%q inbound_len=%d outbound_len=%d", status, len(inboundHash), len(outbound))
	}
}

func assertPeerStatus(t *testing.T, db *sql.DB, peerID, want string) {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM site_migration_peers WHERE id=?`, peerID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != want {
		t.Fatalf("peer status=%q, want %q", status, want)
	}
}
